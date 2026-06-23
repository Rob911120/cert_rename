// Package worker orchestrerar inbox-processeringen: en process av en .eml-fil
// (parse → classify → verify → extract → flytta) samt en pollande loop.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	content, err := eml.Parse(emlPath)
	if err != nil {
		n.Logf("   ❌ kunde inte parsa: %v", err)
		store.MoveToReview(cfg, emlPath, nil, nil, nil, nil, fmt.Sprintf("parse error: %v", err))
		n.BroadcastStats()
		n.BroadcastReview()
		return
	}
	if len(content.Attachments) == 0 {
		n.Logf("   📦 arkiverat: inga PDF-bilagor")
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

	ver, verr := ai.Verify(ctx, n, client, content)
	if verr != nil {
		n.Logf("   ⚠️  verify-fel, faller igenom till Sonnet: %v", verr)
	} else if !ver.AnyIsCert {
		if cls != nil && cls.IsCertMail {
			n.Logf("   📨 classify sa ja men verify sa nej: %s", ver.Reason)
			n.Logf("   🚫 inte cert-mejl: %s", ver.Reason)
			store.MoveToReview(cfg, emlPath, content, nil, nil, nil,
				"inte ett cert-mejl: "+ver.Reason)
			n.BroadcastReview()
		} else {
			if cls != nil {
				n.Logf("   🤔 text-classify sa nej (%s): %s", cls.Confidence, cls.Reason)
			}
			n.Logf("   📦 arkiverat: %s", ver.Reason)
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

	if !runFullFlow {
		return
	}

	anyFail := false
	for _, att := range content.Attachments {
		if ctx.Err() != nil {
			return
		}
		bNums := eml.ExtractBNumbers(content.Subject, content.Body, att.Filename)
		ext, err := ai.Extract(ctx, n, client, att.Data, content.Subject, content.Body, att.Filename)
		if err != nil {
			n.Logf("   ❌ %s — Claude-fel: %v", att.Filename, err)
			store.MoveToReview(cfg, emlPath, content, &att, nil, bNums, fmt.Sprintf("claude error: %v", err))
			anyFail = true
			continue
		}
		fails := cert.Validate(ext, bNums)
		if len(fails) > 0 {
			n.Logf("   ❌ %s — %s", att.Filename, strings.Join(fails, "; "))
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
		meta := store.PdfMeta{
			Charge:           ext.Charge,
			Material:         ext.MaterialShort,
			ProductForm:      ext.ProductForm,
			Dimensions:       ext.Dimensions,
			BNumbers:         bNums,
			Confidence:       ext.Confidence,
			Issues:           ext.Issues,
			OriginalFilename: att.Filename,
			ExtractedAt:      time.Now().Format(time.RFC3339),
			Schema:           4,
			EmailRaw:         store.EmailRawText(content),
			Status:           "queue",
			Hash:             hex.EncodeToString(sum[:]),
		}
		if err := store.EmbedMetadata(dst, meta); err != nil {
			n.Logf("   ⚠️  kunde inte bädda in metadata i %s: %v", filepath.Base(dst), err)
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
		_ = os.Remove(emlPath)
	} else {
		dst := filepath.Join(store.ReviewDir(cfg), filepath.Base(emlPath))
		_ = os.Rename(emlPath, dst)
	}
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
