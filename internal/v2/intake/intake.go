// Package intake pollar inkorgen och förvandlar .eml-filer till levande
// cert-rader via app-servicen. Samma hjärna som V1 (kategori → classify/verify
// → extract) men nya händer: PDF:en lagras EN gång under stabilt namn, ingen
// valideringsgrind, ingen mapp-koreografi. AI:t sitter bakom en port
// (AI-interfacet) så hela pipelinen testas offline med fejk.
package intake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
)

const PollInterval = 30 * time.Second
const MailPause = 5 * time.Second

// ExtractResult är extraktionen + dess faktiska kostnad (riktiga tokental —
// fixar V1:s nollade tokens_input/output).
type ExtractResult struct {
	Extraction *cert.Extraction
	Model      string
	TokensIn   int64
	TokensOut  int64
	DurationMS int64
}

// AI är intagets port mot Claude. Fejkas i test.
type AI interface {
	MailCategory(ctx context.Context, c *eml.Content) (*ai.MailClassification, error)
	Classify(ctx context.Context, c *eml.Content) (*cert.Classification, error)
	Verify(ctx context.Context, c *eml.Content) (*cert.Verification, error)
	Extract(ctx context.Context, pdf []byte, subject, body, filename string) (*ExtractResult, error)
}

// Intake är pipelinen. All mutation går genom App (enda skrivvägen).
type Intake struct {
	App    *app.App
	AI     AI
	Config func() v1store.Config
}

// Run pollar inkorgen tills ctx avbryts. kick triggar en omedelbar tick
// (t.ex. efter drag-drop-upload); nil stänger av kick-vägen.
func (in *Intake) Run(ctx context.Context, kick <-chan struct{}) {
	in.App.Notify.Logf("🔍 Scannar %s var %s", in.Config().InboxDir, PollInterval)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		if in.ProcessInboxOnce(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			in.App.Notify.Logf("⏹  Stoppar intag")
			return
		case <-ticker.C:
		case <-kick:
		}
	}
}

// ProcessInboxOnce processar alla .eml i inkorgen en gång. Filer vars senaste
// körning slutade i fel hoppas över (ligger kvar synliga tills Rob agerar).
// Returnerar true om ctx avbröts.
func (in *Intake) ProcessInboxOnce(ctx context.Context) bool {
	inbox := in.Config().InboxDir
	entries, err := os.ReadDir(inbox)
	if err != nil {
		in.App.Notify.Logf("⚠️  kan inte läsa inbox: %v", err)
		return false
	}
	var emls []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			continue
		}
		if in.App.LatestEmailStatus(ctx, e.Name()) == "error" {
			continue // redan felad — vänta på Rob
		}
		emls = append(emls, filepath.Join(inbox, e.Name()))
	}
	for i, path := range emls {
		if ctx.Err() != nil {
			return true
		}
		in.App.Notify.Logf("📧 [%d/%d] %s", i+1, len(emls), filepath.Base(path))
		in.ProcessEml(ctx, path)
		if i < len(emls)-1 {
			select {
			case <-ctx.Done():
				return true
			case <-time.After(MailPause):
			}
		}
	}
	return false
}

// ProcessEml kör hela pipelinen för EN .eml-fil.
func (in *Intake) ProcessEml(ctx context.Context, emlPath string) {
	n := in.App.Notify
	emailID := in.App.EmailStarted(ctx, filepath.Base(emlPath))

	content, err := eml.Parse(emlPath)
	if err != nil {
		n.Logf("   ❌ kunde inte parsa: %v", err)
		in.App.EmailFinished(ctx, emailID, "error", fmt.Sprintf("parse error: %v", err))
		return // filen ligger kvar; skip-logiken hindrar loop
	}

	// STEG 0: kategori (fail-open: certifikat tappas aldrig på klassificeringsfel).
	category := ai.CategoryCertificate
	if mc, err := in.AI.MailCategory(ctx, content); err != nil {
		n.Logf("   ⚠️  kategori-classify-fel, antar certificate: %v", err)
	} else if mc != nil && mc.Category != "" {
		category = mc.Category
		n.Logf("   🏷️  kategori: %s (%s) — %s", mc.Category, mc.Confidence, mc.Reason)
	}
	if category != ai.CategoryCertificate {
		n.Logf("   📁 kategori=%s — inte cert, tas bort", category)
		in.App.EmailFinished(ctx, emailID, "archived", "kategori: "+category)
		_ = os.Remove(emlPath)
		return
	}
	if len(content.Attachments) == 0 {
		n.Logf("   📦 inga PDF-bilagor")
		in.App.EmailFinished(ctx, emailID, "archived", "inga PDF-bilagor")
		_ = os.Remove(emlPath)
		return
	}

	// Classify (billig signal) + verify — samma matris som V1: verify avgör,
	// oenighet blir fel-rad (Rob tittar), verify-fel faller igenom till extract.
	cls, clsErr := in.AI.Classify(ctx, content)
	if clsErr != nil {
		n.Logf("   ⚠️  classify-fel, fortsätter med verify: %v", clsErr)
	}
	ver, verErr := in.AI.Verify(ctx, content)
	if verErr != nil {
		n.Logf("   ⚠️  verify-fel, faller igenom till extraktion: %v", verErr)
	} else if !ver.AnyIsCert {
		if cls != nil && cls.IsCertMail {
			n.Logf("   🪤 classify sa ja men verify sa nej: %s", ver.Reason)
			in.App.EmailFinished(ctx, emailID, "error", "classify/verify oense: "+ver.Reason)
			return // ligger kvar för Rob
		}
		n.Logf("   📦 inte cert-mejl: %s", ver.Reason)
		in.App.EmailFinished(ctx, emailID, "archived", "inte ett cert-mejl: "+ver.Reason)
		_ = os.Remove(emlPath)
		return
	}

	anyFail := false
	for _, att := range content.Attachments {
		if ctx.Err() != nil {
			return
		}
		if !in.ingestAttachment(ctx, content, att) {
			anyFail = true
		}
	}
	if anyFail {
		in.App.EmailFinished(ctx, emailID, "error", "en eller flera bilagor kunde inte extraheras")
		return // kvar i inkorgen; lyckade bilagor är redan dedupe-skyddade
	}
	in.App.EmailFinished(ctx, emailID, "completed", "")
	_ = os.Remove(emlPath)
}

