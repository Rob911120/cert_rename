package sickan

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// Notifier är minimal kontaktyta mot Server — bara loggning + broadcast efter
// disk-mutationer. Server uppfyller detta direkt.
type Notifier interface {
	Logf(format string, args ...any)
	BroadcastQueue()
	BroadcastReview()
	BroadcastStats()
	// DriveMonitorRoutine styr Monitor-skrivbordsklienten (UI-automation) för
	// rutinen "report_arrival" eller "inspection". save=true begär även en
	// Ctrl+S-sparning (server-sidan kan spärra det via inställning).
	DriveMonitorRoutine(routine, orderNumber string, save bool) error
}

// Toolbox knyter ihop config + notifier + repo för en chat-session.
// Monitor är inköps-ERP-klienten (kan vara nil om Monitor inte är konfigurerad
// eller login misslyckats — verktygen hanterar det gracefully).
type Toolbox struct {
	Cfg     store.Config
	N       Notifier
	Repo    *store.Repository
	Monitor *monitor.Client
}

// ToolDefs returnerar tool-defs som skickas till Claude i varje request.
// Sista entry har CacheControl=ephemeral satt så hela tools-arrayen + system
// cachas — ger ~10% input-pris från tur två i en session.
func ToolDefs() []anthropic.ToolUnionParam {
	last := promoteReviewTool
	last.CacheControl = anthropic.NewCacheControlEphemeralParam()
	return []anthropic.ToolUnionParam{
		{OfTool: &listQueueTool},
		{OfTool: &listReviewTool},
		{OfTool: &applyOrderTool},
		{OfTool: &analyzeReviewTool},
		{OfTool: &analyzeQueueTool},
		{OfTool: &updateQueueTool},
		{OfTool: &archiveReviewTool},
		{OfTool: &readPdfTool},
		{OfTool: &listClassifiedMailTool},
		{OfTool: &monitorFindPurchaseOrderTool},
		{OfTool: &monitorFindSupplierTool},
		{OfTool: &monitorFillMissingCertDataTool},
		{OfTool: &listDeliveryNotesTool},
		{OfTool: &readDeliveryNoteImageTool},
		{OfTool: &matchDeliveryNoteToPOTool},
		{OfTool: &proposeReceivingTool},
		{OfTool: &monitorRegisterArrivalTool},
		{OfTool: &monitorReportArrivalDirectTool},
		{OfTool: &monitorUIReportArrivalTool},
		{OfTool: &addImprovementTool},
		{OfTool: &listImprovementsTool},
		{OfTool: &last},
	}
}

// DispatchResult är resultatet av en tool-körning. Content är vad som skickas
// in i tool_result-blocket till Claude (kan vara text + dokument); Summary är
// en kort textversion som SSE-emittas till UI:t.
type DispatchResult struct {
	Content []anthropic.ToolResultBlockParamContentUnion
	Summary string
}

func textResult(s string) DispatchResult {
	return DispatchResult{
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfText: &anthropic.TextBlockParam{Text: s}},
		},
		Summary: s,
	}
}

