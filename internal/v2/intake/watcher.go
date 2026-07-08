package intake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/eml"
)

// ---------------------------------------------------------------------------
// Följesedel-vägen: en EGEN mapp (Config.DeliveryInboxDir) dit inmejlade
// följesedel-foton landar. Routing sker per mapp — allt här behandlas som
// följesedel (ingen cert-kategorikoll). Bild → vision → levande DB-rad → lokal
// auto-match. Cert-inkorgen (ProcessInboxOnce) är oförändrad.
// ---------------------------------------------------------------------------

// ProcessDeliveryOnce processar alla .eml i följesedel-inkorgen en gång.
// No-op om ingen följesedel-mapp är konfigurerad. Returnerar true om ctx avbröts.
func (in *Intake) ProcessDeliveryOnce(ctx context.Context) bool {
	dir := in.Config().DeliveryInboxDir
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		in.App.Notify.Logf("⚠️  kan inte läsa följesedel-mapp: %v", err)
		return false
	}
	var emls []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if in.App.LatestEmailStatus(ctx, e.Name(), fileSHA256(path)) == "error" {
			continue
		}
		emls = append(emls, path)
	}
	for i, path := range emls {
		if ctx.Err() != nil {
			return true
		}
		in.App.Notify.Logf("📸 [%d/%d] följesedel %s", i+1, len(emls), filepath.Base(path))
		in.ProcessDeliveryEml(ctx, path)
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

// ProcessDeliveryEml kör följesedel-pipelinen för EN .eml: plockar bild-bilagan/
// bilagorna, kör vision, skapar levande följesedel-rader och auto-matchar lokalt.
// En följesedel per bild (flera foton → flera rader).
func (in *Intake) ProcessDeliveryEml(ctx context.Context, emlPath string) {
	n := in.App.Notify
	emailID := in.App.EmailStarted(ctx, filepath.Base(emlPath), fileSHA256(emlPath))

	content, err := eml.Parse(emlPath)
	if err != nil {
		n.Logf("   ❌ kunde inte parsa: %v", err)
		in.App.EmailFinished(ctx, emailID, "error", fmt.Sprintf("parse error: %v", err))
		return
	}

	imgs := imageAttachments(content.Attachments)
	if len(imgs) == 0 {
		n.Logf("   🖼️  ingen bild-bilaga — inte en följesedel")
		in.App.EmailFinished(ctx, emailID, "archived", "ingen bild-bilaga")
		in.removeEml(ctx, emailID, emlPath)
		return
	}

	anyFail := false
	for _, att := range imgs {
		if ctx.Err() != nil {
			return
		}
		if !in.ingestDeliveryImage(ctx, att) {
			anyFail = true
		}
	}
	if anyFail {
		in.App.EmailFinished(ctx, emailID, "error", "en eller flera bilder kunde inte läsas av")
		return // kvar i mappen; lyckade bilder är redan dedupe-skyddade
	}
	in.App.EmailFinished(ctx, emailID, "completed", "")
	in.removeEml(ctx, emailID, emlPath)
}

// ingestDeliveryImage kör vision + intag + lokal matchning för EN bild.
// Returnerar false vid vision-/intagsfel. Global tokenkostnad räknas av
// bas-loggern (ai.logAICall.RecordUsage), så ingen separat cert-kopplad
// ai_calls-rad behövs här.
func (in *Intake) ingestDeliveryImage(ctx context.Context, att eml.Attachment) bool {
	n := in.App.Notify
	dn, err := in.AI.ExtractFromImage(ctx, att.Data, att.MediaType)
	if err != nil {
		n.Logf("   ❌ %s — vision-fel: %v", att.Filename, err)
		return false
	}
	d, dup, err := in.App.IngestDeliveryNote(ctx, app.DeliveryNoteInput{
		OriginalFilename:   att.Filename,
		Data:               att.Data,
		Supplier:           dn.Supplier,
		DeliveryDate:       dn.DeliveryDate,
		OrderNumber:        dn.OrderNumber,
		Charge:             dn.Charge,
		Material:           dn.Material,
		Quantity:           dn.Quantity,
		Unit:               dn.Unit,
		DeliveryNoteNumber: dn.DeliveryNoteNumber,
		BNumbers:           dn.BNumbers,
		Confidence:         dn.Confidence,
	})
	if err != nil {
		n.Logf("   ❌ %s — kunde inte ta in: %v", att.Filename, err)
		return false
	}
	if dup {
		n.Logf("   ♻️  %s — dublett (bild-hash), hoppar över", att.Filename)
		return true
	}
	// Lokal auto-match mot synkade order_rows — inget live-Monitor-anrop.
	if m, err := in.App.MatchDeliveryNoteLocal(ctx, d.ID); err != nil {
		n.Logf("   ⚠️  matchningspass: %v", err)
	} else if m.Matched {
		n.Logf("   🔗 %s — matchad mot order %s", att.Filename, m.Row.OrderNumber)
	} else if len(m.Candidates) > 1 {
		n.Logf("   🤔 %s — %d kandidatrader, välj manuellt", att.Filename, len(m.Candidates))
	}
	return true
}

// pdfAttachments filtrerar ut cert-bärande PDF-bilagor (cert-vägen).
func pdfAttachments(atts []eml.Attachment) []eml.Attachment {
	var out []eml.Attachment
	for _, a := range atts {
		if a.MediaType == "application/pdf" {
			out = append(out, a)
		}
	}
	return out
}

// imageAttachments filtrerar ut bild-bilagor (följesedel-vägen).
func imageAttachments(atts []eml.Attachment) []eml.Attachment {
	var out []eml.Attachment
	for _, a := range atts {
		if strings.HasPrefix(a.MediaType, "image/") {
			out = append(out, a)
		}
	}
	return out
}
