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

// emlJob samlar tillståndet för ett mejl genom pipelinen så stegen kan vara
// egna funktioner: parse → kategori → classify/verify → extract per bilaga.
type emlJob struct {
	ctx     context.Context
	client  *anthropic.Client
	cfg     store.Config
	n       Notifier
	repo    *store.Repository
	emlPath string
	emailID int64
	content *eml.Content
}

// dbLog loggar en misslyckad bokförings-skrivning. Pipelinen ska inte stanna
// på DB-fel, men de ska synas i loggen istället för att tystas.
func (j *emlJob) dbLog(what string, err error) {
	if err != nil {
		j.n.Logf("   ⚠️  DB (%s): %v", what, err)
	}
}

func (j *emlJob) setEmailStatus(status string) {
	if j.emailID > 0 {
		j.dbLog("status="+status, j.repo.UpdateEmailStatus(j.emailID, status))
	}
}

// recordDecision persisterar ett AI-beslut kopplat till mejlet (no-op utan DB-rad).
func (j *emlJob) recordDecision(d *store.AIDecision) {
	if j.emailID <= 0 {
		return
	}
	d.EmailID = &j.emailID
	_, err := j.repo.InsertAIDecision(d)
	j.dbLog("ai_decision "+d.Step, err)
}

func processEml(ctx context.Context, client *anthropic.Client, cfg store.Config, emlPath string, n Notifier, idx, total int) {
	n.Logf("📧 [%d/%d] %s", idx, total, filepath.Base(emlPath))

	j := &emlJob{ctx: ctx, client: client, cfg: cfg, n: n, repo: n.Repo(), emlPath: emlPath}
	j.createEmailRow()

	content, err := eml.Parse(emlPath)
	if err != nil {
		n.Logf("   ❌ kunde inte parsa: %v", err)
		j.setEmailStatus("error")
		store.MoveToReview(cfg, emlPath, nil, nil, nil, nil, fmt.Sprintf("parse error: %v", err))
		n.BroadcastStats()
		n.BroadcastReview()
		return
	}
	j.content = content
	if j.emailID > 0 {
		j.dbLog("email-innehåll", j.repo.UpdateEmailContent(j.emailID, content.Subject, content.From, content.Date, content.Body))
	}

	// Icke-cert (faktura, följesedel, orderbekräftelse, teknisk doc, reklam, other):
	// kategorin är redan persisterad — arkivera på disk och hoppa över cert-vägen
	// (verify/extract). Reklam behålls i DB med sin kategori men surfas inte som
	// arbetsobjekt (filtreras bort i list_classified_mail).
	if category := j.classifyCategory(); category != ai.CategoryCertificate {
		n.Logf("   📁 kategori=%s — arkiverar, kör inte cert-flödet", category)
		j.setEmailStatus("archived")
		store.MoveToArchive(cfg, emlPath, "kategori: "+category)
		n.BroadcastStats()
		_ = os.Remove(emlPath)
		return
	}

	if len(content.Attachments) == 0 {
		n.Logf("   📦 arkiverat: inga PDF-bilagor")
		j.setEmailStatus("archived")
		store.MoveToArchive(cfg, emlPath, "inga PDF-bilagor")
		n.BroadcastStats()
		_ = os.Remove(emlPath)
		return
	}
	n.Logf("   subject: %s", content.Subject)
	n.Logf("   %d PDF-bilagor", len(content.Attachments))

	if !j.verifyIsCert() {
		return
	}

	j.setEmailStatus("certificates_found")

	anyFail := false
	for _, att := range content.Attachments {
		if ctx.Err() != nil {
			return
		}
		if !j.extractAttachment(att) {
			anyFail = true
		}
	}
	n.BroadcastStats()
	n.BroadcastQueue()
	if anyFail {
		n.BroadcastReview()
	}

	if !anyFail {
		j.setEmailStatus("completed")
		_ = os.Remove(emlPath)
	} else {
		j.setEmailStatus("partial")
		dst := filepath.Join(store.ReviewDir(cfg), filepath.Base(emlPath))
		_ = os.Rename(emlPath, dst)
	}
}

// createEmailRow skapar mejlets DB-rad (status "processing"). emailID förblir 0
// om skrivningen misslyckas — alla senare DB-steg blir då no-ops.
func (j *emlJob) createEmailRow() {
	email := &store.Email{
		Filename:    filepath.Base(j.emlPath),
		Status:      "processing",
		ProcessedAt: time.Now().Format(time.RFC3339),
	}
	id, err := j.repo.InsertEmail(email)
	if err != nil {
		j.n.Logf("   ⚠️  kunde inte spara email i DB: %v", err)
		return
	}
	j.emailID = id
}

// classifyCategory kör STEG 0: kategori-klassificering av ALL inkorgspost.
// Misslyckas anropet faller vi tillbaka på "certificate" (fail-open) så cert
// aldrig tappas. Kategorin + beslutet persisteras.
func (j *emlJob) classifyCategory() string {
	category := ai.CategoryCertificate
	mc, mcerr := ai.ClassifyMailCategory(j.ctx, j.n, j.client, j.content)
	if mcerr != nil {
		j.n.Logf("   ⚠️  kategori-classify-fel, antar certificate: %v", mcerr)
	} else if mc != nil && mc.Category != "" {
		category = mc.Category
		j.n.Logf("   🏷️  kategori: %s (%s) — %s", mc.Category, mc.Confidence, mc.Reason)
	}
	if j.emailID > 0 {
		j.dbLog("kategori", j.repo.UpdateEmailCategory(j.emailID, category))
	}
	decision := &store.AIDecision{
		Step:    "classify_category",
		Model:   ai.ModelClassify,
		Success: mcerr == nil,
	}
	if mcerr != nil {
		decision.ErrorMessage = mcerr.Error()
	}
	j.recordDecision(decision)
	return category
}