// Dispatch kör en namngiven tool med JSON-input och returnerar resultat-block
// + en sammanfattning för UI:t. Fel översätts till is_error-tool_result av anroparen.
func (tb *Toolbox) Dispatch(name string, input json.RawMessage) (DispatchResult, error) {
	switch name {
	case "list_queue":
		return wrapText(tb.listQueue())
	case "list_review":
		return wrapText(tb.listReview())
	case "apply_queue_order":
		return wrapText(tb.applyOrder(input))
	case "analyze_review_item":
		return wrapText(tb.analyzeReview(input))
	case "analyze_queue_item":
		return wrapText(tb.analyzeQueue(input))
	case "update_queue_item":
		return wrapText(tb.updateQueue(input))
	case "archive_review_item":
		return wrapText(tb.archiveReview(input))
	case "read_pdf":
		return tb.readPdf(input)
	case "list_classified_mail":
		return wrapText(tb.listClassifiedMail(input))
	case "monitor_find_purchase_order":
		return wrapText(tb.monitorFindPurchaseOrder(input))
	case "monitor_find_supplier":
		return wrapText(tb.monitorFindSupplier(input))
	case "monitor_fill_missing_cert_data":
		return wrapText(tb.monitorFillMissingCertData(input))
	case "list_delivery_notes":
		return wrapText(tb.listDeliveryNotes(input))
	case "read_delivery_note_image":
		return tb.readDeliveryNoteImage(input)
	case "match_delivery_note_to_po":
		return wrapText(tb.matchDeliveryNoteToPO(input))
	case "propose_receiving":
		return wrapText(tb.proposeReceiving(input))
	case "monitor_register_arrival":
		return wrapText(tb.monitorRegisterArrival(input))
	case "monitor_report_arrival_direct":
		return wrapText(tb.monitorReportArrivalDirect(input))
	case "monitor_ui_report_arrival":
		return wrapText(tb.monitorUIReportArrival(input))
	case "promote_review_to_queue":
		return wrapText(tb.promoteReview(input))
	case "add_improvement":
		return wrapText(tb.addImprovement(input))
	case "list_improvements":
		return wrapText(tb.listImprovements())
	default:
		return DispatchResult{}, fmt.Errorf("okänt verktyg: %s", name)
	}
}

func wrapText(s string, err error) (DispatchResult, error) {
	if err != nil {
		return DispatchResult{}, err
	}
	return textResult(s), nil
}

// ---------------------------------------------------------------------------
// Tool-defs
// ---------------------------------------------------------------------------

var listQueueTool = anthropic.ToolParam{
	Name:        "list_queue",
	Description: anthropic.String("Listar alla cert-PDF:er i kön med metadata: filnamn, charge, material, dimensioner, B-nummer, confidence, issues. Använd alltid detta innan du föreslår en kö-ordning eller pekar ut en specifik post."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{},
	},
}

var listReviewTool = anthropic.ToolParam{
	Name:        "list_review",
	Description: anthropic.String("Listar alla poster i 'Behöver granskas' med base-namn, anledning och filer. Använd för att hitta vilken post användaren menar innan analyze_review_item."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{},
	},
}

var applyOrderTool = anthropic.ToolParam{
	Name:        "apply_queue_order",
	Description: anthropic.String("Sätter UI:ts kö-ordning till exakt den lista av filnamn du anger. Filnamn som inte finns i ordningen visas sist. Anropa BARA efter att användaren bekräftat den föreslagna mappningen."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"filenames": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Filnamn i önskad ordning, t.ex. [\"610042-rundstang-5-1.4307-B127196.pdf\", ...]",
			},
		},
		Required: []string{"filenames"},
	},
}

var analyzeReviewTool = anthropic.ToolParam{
	Name:        "analyze_review_item",
	Description: anthropic.String("Returnerar full text från .eml + ev. PDF-metadata + reason för en post i 'Behöver granskas'. Använd när användaren vill veta varför en post hamnade där."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Base-namnet på review-mappen, t.ex. \"BV-2024-08\".",
			},
		},
		Required: []string{"base"},
	},
}

var analyzeQueueTool = anthropic.ToolParam{
	Name:        "analyze_queue_item",
	Description: anthropic.String("Returnerar full PDF-metadata + sparat email-raw för en kö-post. Använd när användaren vill veta detaljer om en post som redan ligger i kön (t.ex. issues, original-filnamn, tidpunkt, mejl-context)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"filename": map[string]any{
				"type":        "string",
				"description": "Filnamnet i kön, t.ex. \"610042-rundstang-5-1.4307-B127196.pdf\".",
			},
		},
		Required: []string{"filename"},
	},
}

