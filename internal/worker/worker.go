// Package worker orchestrerar inbox-processeringen: en process av en .eml-fil
// (parse → classify → verify → extract → flytta) samt en pollande loop.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	"cert-renamer/internal/store"
)

const PollInterval = 30 * time.Second
const MailPause = 5 * time.Second

func processEml(ctx context.Context, client *anthropic.Client, cfg store.Config, emlPath string, n Notifier, idx, total int) {
	n.Logf("📧 [%d/%d] %s", idx, total, filepath.Base(emlPath))

	repo := n.Repo()

	// Skapa email-post i DB
	email := &store.Email{
		Filename:    filepath.Base(emlPath),
		Status:      "processing",
		ProcessedAt: time.Now().Format(time.RFC3339),
	}
	emailID, err := repo.InsertEmail(email)
	if err != nil {
		n.Logf("   ⚠️  kunde inte spara email i DB: %v", err)
	}

	content, err := eml.Parse(emlPath)
	if err != nil {
		n.Logf("   ❌ kunde inte parsa: %v", err)
		if emailID > 0 {
			repo.UpdateEmailStatus(emailID, "error")
		}
		store.MoveToReview(cfg, emlPath, nil, nil, nil, nil, fmt.Sprintf("parse error: %v", err))
		n.BroadcastStats()
		n.BroadcastReview()
		return
	}

	// Uppdatera email med innehåll
	if emailID > 0 {
		email.Subject = content.Subject
		email.FromAddr = content.From
		email.Date = content.Date
		email.Body = content.Body
		// Uppdatera email-posten med innehållet
		_, _ = repo.DB().Exec(`UPDATE emails SET subject = ?, from_addr = ?, date = ?, body = ? WHERE id = ?`,
			content.Subject, content.From, content.Date, content.Body, emailID)
	}

	// STEG 0: kategori-klassificering av ALL inkorgspost. Misslyckas anropet
	// faller vi tillbaka på "certificate" (fail-open) så cert aldrig tappas.
	category := ai.CategoryCertificate
	mc, mcerr := ai.ClassifyMailCategory(ctx, n, client, content)
	if mcerr != nil {
		n.Logf("   ⚠️  kategori-classify-fel, antar certificate: %v", mcerr)
	} else if mc != nil && mc.Category != "" {
		category = mc.Category
		n.Logf("   🏷️  kategori: %s (%s) — %s", mc.Category, mc.Confidence, mc.Reason)
	}
	if emailID > 0 {
		repo.UpdateEmailCategory(emailID, category)
		decision := &store.AIDecision{
			EmailID: &emailID,
			Step:    "classify_category",
			Model:   "claude-haiku-4-5-20251001",
			Success: mcerr == nil,
		}
		if mcerr != nil {
			decision.ErrorMessage = mcerr.Error()
		}
		repo.InsertAIDecision(decision)
	}

	// Icke-cert (faktura, följesedel, orderbekräftelse, teknisk doc, reklam, other):
	// kategorin är redan persisterad — arkivera på disk och hoppa över cert-vägen
	// (verify/extract). Reklam behålls i DB med sin kategori men surfas inte som
	// arbetsobjekt (filtreras bort i list_classified_mail).
	if category != ai.CategoryCertificate {
		n.Logf("   📁 kategori=%s — arkiverar, kör inte cert-flödet", category)
		if emailID > 0 {
			repo.UpdateEmailStatus(emailID, "archived")
		}
		store.MoveToArchive(cfg, emlPath, "kategori: "+category)
		n.BroadcastStats()
		_ = os.Remove(emlPath)
		return
	}

	if len(content.Attachments) == 0 {
		n.Logf("   📦 arkiverat: inga PDF-bilagor")
		if emailID > 0 {
			repo.UpdateEmailStatus(emailID, "archived")
		}
		store.MoveToArchive(cfg, emlPath, "inga PDF-bilagor")
		n.BroadcastStats()
		_ = os.Remove(emlPath)
		return
	}
	n.Logf("   subject: %s", content.Subject)
	n.Logf("   %d PDF-bilagor", len(content.Attachments))

	runFullFlow := true

	cls, cerr := ai.Classify(ctx, n, client, content)
	if cerr != nil {
		n.Logf("   ⚠️  classify-fel, fortsätter med verify: %v", cerr)
	}

	// Spara classify-beslut i DB
	if emailID > 0 && cls != nil {
		decision := &store.AIDecision{
			EmailID: &emailID,
			Step:    "classify",
			Model:   "claude-haiku-4-5-20251001",
			Success: cerr == nil,
		}
		if cerr != nil {
			decision.ErrorMessage = cerr.Error()
		}
		repo.InsertAIDecision(decision)
	}

	ver, verr := ai.Verify(ctx, n, client, content)
	if verr != nil {
		n.Logf("   ⚠️  verify-fel, faller igenom till Sonnet: %v", verr)
	} else if !ver.AnyIsCert {
		if cls != nil && cls.IsCertMail {
			n.Logf("   📨 classify sa ja men verify sa nej: %s", ver.Reason)
			n.Logf("   🚫 inte cert-mejl: %s", ver.Reason)
			if emailID > 0 {
				repo.UpdateEmailStatus(emailID, "review")
			}
			store.MoveToReview(cfg, emlPath, content, nil, nil, nil,
				"inte ett cert-mejl: "+ver.Reason)
			n.BroadcastReview()
		} else {
			if cls != nil {
				n.Logf("   🤔 text-classify sa nej (%s): %s", cls.Confidence, cls.Reason)
			}
			n.Logf("   📦 arkiverat: %s", ver.Reason)
			if emailID > 0 {
				repo.UpdateEmailStatus(emailID, "archived")
			}
			store.MoveToArchive(cfg, emlPath, "inte ett cert-mejl: "+ver.Reason)
		}
		n.BroadcastStats()
		_ = os.Remove(emlPath)
		runFullFlow = false
	} else {
		if cls != nil && !cls.IsCertMail {
			n.Logf("   🪤 text-classify sa nej men verify hittade cert: %s", ver.Reason)
		}
	}

	// Spara verify-beslut i DB
	if emailID > 0 && ver != nil {
		decision := &store.AIDecision{
			EmailID: &emailID,
			Step:    "verify",
			Model:   "claude-haiku-4-5-20251001",
			Success: verr == nil,
		}
		if verr != nil {
			decision.ErrorMessage = verr.Error()
		}
		repo.InsertAIDecision(decision)
	}

	if !runFullFlow {
		return
	}

	if emailID > 0 {
		repo.UpdateEmailStatus(emailID, "certificates_found")
	}

	anyFail := false
	for _, att := range content.Attachments {
		if ctx.Err() != nil {
			return
		}
		startTime := time.Now()
		bNums := eml.ExtractBNumbers(content.Subject, content.Body, att.Filename)
		ext, err := ai.Extract(ctx, n, client, att.Data, content.Subject, content.Body, att.Filename)
		processingMs := time.Since(startTime).Milliseconds()

		if err != nil {
			n.Logf("   ❌ %s — Claude-fel: %v", att.Filename, err)
			if emailID > 0 {
				decision := &store.AIDecision{
					EmailID:      &emailID,
					Step:         "extract",
					Model:        ai.ModelExtract,
					DurationMs:   processingMs,
					Success:      false,
					ErrorMessage: err.Error(),
				}
				repo.InsertAIDecision(decision)
			}
			store.MoveToReview(cfg, emlPath, content, &att, nil, bNums, fmt.Sprintf("claude error: %v", err))
			anyFail = true
			continue
		}
		fails := cert.Validate(ext, bNums)
		if len(fails) > 0 {
			n.Logf("   ❌ %s — %s", att.Filename, strings.Join(fails, "; "))
			if emailID > 0 {
				decision := &store.AIDecision{
					EmailID:      &emailID,
					Step:         "validate",
					Model:        ai.ModelExtract,
					DurationMs:   processingMs,
					Success:      false,
					ErrorMessage: strings.Join(fails, "; "),
				}
				repo.InsertAIDecision(decision)
			}
			store.MoveToReview(cfg, emlPath, content, &att, ext, bNums, strings.Join(fails, "; "))
			anyFail = true
			continue
		}
		name := cert.BuildFilename(ext, bNums)
		dst, err := store.WriteUniqueFile(store.QueueDir(cfg), name, att.Data)
		if err != nil {
			n.Logf("   ❌ skrivfel: %v", err)
			anyFail = true
			continue
		}
		sum := sha256.Sum256(att.Data)
		hash := hex.EncodeToString(sum[:])

		// Skapa PdfMeta med nya fälten
		meta := store.PdfMeta{
			Charge:            ext.Charge,
			Material:          ext.Material,
			EnStandardPresent: ext.EnStandardPresent,
			IsEnglish:         ext.IsEnglish,
			ProductForm:       ext.ProductForm,
			Dimensions:        ext.Dimensions,
			CountryOfOrigin:   ext.CountryOfOrigin,
			BNumbers:          bNums,
			Confidence:        ext.Confidence,
			Issues:            ext.Issues,
			EmailSubject:      content.Subject,
			EmailFrom:         content.From,
			EmailDate:         content.Date,
			EmailBody:         content.Body,
			ModelUsed:         ai.ModelExtract,
			TokensInput:       0, // Fylls i av logAICall
			TokensOutput:      0,
			ProcessingMs:      processingMs,
			OriginalFilename:  att.Filename,
			ExtractedAt:       time.Now().Format(time.RFC3339),
			Hash:              hash,
			Schema:            5,
			Status:            "queue",
		}
		if err := store.EmbedMetadata(dst, meta); err != nil {
			n.Logf("   ⚠️  kunde inte bädda in metadata i %s: %v", filepath.Base(dst), err)
		}

		// Spara certifikat i DB
		if emailID > 0 {
			cert := &store.Certificate{
				EmailID:           emailID,
				PDFHash:           hash,
				Filename:          filepath.Base(dst),
				OriginalFilename:  att.Filename,
				CertType:          ext.CertType,
				Charge:            ext.Charge,
				Material:          ext.Material,
				EnStandardPresent: ext.EnStandardPresent,
				IsEnglish:         ext.IsEnglish,
				ProductForm:       ext.ProductForm,
				Dimensions:        ext.Dimensions,
				CountryOfOrigin:   ext.CountryOfOrigin,
				BNumbers:          mustMarshal(bNums),
				Confidence:        ext.Confidence,
				Issues:            mustMarshal(ext.Issues),
				ModelUsed:         ai.ModelExtract,
				TokensInput:       0,
				TokensOutput:      0,
				ProcessingMs:      processingMs,
				Status:            "queue",
				ExtractedAt:       time.Now().Format(time.RFC3339),
			}
			certID, err := repo.InsertCertificate(cert)
			if err != nil {
				n.Logf("   ⚠️  kunde inte spara certifikat i DB: %v", err)
			} else {
				// Spara extract-beslut
				decision := &store.AIDecision{
					EmailID:       &emailID,
					CertificateID: &certID,
					Step:          "extract",
					Model:         ai.ModelExtract,
					DurationMs:    processingMs,
					Success:       true,
				}
				repo.InsertAIDecision(decision)
			}
		}

		n.Logf("   ✅ %s", filepath.Base(dst))
		n.IncrementOK()
	}
	n.BroadcastStats()
	n.BroadcastQueue()
	if anyFail {
		n.BroadcastReview()
	}

	if !anyFail {
		if emailID > 0 {
			repo.UpdateEmailStatus(emailID, "completed")
		}
		_ = os.Remove(emlPath)
	} else {
		if emailID > 0 {
			repo.UpdateEmailStatus(emailID, "partial")
		}
		dst := filepath.Join(store.ReviewDir(cfg), filepath.Base(emlPath))
		_ = os.Rename(emlPath, dst)
	}
}