// verifyIsCert kör text-classify (billig signal) + bilage-verify och avgör om
// cert-flödet ska köras. Vid nej arkiveras mejlet (eller flyttas till review om
// classify och verify var oense). Båda besluten persisteras.
func (j *emlJob) verifyIsCert() bool {
	runFullFlow := true

	cls, cerr := ai.Classify(j.ctx, j.n, j.client, j.content)
	if cerr != nil {
		j.n.Logf("   ⚠️  classify-fel, fortsätter med verify: %v", cerr)
	}
	if cls != nil {
		decision := &store.AIDecision{
			Step:    "classify",
			Model:   ai.ModelClassify,
			Success: cerr == nil,
		}
		if cerr != nil {
			decision.ErrorMessage = cerr.Error()
		}
		j.recordDecision(decision)
	}

	ver, verr := ai.Verify(j.ctx, j.n, j.client, j.content)
	if verr != nil {
		j.n.Logf("   ⚠️  verify-fel, faller igenom till Sonnet: %v", verr)
	} else if !ver.AnyIsCert {
		if cls != nil && cls.IsCertMail {
			j.n.Logf("   📨 classify sa ja men verify sa nej: %s", ver.Reason)
			j.n.Logf("   🚫 inte cert-mejl: %s", ver.Reason)
			j.setEmailStatus("review")
			store.MoveToReview(j.cfg, j.emlPath, j.content, nil, nil, nil,
				"inte ett cert-mejl: "+ver.Reason)
			j.n.BroadcastReview()
		} else {
			if cls != nil {
				j.n.Logf("   🤔 text-classify sa nej (%s): %s", cls.Confidence, cls.Reason)
			}
			j.n.Logf("   📦 arkiverat: %s", ver.Reason)
			j.setEmailStatus("archived")
			store.MoveToArchive(j.cfg, j.emlPath, "inte ett cert-mejl: "+ver.Reason)
		}
		j.n.BroadcastStats()
		_ = os.Remove(j.emlPath)
		runFullFlow = false
	} else {
		if cls != nil && !cls.IsCertMail {
			j.n.Logf("   🪤 text-classify sa nej men verify hittade cert: %s", ver.Reason)
		}
	}

	if ver != nil {
		decision := &store.AIDecision{
			Step:    "verify",
			Model:   ai.ModelClassify,
			Success: verr == nil,
		}
		if verr != nil {
			decision.ErrorMessage = verr.Error()
		}
		j.recordDecision(decision)
	}

	return runFullFlow
}

// extractAttachment kör extract → validate → skriv till kön → bädda in metadata
// → persistera cert + beslut för EN bilaga. Returnerar false vid fel (bilagan
// hamnar i review respektive loggas vid skrivfel).
func (j *emlJob) extractAttachment(att eml.Attachment) bool {
	startTime := time.Now()
	bNums := eml.ExtractBNumbers(j.content.Subject, j.content.Body, att.Filename)
	ext, err := ai.Extract(j.ctx, j.n, j.client, att.Data, j.content.Subject, j.content.Body, att.Filename)
	processingMs := time.Since(startTime).Milliseconds()

	if err != nil {
		j.n.Logf("   ❌ %s — Claude-fel: %v", att.Filename, err)
		j.recordDecision(&store.AIDecision{
			Step:         "extract",
			Model:        ai.ModelExtract,
			DurationMs:   processingMs,
			Success:      false,
			ErrorMessage: err.Error(),
		})
		store.MoveToReview(j.cfg, j.emlPath, j.content, &att, nil, bNums, fmt.Sprintf("claude error: %v", err))
		return false
	}
	fails := cert.Validate(ext, bNums)
	if len(fails) > 0 {
		j.n.Logf("   ❌ %s — %s", att.Filename, strings.Join(fails, "; "))
		j.recordDecision(&store.AIDecision{
			Step:         "validate",
			Model:        ai.ModelExtract,
			DurationMs:   processingMs,
			Success:      false,
			ErrorMessage: strings.Join(fails, "; "),
		})
		store.MoveToReview(j.cfg, j.emlPath, j.content, &att, ext, bNums, strings.Join(fails, "; "))
		return false
	}
	name := cert.BuildFilename(ext, bNums)
	dst, err := store.WriteUniqueFile(store.QueueDir(j.cfg), name, att.Data)
	if err != nil {
		j.n.Logf("   ❌ skrivfel: %v", err)
		return false
	}
	sum := sha256.Sum256(att.Data)
	hash := hex.EncodeToString(sum[:])

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
		EmailSubject:      j.content.Subject,
		EmailFrom:         j.content.From,
		EmailDate:         j.content.Date,
		EmailBody:         j.content.Body,
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
		j.n.Logf("   ⚠️  kunde inte bädda in metadata i %s: %v", filepath.Base(dst), err)
	}

	if j.emailID > 0 {
		c := &store.Certificate{
			EmailID:           j.emailID,
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
		certID, err := j.repo.InsertCertificate(c)
		if err != nil {
			j.n.Logf("   ⚠️  kunde inte spara certifikat i DB: %v", err)
		} else {
			j.recordDecision(&store.AIDecision{
				CertificateID: &certID,
				Step:          "extract",
				Model:         ai.ModelExtract,
				DurationMs:    processingMs,
				Success:       true,
			})
		}
	}

	j.n.Logf("   ✅ %s", filepath.Base(dst))
	j.n.IncrementOK()
	return true
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
