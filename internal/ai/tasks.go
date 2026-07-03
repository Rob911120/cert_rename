// Package ai kapslar Claude-anropen för cert-renamer: klassificering av mejl,
// verifiering av bifogade PDF:er, samt fält-extraktion från en cert-PDF.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
)

// callTool gör ett Messages-anrop där svaret förväntas komma via tool_use,
// plockar ut verktygets input och unmarshalar till T. Delas av alla
// task-funktioner så anrops-/avkodningsmönstret bara finns på ett ställe.
func callTool[T any](ctx context.Context, client *anthropic.Client, params anthropic.MessageNewParams) (*T, anthropic.Usage, error) {
	resp, err := client.Messages.New(ctx, params)
	if err != nil {
		return nil, anthropic.Usage{}, err
	}
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			tu := block.AsToolUse()
			var out T
			if err := json.Unmarshal(tu.Input, &out); err != nil {
				return nil, resp.Usage, fmt.Errorf("unmarshal: %w", err)
			}
			return &out, resp.Usage, nil
		}
	}
	return nil, resp.Usage, fmt.Errorf("inget tool_use-svar från Claude")
}

// Extract anropar sonnet med en PDF + email-context och returnerar extraherade fält.
func Extract(ctx context.Context, log Logger, client *anthropic.Client, pdf []byte, subject, body, filename string) (*cert.Extraction, error) {
	b64 := base64.StdEncoding.EncodeToString(pdf)
	userText := fmt.Sprintf("Extrahera fält från detta certifikat.\n\nMejlets ämnesrad: %s\nBilagans originalfilnamn: %s\n\nMejlets brödtext:\n%s\n",
		subject, filename, body)
	return logAICall(log, "sonnet Extract("+filename+")",
		func() (*cert.Extraction, anthropic.Usage, error) {
			return callTool[cert.Extraction](ctx, client, anthropic.MessageNewParams{
				Model:     ModelExtract,
				MaxTokens: 1024,
				Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
				System:    []anthropic.TextBlockParam{{Text: extractSystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &extractionTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "submit_extraction"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
								Data: b64, MediaType: "application/pdf",
							}),
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
		},
		func(ext *cert.Extraction) string {
			return fmt.Sprintf("type=%s charge=%s mat=%s", ext.CertType, ext.Charge, ext.Material)
		},
	)
}

// Classify anropar haiku och avgör om mejlet är ett cert-mejl.
func Classify(ctx context.Context, log Logger, client *anthropic.Client, c *eml.Content) (*cert.Classification, error) {
	var attNames []string
	for _, a := range c.Attachments {
		attNames = append(attNames, a.Filename)
	}
	body := c.Body
	if len(body) > eml.MaxBodyBytes {
		body = body[:eml.MaxBodyBytes] + "\n[trunkerad]"
	}
	userText := fmt.Sprintf("Subject: %s\nFrom: %s\nDate: %s\nBilagor: %s\n\nBody:\n%s",
		c.Subject, c.From, c.Date, strings.Join(attNames, ", "), body)
	return logAICall(log, "haiku Classify",
		func() (*cert.Classification, anthropic.Usage, error) {
			return callTool[cert.Classification](ctx, client, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: classifySystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &classifyTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "classify_email"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
		},
		func(cls *cert.Classification) string {
			return fmt.Sprintf("cert=%t conf=%s — %s", cls.IsCertMail, cls.Confidence, cls.Reason)
		},
	)
}

// Mail-kategorier för ClassifyMailCategory (steg 0 i worker-pipelinen).
const (
	CategoryCertificate       = "certificate"
	CategoryDeliveryNote      = "delivery_note"
	CategoryInvoice           = "invoice"
	CategoryOrderConfirmation = "order_confirmation"
	CategoryTechnicalDoc      = "technical_doc"
	CategoryReklam            = "reklam"
	CategoryOther             = "other"
)

// MailClassification är resultatet av kategori-klassificeringen (steg 0):
// en grov kategori för ALL inkorgspost, inte bara cert.
type MailClassification struct {
	Category   string `json:"category"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
}

// ClassifyMailCategory kategoriserar ett mejl (steg 0) med haiku, baserat på
// text + bilagenamn. Returnerar en av Category*-konstanterna. Till skillnad
// från Classify (binär is_cert_mail) täcker den ALL inkorgspost så att icke-cert
// (faktura, följesedel, orderbekräftelse, …) kan sparas och reklam filtreras bort.
func ClassifyMailCategory(ctx context.Context, log Logger, client *anthropic.Client, c *eml.Content) (*MailClassification, error) {
	var attNames []string
	for _, a := range c.Attachments {
		attNames = append(attNames, a.Filename)
	}
	body := c.Body
	if len(body) > eml.MaxBodyBytes {
		body = body[:eml.MaxBodyBytes] + "\n[trunkerad]"
	}
	userText := fmt.Sprintf("Subject: %s\nFrom: %s\nDate: %s\nBilagor: %s\n\nBody:\n%s",
		c.Subject, c.From, c.Date, strings.Join(attNames, ", "), body)
	return logAICall(log, "haiku ClassifyMailCategory",
		func() (*MailClassification, anthropic.Usage, error) {
			return callTool[MailClassification](ctx, client, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: classifyCategorySystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &classifyCategoryTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "classify_mail_category"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
		},
		func(mc *MailClassification) string {
			return fmt.Sprintf("category=%s conf=%s — %s", mc.Category, mc.Confidence, mc.Reason)
		},
	)
}

// DeliveryNoteExtraction är fälten vision läser ur en följesedel-bild (Fas 4).
type DeliveryNoteExtraction struct {
	Supplier           string   `json:"supplier"`
	DeliveryDate       string   `json:"delivery_date"`
	OrderNumber        string   `json:"order_number"`
	BNumbers           []string `json:"b_numbers"`
	Charge             string   `json:"charge"`
	Material           string   `json:"material"`
	Quantity           float64  `json:"quantity"`
	Unit               string   `json:"unit"`
	DeliveryNoteNumber string   `json:"delivery_note_number"`
	Confidence         string   `json:"confidence"`
}

// ExtractFromImage läser en följesedel-BILD (image/png eller image/jpeg) med
// sonnet-vision och returnerar extraherade fält. Bara bild-media types stöds.
func ExtractFromImage(ctx context.Context, log Logger, client *anthropic.Client, img []byte, mediaType string) (*DeliveryNoteExtraction, error) {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, fmt.Errorf("media type stöds ej för vision: %q (förväntade image/png eller image/jpeg)", mediaType)
	}
	b64 := base64.StdEncoding.EncodeToString(img)
	return logAICall(log, "sonnet ExtractFromImage",
		func() (*DeliveryNoteExtraction, anthropic.Usage, error) {
			return callTool[DeliveryNoteExtraction](ctx, client, anthropic.MessageNewParams{
				Model:     ModelExtract,
				MaxTokens: 1024,
				Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
				System:    []anthropic.TextBlockParam{{Text: deliveryNoteSystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &deliveryNoteTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "extract_delivery_note"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							anthropic.NewImageBlockBase64(mediaType, b64),
							{OfText: &anthropic.TextBlockParam{Text: "Läs av denna följesedel och returnera fälten via verktyget."}},
						},
					},
				},
			})
		},
		func(dn *DeliveryNoteExtraction) string {
			return fmt.Sprintf("leverantör=%s order=%s charge=%s antal=%g %s", dn.Supplier, dn.OrderNumber, dn.Charge, dn.Quantity, dn.Unit)
		},
	)
}

// UpcomingClassifyInput är vad materialdomen får för en kommande inleveransrad.
// CertMaterial/CertType/CertDimensions kommer från det REDAN extraherade certet
// (vid intag) — extrahera inte om från PDF:en.
type UpcomingClassifyInput struct {
	PartNumber       string // artikelnummer (kontext)
	Description      string // Part.Description
	ExtraDescription string // Part.ExtraDescription (bär ofta stålsort + cert-krav)
	CertRequired     bool   // härlett ur artikeln (ReceivingInspectionType/TraceabilityMode)
	CertMaterial     string // matchat certs lagrade Material (vårt material)
	CertType         string // matchat certs CertType (t.ex. "3.1")
	CertDimensions   string // matchat certs Dimensions
	CertProductForm  string // matchat certs ProductForm (t.ex. "rundstång")

	// Parsade krav (Task 8) — den BESTÄLLDA sanningen. ADDITIVA (Task 9): V2
	// fyller dem från row.Req; V1 lämnar dem tomma och prompten ignorerar dem
	// då. När de är ifyllda ekar AI:n dem i required_*-fälten i stället för att
	// omtolka fritexten.
	ReqMaterial    string
	ReqEnNorm      string
	ReqCertType    string
	ReqProductForm string
	ReqDimensions  string
	ReqImpact      string

	// Certets nya strukturerade kolumner (Task 1-3). Också additiva: V1 lämnar
	// dem tomma/noll. Ren kontext åt materialdomen (t.ex. leveranstillstånd).
	CertNormSystem        string
	CertNormEdition       string
	CertImpactTempC       *float64
	CertImpactEnergyJ     float64
	CertIsEnglish         bool
	CertDeliveryCondition string
}

// UpcomingClassification är AI-domen för en rad. material_ok/product_form_ok ∈ {ok,mismatch,unknown}.
type UpcomingClassification struct {
	RequiredMaterial    string `json:"required_material"`
	RequiredCert        string `json:"required_cert"`
	OurMaterial         string `json:"our_material"`
	MaterialOK          string `json:"material_ok"`
	RequiredProductForm string `json:"required_product_form"`
	ProductFormOK       string `json:"product_form_ok"`
	Notes               string `json:"notes"`
}

// ClassifyUpcoming låter ren AI döma om certets material matchar det beställda
// (Robs beslut: AI dömer, men evidensen lagras separat så domen är granskbar).
// Sonnet, thinking avstängt för stabila/snabba svar. Anropas bara när ett cert har matchats —
// underlaget bygger på den lagrade cert.Material, inte en ny PDF-extraktion.
func ClassifyUpcoming(ctx context.Context, log Logger, client *anthropic.Client, in UpcomingClassifyInput) (*UpcomingClassification, error) {
	userText := buildUpcomingUserText(in)

	return logAICall(log, "sonnet ClassifyUpcoming("+in.PartNumber+")",
		func() (*UpcomingClassification, anthropic.Usage, error) {
			return callTool[UpcomingClassification](ctx, client, anthropic.MessageNewParams{
				Model:     ModelExtract, // sonnet
				MaxTokens: 512,
				Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
				System:    []anthropic.TextBlockParam{{Text: upcomingSystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &upcomingTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "judge_material"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
		},
		func(uc *UpcomingClassification) string {
			return fmt.Sprintf("krav=%s vårt=%s → %s", uc.RequiredMaterial, uc.OurMaterial, uc.MaterialOK)
		},
	)
}

// buildUpcomingUserText bygger user-meddelandet. ARTIKEL + CERT VI HAR är
// oförändrade (V1-kompatibelt). Avsnitten PARSADE KRAV och CERTETS KOLUMNER
// tas bara med när minst ett av de additiva fälten är ifyllt — V1 skickar dem
// tomma och får då exakt det gamla meddelandet.
func buildUpcomingUserText(in UpcomingClassifyInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, `Bedöm om materialet vi har cert för matchar det som är beställt för artikeln.

ARTIKEL
- Artikelnummer: %s
- Beskrivning: %s
- Extra beskrivning (bär ofta stålsort + ev. cert-krav): %s
- Kräver materialcert enligt artikelinställning: %t`,
		dash(in.PartNumber), dash(in.Description), dash(in.ExtraDescription), in.CertRequired)

	// PARSADE KRAV: den beställda sanningen (Task 8), om ifylld.
	reqLines := labeledLines([][2]string{
		{"Material", in.ReqMaterial},
		{"EN-norm", in.ReqEnNorm},
		{"Cert-typ", in.ReqCertType},
		{"Produktform", in.ReqProductForm},
		{"Dimension", in.ReqDimensions},
		{"Slagseghet", in.ReqImpact},
	})
	if reqLines != "" {
		b.WriteString("\n\nPARSADE KRAV (beställd sanning — eka dem i required_*-fälten, omtolka inte)")
		b.WriteString(reqLines)
	}

	fmt.Fprintf(&b, `

CERT VI HAR (redan extraherat vid intag — extrahera inte om)
- Material: %s
- Cert-typ: %s
- Dimension: %s
- Produktform: %s`,
		dash(in.CertMaterial), dash(in.CertType), dash(in.CertDimensions), dash(in.CertProductForm))

	// CERTETS KOLUMNER: nya strukturerade fält (Task 1-3), om ifyllda.
	certExtra := labeledLines([][2]string{
		{"Normsystem", in.CertNormSystem},
		{"Normutgåva", in.CertNormEdition},
		{"Leveranstillstånd", in.CertDeliveryCondition},
	})
	if certExtra != "" {
		b.WriteString("\n\nCERTETS KOLUMNER (certets sanning)")
		b.WriteString(certExtra)
	}

	return b.String()
}

// labeledLines returnerar "\n- Etikett: värde" för varje icke-tomt värde, eller
// "" om alla är tomma (så anropssidan kan hoppa hela rubriken).
func labeledLines(pairs [][2]string) string {
	var b strings.Builder
	for _, p := range pairs {
		if strings.TrimSpace(p[1]) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- %s: %s", p[0], p[1])
	}
	return b.String()
}

// dash returnerar "—" för tom sträng (gör prompten läsbar utan tomma rader).
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// Verify anropar haiku på alla PDF-bilagor och avgör om någon är ett 3.1-cert.
func Verify(ctx context.Context, log Logger, client *anthropic.Client, c *eml.Content) (*cert.Verification, error) {
	content := []anthropic.ContentBlockParamUnion{}
	for _, att := range c.Attachments {
		b64 := base64.StdEncoding.EncodeToString(att.Data)
		content = append(content, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
			Data: b64, MediaType: "application/pdf",
		}))
	}
	content = append(content, anthropic.ContentBlockParamUnion{
		OfText: &anthropic.TextBlockParam{Text: "Är någon av dessa PDF:er ett 3.1-stålcert? Returnera via verktyget."},
	})
	return logAICall(log, fmt.Sprintf("haiku Verify(%d pdf)", len(c.Attachments)),
		func() (*cert.Verification, anthropic.Usage, error) {
			return callTool[cert.Verification](ctx, client, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: verifySystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &verifyTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "verify_pdfs"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role:    anthropic.MessageParamRoleUser,
						Content: content,
					},
				},
			})
		},
		func(ver *cert.Verification) string {
			return fmt.Sprintf("any_cert=%t — %s", ver.AnyIsCert, ver.Reason)
		},
	)
}