func mustMarshal(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// runOneTick scannar inbox och processar alla hittade .eml-filer en gång.
// Returnerar true om ctx avbröts mitt i processeringen.
func runOneTick(ctx context.Context, client *anthropic.Client, cfg store.Config, n Notifier, tickN *atomic.Int64) bool {
	tick := tickN.Add(1)
	entries, err := os.ReadDir(cfg.InboxDir)
	if err != nil {
		n.Logf("⚠️  tick #%d — kan inte läsa inbox: %v", tick, err)
		return false
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			found++
		}
	}
	if found == 0 {
		if tick%10 == 1 {
			n.Logf("💤 tick #%d — inbox tom", tick)
		}
		return false
	}
	n.Logf("🔄 tick #%d — %d .eml att processa", tick, found)
	idx := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return true
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			continue
		}
		idx++
		processEml(ctx, client, cfg, filepath.Join(cfg.InboxDir, e.Name()), n, idx, found)
		if idx < found {
			select {
			case <-ctx.Done():
				return true
			case <-time.After(MailPause):
			}
		}
	}
	return false
}

// Run pollar inbox och kallar processEml för varje hittad .eml-fil tills ctx avbryts.
// kick är en valfri kanal som triggar en omedelbar tick (t.ex. efter eml-upload);
// nil-kanal stänger av kick-vägen och bara ticker används.
func Run(ctx context.Context, cfg store.Config, n Notifier, kick <-chan struct{}) {
	if cfg.ApiKey == "" {
		n.Logf("❌ Ingen API-nyckel konfigurerad — öppna ⚙️ Inställningar och spara en nyckel")
		return
	}
	client := anthropic.NewClient(option.WithAPIKey(cfg.ApiKey))
	n.Logf("🔍 Scannar %s var %s", cfg.InboxDir, PollInterval)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	var tickN atomic.Int64
	for {
		if runOneTick(ctx, &client, cfg, n, &tickN) {
			return
		}
		select {
		case <-ctx.Done():
			n.Logf("⏹  Stoppar worker")
			return
		case <-ticker.C:
		case <-kick:
		}
	}
}