// IngestPDF tar in en direktuppladdad PDF (drag-drop) — samma väg som en
// mailbilaga men utan mailkontext. B-nummer plockas ur filnamnet.
func (in *Intake) IngestPDF(ctx context.Context, filename string, data []byte) (int64, bool, error) {
	res, err := in.AI.Extract(ctx, data, "", "", filename)
	if err != nil {
		in.App.RecordAICall(ctx, 0, "extract", ai.ModelExtract, 0, 0, 0, false, err.Error())
		return 0, false, err
	}
	c, dup, err := in.App.IngestCert(ctx, app.IngestInput{
		OriginalFilename: filename,
		Data:             data,
		Extraction:       res.Extraction,
		BNumbers:         eml.ExtractBNumbers(filename),
		Model:            res.Model,
		TokensIn:         res.TokensIn,
		TokensOut:        res.TokensOut,
		ProcessingMS:     res.DurationMS,
	})
	if err != nil || dup {
		return certID(c), dup, err
	}
	in.App.RecordAICall(ctx, c.ID, "extract", res.Model, res.TokensIn, res.TokensOut, res.DurationMS, true, "")
	if _, err := in.App.SuggestLinksByBNumber(ctx, c.ID); err != nil {
		in.App.Notify.Logf("   ⚠️  förslagspass: %v", err)
	}
	return c.ID, false, nil
}

func certID(c *domain.Cert) int64 {
	if c == nil {
		return 0
	}
	return c.ID
}

// ingestAttachment extraherar och tar in EN PDF-bilaga. Ingen valideringsgrind:
// ofullständiga cert blir levande rader. Returnerar false vid extraktionsfel.
func (in *Intake) ingestAttachment(ctx context.Context, content *eml.Content, att eml.Attachment) bool {
	n := in.App.Notify
	bNums := eml.ExtractBNumbers(content.Subject, content.Body, att.Filename)

	res, err := in.AI.Extract(ctx, att.Data, content.Subject, content.Body, att.Filename)
	if err != nil {
		n.Logf("   ❌ %s — extraktionsfel: %v", att.Filename, err)
		in.App.RecordAICall(ctx, 0, "extract", ai.ModelExtract, 0, 0, 0, false, err.Error())
		return false
	}

	c, dup, err := in.App.IngestCert(ctx, app.IngestInput{
		OriginalFilename: att.Filename,
		Data:             att.Data,
		EmailSubject:     content.Subject,
		EmailFrom:        content.From,
		EmailDate:        content.Date,
		EmailBody:        content.Body,
		Extraction:       res.Extraction,
		BNumbers:         bNums,
		Model:            res.Model,
		TokensIn:         res.TokensIn,
		TokensOut:        res.TokensOut,
		ProcessingMS:     res.DurationMS,
	})
	if err != nil {
		n.Logf("   ❌ %s — kunde inte ta in: %v", att.Filename, err)
		return false
	}
	if dup {
		n.Logf("   ♻️  %s — dublett (hash), hoppar över", att.Filename)
		return true
	}
	in.App.RecordAICall(ctx, c.ID, "extract", res.Model, res.TokensIn, res.TokensOut, res.DurationMS, true, "")

	// Förslagspasset: koppla mot kända orderrader på B-nummer.
	if created, err := in.App.SuggestLinksByBNumber(ctx, c.ID); err != nil {
		n.Logf("   ⚠️  förslagspass: %v", err)
	} else if created > 0 {
		n.Logf("   🔗 %d länkförslag", created)
	}
	return true
}