var updateQueueTool = anthropic.ToolParam{
	Name:        "update_queue_item",
	Description: anthropic.String("Uppdaterar fält på en kö-post och döper om filen enligt namnkonventionen. Lämna fält tomma som inte ska ändras. Returnerar nytt filnamn."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"filename":       map[string]any{"type": "string", "description": "Nuvarande filnamn i kön."},
			"charge":         map[string]any{"type": "string"},
			"material":       map[string]any{"type": "string", "description": "Kort form för filnamn, t.ex. S355."},
			"product_form":   map[string]any{"type": "string", "description": "rundstång, plåt, fyrkantsrör, ... eller 'okänt'."},
			"dimensions":     map[string]any{"type": "string"},
			"b_numbers":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"filename"},
	},
}

var readPdfTool = anthropic.ToolParam{
	Name:        "read_pdf",
	Description: anthropic.String("Bifogar en PDF-fil från kön eller granskning som dokument-block i tool_result så du kan läsa innehållet (text + bilder). Använd när användaren ber dig titta på själva PDF:en — t.ex. dubbelkolla charge, kontrollera dimensioner, läsa kommentarer."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"queue", "review", "approved"},
				"description": "Var filen ligger.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "PDF-filnamnet.",
			},
			"base": map[string]any{
				"type":        "string",
				"description": "Krävs om kind=review (review-mappens base-namn).",
			},
		},
		Required: []string{"kind", "filename"},
	},
}

var promoteReviewTool = anthropic.ToolParam{
	Name:        "promote_review_to_queue",
	Description: anthropic.String("Flyttar en post från 'Behöver granskas' in i kön med användar-bekräftade fält i samma format som det normala eml-flödet (inbäddad metadata, EmailRaw, namnkonvention). Bekräfta ALLTID samtliga fält explicit med användaren innan du anropar — kör read_pdf + analyze_review_item först om något är osäkert."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"base":         map[string]any{"type": "string", "description": "Review-mappens base-namn."},
			"pdf_filename": map[string]any{"type": "string", "description": "PDF-filnamnet i review-mappen."},
			"charge":       map[string]any{"type": "string"},
			"material":     map[string]any{"type": "string", "description": "Kort form, t.ex. S355 eller 1.4307."},
			"product_form": map[string]any{"type": "string", "description": "rundstång, plåt, fyrkantsrör, ... eller 'okänt'."},
			"dimensions":   map[string]any{"type": "string"},
			"b_numbers":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"base", "pdf_filename", "charge", "material", "product_form", "dimensions", "b_numbers"},
	},
}

var archiveReviewTool = anthropic.ToolParam{
	Name:        "archive_review_item",
	Description: anthropic.String("Arkiverar en post från 'Behöver granskas' till arkiverat/. Anropa bara efter explicit ja från användaren."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"base": map[string]any{
				"type":        "string",
				"description": "Base-namnet på review-mappen.",
			},
		},
		Required: []string{"base"},
	},
}

var listClassifiedMailTool = anthropic.ToolParam{
	Name:        "list_classified_mail",
	Description: anthropic.String("Listar inkorgspost som klassificerats och sparats i databasen (faktura, följesedel, orderbekräftelse, teknisk doc, m.m.) med ämne, avsändare, datum och kategori. Ange 'category' för att filtrera (t.ex. \"invoice\", \"delivery_note\"); utan filter listas alla arbetsobjekt utom reklam. Använd för att jobba med icke-cert-post som fångats in från inkorgen."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"category": map[string]any{
				"type":        "string",
				"enum":        []string{"certificate", "delivery_note", "invoice", "order_confirmation", "technical_doc", "reklam", "other"},
				"description": "Valfritt: filtrera på exakt en kategori. Lämna tomt för alla arbetsobjekt (reklam exkluderas då).",
			},
		},
	},
}

var addImprovementTool = anthropic.ToolParam{
	Name:        "add_improvement",
	Description: anthropic.String("Lägger till en post i förbättringslistan (Google Form → Sheet). Använd när användaren ber dig 'skicka en task', 'lägg till på förbättringslistan' eller liknande. Skriv koncist — en mening eller två som beskriver vad som borde förbättras eller fixas."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"text": map[string]any{"type": "string", "description": "Förbättringstexten."},
		},
		Required: []string{"text"},
	},
}

