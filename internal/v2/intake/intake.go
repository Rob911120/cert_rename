// Package intake pollar inkorgen och förvandlar .eml-filer till levande
// cert-rader via app-servicen. Samma hjärna som V1 (kategori → classify/verify
// → extract) men nya händer: PDF:en lagras EN gång under stabilt namn, ingen
// valideringsgrind, ingen mapp-koreografi. AI:t sitter bakom en port
// (AI-interfacet) så hela pipelinen testas offline med fejk.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"cert-renamer/internal/v2/ai"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/cert"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/eml"
	"cert-renamer/internal/v2/store"
)

const PollInterval = 30 * time.Second // fallback-poll när mappbevakning inte kan startas
const MailPause = 5 * time.Second

// BackstopInterval är säkerhetspollen som ALLTID kör bredvid fsnotify-bevakningen:
// på nätverks-/molnsynkade mappar (OneDrive/SMB) uteblir filsystem-eventen ofta,
// och då är det den här pollen som garanterar att filer till slut processas.
const BackstopInterval = 60 * time.Second

// SettleDelay är debounce/stabiliserings-fördröjningen: ett foto eller .eml kan
// fortfarande skrivas när Create-eventet kommer, så vi väntar tills skrivandet
// lugnat sig (och hoppar över filer nyare än så här i skanningen) innan process.
const SettleDelay = 2 * time.Second

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
	ExtractFromImage(ctx context.Context, img []byte, mediaType string) (*ai.DeliveryNoteExtraction, error)
}

// Intake är pipelinen. All mutation går genom App (enda skrivvägen).
type Intake struct {
	App    *app.App
	AI     AI
	Config func() store.Config
}

// Run bevakar cert-inkorgen (och, om satt, följesedel-inkorgen) tills ctx
// avbryts. fsnotify ger snabb reaktion; en backstop-poll kör alltid bredvid
// (nätverks-/molnmappar tappar events). kick triggar en omedelbar körning.
// Kan inte fsnotify startas faller vi tillbaka på ren poll (pollLoop).
func (in *Intake) Run(ctx context.Context, kick <-chan struct{}) {
	cfg := in.Config()
	suffix := ""
	if cfg.DeliveryInboxDir != "" {
		suffix = " + " + cfg.DeliveryInboxDir + " (följesedlar)"
	}
	in.App.Notify.Logf("🔍 Bevakar %s%s (backstop var %s)", cfg.InboxDir, suffix, BackstopInterval)

	if in.processAll(ctx) {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		in.App.Notify.Logf("⚠️  kunde inte starta mappbevakning (%v) — faller tillbaka på poll", err)
		in.pollLoop(ctx, kick)
		return
	}
	defer watcher.Close()
	for _, d := range []string{cfg.InboxDir, cfg.DeliveryInboxDir} {
		if d == "" {
			continue
		}
		if err := watcher.Add(d); err != nil {
			in.App.Notify.Logf("⚠️  kan inte bevaka %s: %v — backstop-poll täcker den", d, err)
		}
	}

	backstop := time.NewTicker(BackstopInterval)
	defer backstop.Stop()
	var debounce <-chan time.Time // satt av senaste filsystem-event; nil = inaktiv
	for {
		select {
		case <-ctx.Done():
			in.App.Notify.Logf("⏹  Stoppar intag")
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
				debounce = time.After(SettleDelay) // vänta ut skrivandet innan process
			}
		case wErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			in.App.Notify.Logf("⚠️  bevakningsfel: %v", wErr)
		case <-debounce:
			debounce = nil
			if in.processAll(ctx) {
				return
			}
		case <-backstop.C:
			if in.processAll(ctx) {
				return
			}
		case <-kick:
			if in.processAll(ctx) {
				return
			}
		}
	}
}

// pollLoop är fallback-beteendet (fsnotify kunde inte startas): ren poll av
// båda mapparna var PollInterval, plus kick.
func (in *Intake) pollLoop(ctx context.Context, kick <-chan struct{}) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			in.App.Notify.Logf("⏹  Stoppar intag")
			return
		case <-ticker.C:
		case <-kick:
		}
		if in.processAll(ctx) {
			return
		}
	}
}

// processAll kör en runda över cert-inkorgen och (om satt) följesedel-inkorgen.
// Returnerar true om ctx avbröts.
func (in *Intake) processAll(ctx context.Context) bool {
	if in.ProcessInboxOnce(ctx) {
		return true
	}
	return in.ProcessDeliveryOnce(ctx)
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
		path := filepath.Join(inbox, e.Name())
		if in.App.LatestEmailStatus(ctx, e.Name(), fileSHA256(path)) == "error" {
			continue // redan felad (samma innehåll) — vänta på Rob
		}
		emls = append(emls, path)
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
	emailID := in.App.EmailStarted(ctx, filepath.Base(emlPath), fileSHA256(emlPath))

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
		in.removeEml(ctx, emailID, emlPath)
		return
	}
	pdfs := pdfAttachments(content.Attachments)
	if len(pdfs) == 0 {
		n.Logf("   📦 inga PDF-bilagor")
		in.App.EmailFinished(ctx, emailID, "archived", "inga PDF-bilagor")
		in.removeEml(ctx, emailID, emlPath)
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
		in.removeEml(ctx, emailID, emlPath)
		return
	}

	anyFail := false
	for _, att := range pdfs {
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
	in.removeEml(ctx, emailID, emlPath)
}

// removeEml raderar en färdigbehandlad .eml. Misslyckas raderingen (t.ex.
// skrivskyddad fil på Windows) sätts e-postens status till error så att
// skip-logiken i ProcessInboxOnce tar den — annars skulle mejlet köras genom
// hela AI-pipelinen igen var 30:e sekund så länge filen ligger kvar.
func (in *Intake) removeEml(ctx context.Context, emailID int64, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		in.App.Notify.Logf("   ⚠️  kunde inte radera %s — hoppar över den framöver; radera manuellt: %v",
			filepath.Base(path), err)
		in.App.EmailFinished(ctx, emailID, "error", fmt.Sprintf("färdigbehandlad men kunde inte raderas: %v", err))
	}
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
	if err != nil {
		return certID(c), dup, err
	}
	// Extract-anropet är redan betalt — logga det även när intaget visade sig
	// vara en dublett, annars ljuger kostnads-/auditstatistiken.
	in.App.RecordAICall(ctx, certID(c), "extract", res.Model, res.TokensIn, res.TokensOut, res.DurationMS, true, "")
	if dup {
		return certID(c), true, nil
	}
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

// fileSHA256 är innehålls-hashen som (tillsammans med filnamnet) nycklar
// e-postens fel-skip. Tom sträng vid läsfel — matchar då aldrig en riktig rad.
func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
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
	// Extract-anropet är redan betalt — logga det även för dubbletter, annars
	// ljuger kostnads-/auditstatistiken.
	in.App.RecordAICall(ctx, c.ID, "extract", res.Model, res.TokensIn, res.TokensOut, res.DurationMS, true, "")
	if dup {
		n.Logf("   ♻️  %s — dublett (hash), hoppar över", att.Filename)
		return true
	}

	// Förslagspasset: koppla mot kända orderrader på B-nummer.
	if created, err := in.App.SuggestLinksByBNumber(ctx, c.ID); err != nil {
		n.Logf("   ⚠️  förslagspass: %v", err)
	} else if created > 0 {
		n.Logf("   🔗 %d länkförslag", created)
	}
	return true
}