var listImprovementsTool = anthropic.ToolParam{
	Name:        "list_improvements",
	Description: anthropic.String("Läser förbättringslistan från det publika Google Sheet:et. Använd om användaren undrar vad som redan finns där, eller innan add_improvement för att kolla att samma sak inte redan står på listan."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{},
	},
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (tb *Toolbox) listQueue() (string, error) {
	// Använd DB om tillgänglig, annars fallback till filsystem
	if tb.Repo != nil {
		certs, err := tb.Repo.ListCertificates("queue")
		if err != nil {
			return "", err
		}
		items := make([]store.QueueItem, 0, len(certs))
		for _, c := range certs {
			item := store.QueueItem{
				Filename:    c.Filename,
				Charge:      c.Charge,
				Material:    c.Material,
				ProductForm: c.ProductForm,
				Dimensions:  c.Dimensions,
				Confidence:  c.Confidence,
			}
			if c.BNumbers != "" {
				_ = json.Unmarshal([]byte(c.BNumbers), &item.BNumbers)
			}
			if c.Issues != "" {
				_ = json.Unmarshal([]byte(c.Issues), &item.Issues)
			}
			items = append(items, item)
		}
		out, _ := json.Marshal(map[string]any{"items": items, "count": len(items)})
		return string(out), nil
	}
	// Fallback till filsystem
	items := readQueue(tb.Cfg)
	items = Apply(tb.Cfg, items)
	out, _ := json.Marshal(map[string]any{"items": items, "count": len(items)})
	return string(out), nil
}

func (tb *Toolbox) listReview() (string, error) {
	if tb.Cfg.InboxDir == "" {
		return `{"items":[],"count":0}`, nil
	}
	entries, err := os.ReadDir(store.ReviewDir(tb.Cfg))
	if err != nil {
		return `{"items":[],"count":0}`, nil
	}
	out := []store.ReviewItem{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(store.ReviewDir(tb.Cfg), e.Name())
		item := store.ReviewItem{Base: e.Name(), Files: []string{}}
		if data, err := os.ReadFile(filepath.Join(dir, "_reason.txt")); err == nil {
			item.Reason = strings.TrimSpace(string(data))
		}
		if files, err := os.ReadDir(dir); err == nil {
			for _, f := range files {
				if f.IsDir() || f.Name() == "_reason.txt" {
					continue
				}
				item.Files = append(item.Files, f.Name())
			}
		}
		out = append(out, item)
	}
	b, _ := json.Marshal(map[string]any{"items": out, "count": len(out)})
	return string(b), nil
}

func (tb *Toolbox) applyOrder(input json.RawMessage) (string, error) {
	var args struct {
		Filenames []string `json:"filenames"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if tb.Cfg.InboxDir == "" {
		return "", fmt.Errorf("ingen inbox vald")
	}
	if err := SaveOrder(tb.Cfg, args.Filenames); err != nil {
		return "", err
	}
	tb.N.BroadcastQueue()
	tb.N.Logf("🤖 Sickan: tillämpade kö-ordning (%d filnamn)", len(args.Filenames))
	out, _ := json.Marshal(map[string]any{"ok": true, "applied": len(args.Filenames)})
	return string(out), nil
}

func (tb *Toolbox) analyzeReview(input json.RawMessage) (string, error) {
	var args struct {
		Base string `json:"base"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if !safeName(args.Base) {
		return "", fmt.Errorf("ogiltig base")
	}
	dir := filepath.Join(store.ReviewDir(tb.Cfg), args.Base)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("review-mapp finns inte: %s", args.Base)
	}
	result := map[string]any{"base": args.Base}
	if data, err := os.ReadFile(filepath.Join(dir, "_reason.txt")); err == nil {
		result["reason"] = strings.TrimSpace(string(data))
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		full := filepath.Join(dir, f.Name())
		ext := strings.ToLower(filepath.Ext(f.Name()))
		switch ext {
		case ".eml":
			if c, err := eml.Parse(full); err == nil {
				body := c.Body
				if len(body) > 8192 {
					body = body[:8192] + "\n[trunkerad]"
				}
				result["email"] = map[string]any{
					"filename":    f.Name(),
					"subject":     c.Subject,
					"from":        c.From,
					"date":        c.Date,
					"body":        body,
					"attachments": attNames(c.Attachments),
				}
			}
		case ".pdf":
			entry := map[string]any{"filename": f.Name()}
			if m, ok := store.ReadMetadata(full); ok {
				entry["metadata"] = m
			}
			pdfs, _ := result["pdfs"].([]map[string]any)
			result["pdfs"] = append(pdfs, entry)
		}
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func (tb *Toolbox) analyzeQueue(input json.RawMessage) (string, error) {
	var args struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if !safeName(args.Filename) {
		return "", fmt.Errorf("ogiltigt filnamn")
	}
	pdfPath := filepath.Join(store.QueueDir(tb.Cfg), args.Filename)
	if _, err := os.Stat(pdfPath); err != nil {
		return "", fmt.Errorf("kö-post finns inte: %s", args.Filename)
	}
	result := map[string]any{"filename": args.Filename}
	if m, ok := store.ReadMetadata(pdfPath); ok {
		result["metadata"] = m
	} else {
		result["metadata_missing"] = true
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func (tb *Toolbox) updateQueue(input json.RawMessage) (string, error) {
	var args struct {
		Filename    string    `json:"filename"`
		Charge      *string   `json:"charge,omitempty"`
		Material    *string   `json:"material,omitempty"`
		ProductForm *string   `json:"product_form,omitempty"`
		Dimensions  *string   `json:"dimensions,omitempty"`
		BNumbers    *[]string `json:"b_numbers,omitempty"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if !safeName(args.Filename) {
		return "", fmt.Errorf("ogiltigt filnamn")
	}
	pdfPath := filepath.Join(store.QueueDir(tb.Cfg), args.Filename)
	meta, ok := store.ReadMetadata(pdfPath)
	if !ok {
		meta = &store.PdfMeta{OriginalFilename: args.Filename}
	}
	if args.Charge != nil {
		meta.Charge = *args.Charge
	}
	if args.Material != nil {
		meta.Material = *args.Material
	}
	if args.ProductForm != nil {
		meta.ProductForm = *args.ProductForm
	}
	if args.Dimensions != nil {
		meta.Dimensions = *args.Dimensions
	}
	if args.BNumbers != nil {
		meta.BNumbers = *args.BNumbers
	}
	ext := &cert.Extraction{
		Charge:        meta.Charge,
		MaterialShort: meta.Material,
		ProductForm:   meta.ProductForm,
		Dimensions:    meta.Dimensions,
	}
	newName := cert.BuildFilename(ext, meta.BNumbers)
	finalName, err := store.RenameQueueItem(tb.Cfg, args.Filename, newName, *meta)
	if err != nil {
		return "", err
	}
	if finalName != args.Filename {
		RenameInOrder(tb.Cfg, args.Filename, finalName)
	}

	// Uppdatera filnamn i DB
	if tb.Repo != nil && finalName != args.Filename {
		if cert, err := tb.Repo.GetCertificateByFilename(args.Filename); err == nil {
			_ = tb.Repo.UpdateCertificateFilename(cert.ID, finalName)
		}
	}

	tb.N.BroadcastQueue()
	tb.N.Logf("🤖 Sickan: %s → %s", args.Filename, finalName)
	out, _ := json.Marshal(map[string]any{"ok": true, "new_filename": finalName})
	return string(out), nil
}

func (tb *Toolbox) promoteReview(input json.RawMessage) (string, error) {
	var args struct {
		Base        string   `json:"base"`
		PdfFilename string   `json:"pdf_filename"`
		Charge      string   `json:"charge"`
		Material    string   `json:"material"`
		ProductForm string   `json:"product_form"`
		Dimensions  string   `json:"dimensions"`
		BNumbers    []string `json:"b_numbers"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ext := &cert.Extraction{
		IsEN10204_3_1: true,
		CertType:      "3.1",
		Charge:        args.Charge,
		Material:      args.Material,
		MaterialShort: args.Material,
		ProductForm:   args.ProductForm,
		Dimensions:    args.Dimensions,
		Confidence:    "high",
	}
	newName, err := store.PromoteReviewToQueue(tb.Cfg, args.Base, args.PdfFilename, ext, args.BNumbers)
	if err != nil {
		return "", err
	}

	// Infoga i DB
	if tb.Repo != nil {
		metaPath := filepath.Join(store.QueueDir(tb.Cfg), newName)
		if m, ok := store.ReadMetadata(metaPath); ok {
			cert := &store.Certificate{
				PDFHash:          m.Hash,
				Filename:         newName,
				OriginalFilename: m.OriginalFilename,
				CertType:         "3.1",
				Charge:           m.Charge,
				Material:         m.Material,
				MaterialShort:    m.MaterialShort,
				ProductForm:      m.ProductForm,
				Dimensions:       m.Dimensions,
				BNumbers:         marshalJSON(m.BNumbers),
				Confidence:       m.Confidence,
				Issues:           marshalJSON(m.Issues),
				ModelUsed:        m.ModelUsed,
				Status:           "queue",
				ExtractedAt:      m.ExtractedAt,
			}
			if _, insertErr := tb.Repo.InsertCertificate(cert); insertErr != nil {
				tb.N.Logf("⚠️  DB-insert vid promote misslyckades: %v", insertErr)
			}
		}
	}

	tb.N.BroadcastQueue()
	tb.N.BroadcastReview()
	tb.N.BroadcastStats()
	tb.N.Logf("🤖 Sickan: %s → kö (%s)", args.Base, newName)
	out, _ := json.Marshal(map[string]any{"ok": true, "new_filename": newName})
	return string(out), nil
}

func (tb *Toolbox) listClassifiedMail(input json.RawMessage) (string, error) {
	var args struct {
		Category string `json:"category"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", err
		}
	}
	if tb.Repo == nil {
		return `{"items":[],"count":0}`, nil
	}
	emails, err := tb.Repo.ListEmailsByCategory(args.Category)
	if err != nil {
		return "", err
	}
	type mailItem struct {
		ID           int64  `json:"id"`
		Filename     string `json:"filename"`
		Subject      string `json:"subject"`
		From         string `json:"from"`
		Date         string `json:"date"`
		MailCategory string `json:"mail_category"`
		Status       string `json:"status"`
	}
	items := make([]mailItem, 0, len(emails))
	for _, e := range emails {
		// Reklam surfas inte som arbetsobjekt om den inte uttryckligen efterfrågas.
		if args.Category == "" && e.MailCategory == ai.CategoryReklam {
			continue
		}
		items = append(items, mailItem{
			ID:           e.ID,
			Filename:     e.Filename,
			Subject:      e.Subject,
			From:         e.FromAddr,
			Date:         e.Date,
			MailCategory: e.MailCategory,
			Status:       e.Status,
		})
	}
	out, _ := json.Marshal(map[string]any{"items": items, "count": len(items)})
	return string(out), nil
}

func (tb *Toolbox) addImprovement(input json.RawMessage) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if err := AddImprovement(args.Text); err != nil {
		return "", err
	}
	tb.N.Logf("🤖 Sickan: ny post i förbättringslistan: %q", args.Text)
	return `{"ok":true}`, nil
}

func (tb *Toolbox) listImprovements() (string, error) {
	rows, err := ListImprovements()
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"rows": rows, "count": len(rows)})
	return string(out), nil
}

func (tb *Toolbox) archiveReview(input json.RawMessage) (string, error) {
	var args struct {
		Base string `json:"base"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if !safeName(args.Base) {
		return "", fmt.Errorf("ogiltig base")
	}
	src := filepath.Join(store.ReviewDir(tb.Cfg), args.Base)
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("review-mapp finns inte: %s", args.Base)
	}
	if err := os.MkdirAll(store.ArkiveratDir(tb.Cfg), 0755); err != nil {
		return "", err
	}
	dst := store.UniquePath(store.ArkiveratDir(tb.Cfg), args.Base)
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	tb.N.BroadcastReview()
	tb.N.BroadcastStats()
	tb.N.Logf("🤖 Sickan: arkiverade %s", args.Base)
	out, _ := json.Marshal(map[string]any{"ok": true, "archived": filepath.Base(dst)})
	return string(out), nil
}

func (tb *Toolbox) readPdf(input json.RawMessage) (DispatchResult, error) {
	var args struct {
		Kind     string `json:"kind"`
		Filename string `json:"filename"`
		Base     string `json:"base"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return DispatchResult{}, err
	}
	if !safeName(args.Filename) {
		return DispatchResult{}, fmt.Errorf("ogiltigt filnamn")
	}
	if !strings.EqualFold(filepath.Ext(args.Filename), ".pdf") {
		return DispatchResult{}, fmt.Errorf("bara PDF stöds")
	}
	var dir string
	switch args.Kind {
	case "queue":
		dir = store.QueueDir(tb.Cfg)
	case "approved":
		dir = store.ApprovedDir(tb.Cfg)
	case "review":
		if !safeName(args.Base) {
			return DispatchResult{}, fmt.Errorf("review kräver giltig base")
		}
		dir = filepath.Join(store.ReviewDir(tb.Cfg), args.Base)
	default:
		return DispatchResult{}, fmt.Errorf("ogiltig kind: %s", args.Kind)
	}
	full := filepath.Join(dir, args.Filename)
	data, err := os.ReadFile(full)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("kunde inte läsa %s: %w", args.Filename, err)
	}
	if len(data) > 32*1024*1024 {
		return DispatchResult{}, fmt.Errorf("PDF för stor (%d MB)", len(data)/(1024*1024))
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	intro := fmt.Sprintf("PDF bifogad: %s (%d KB) — innehåll i nästa block.", args.Filename, len(data)/1024)
	tb.N.Logf("🤖 Sickan läser %s/%s (%d KB)", args.Kind, args.Filename, len(data)/1024)
	return DispatchResult{
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfText: &anthropic.TextBlockParam{Text: intro}},
			{OfDocument: &anthropic.DocumentBlockParam{
				Source: anthropic.DocumentBlockParamSourceUnion{
					OfBase64: &anthropic.Base64PDFSourceParam{
						Data:      b64,
						MediaType: "application/pdf",
					},
				},
			}},
		},
		Summary: intro,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func safeName(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}

func marshalJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func attNames(atts []eml.Attachment) []string {
	out := make([]string, 0, len(atts))
	for _, a := range atts {
		out = append(out, a.Filename)
	}
	return out
}

// readQueue duplicerar server.listQueue:s minimala IO så Sickan-paketet inte
// behöver importera server. Läser bara fält från PDF-metadata; sidecar-JSON
// täcks av server-versionen och behövs inte här.
func readQueue(cfg store.Config) []store.QueueItem {
	if cfg.InboxDir == "" {
		return []store.QueueItem{}
	}
	entries, err := os.ReadDir(store.QueueDir(cfg))
	if err != nil {
		return []store.QueueItem{}
	}
	out := []store.QueueItem{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			continue
		}
		item := store.QueueItem{Filename: e.Name()}
		if m, ok := store.ReadMetadata(filepath.Join(store.QueueDir(cfg), e.Name())); ok {
			item.Charge = m.Charge
			item.Material = m.Material
			item.ProductForm = m.ProductForm
			item.Dimensions = m.Dimensions
			item.BNumbers = m.BNumbers
			item.Confidence = m.Confidence
			item.Issues = m.Issues
		}
		out = append(out, item)
	}
	return out
}
