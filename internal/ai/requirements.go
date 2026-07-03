package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// RequirementsInput bär alla kravbärande texter för EN artikel/orderrad —
// insamlade av den nattliga Monitor-synken (Tasks 5-6) från order-, rad- och
// artikelnivå. Den här tasken lägger bara AI-lagret som tolkar texterna;
// sync-steget och DB-kolumnerna kommer i nästa task.
type RequirementsInput struct {
	Description              string // artikelns benämning
	ExtraDescription         string // extra beskrivning (bär ofta stålsort + certkrav)
	ReceivingMessage         string // godsmeddelandet (rad)
	RowInspectionInstruction string // mottagnings-/kontrollinstruktion (rad)
	PartReceivingInstruction string // mottagningsinstruktion (artikel)
	PartPurchaseComment      string // inköpskommentar (artikel)
	PartComment              string // artikelkommentar (artikel)
	RowGoodsLabel            string // godsmärke (rad)
	OrderGoodsLabel          string // godsmärke (order)
	RowNotes                 string
	FreeText                 string
	ExternalComment          string // ordernivå, mot leverantören
	AlloyCode                string // materialkvalitetsregistret
	AlloyDescription         string
	PartLength               float64 // meter, 0 = ej angivet
	PartWidth                float64
	PartHeight               float64
}

// ArticleRequirements är de parsade kraven — "gissa aldrig": tomma strängar
// (och requires_english=false) betyder att inget uttryckligen framgick av
// kravtexterna, inte att inget krävs.
type ArticleRequirements struct {
	RequiredMaterial    string `json:"required_material"`     // t.ex. "S355J2+N"
	RequiredEnNorm      string `json:"required_en_norm"`      // t.ex. "EN 10025-2"
	RequiredCertType    string `json:"required_cert_type"`    // t.ex. "3.1"
	RequiresEnglish     bool   `json:"requires_english"`      // "Certificate ... in English"
	RequiredProductForm string `json:"required_product_form"` // lowercase svenska, som extraktionen
	RequiredDimensions  string `json:"required_dimensions"`   // samma format som extraktionen
	RequiredImpact      string `json:"required_impact"`       // t.ex. "27J/-20°C" om angivet
	Notes               string `json:"notes"`                 // kort, svenska
}

// requirementsSystemPrompt: samma "fyll kolumnerna, gissa aldrig"-princip som
// extractV2SystemPrompt, men för beställningstexter i stället för certifikat.
const requirementsSystemPrompt = `Du läser beställningstexter för EN artikel hos en stålverkstad. Ditt jobb är att fylla kolumnerna nedan med vad ARTIKELN KRÄVER (cert och material). Lämna tomt (tom sträng, eller false för requires_english) om det inte uttryckligen framgår av texterna — gissa aldrig. Returnera ALLTID via verktyget submit_requirements.

Kolumnregler:
- required_material: den beställda ståldesignationen, t.ex. "S355J2+N". Bara materialkoden, inte EN-normen.
- required_en_norm: EN-/materialnorm om angiven, t.ex. "EN 10025-2".
- required_cert_type: certnivå om angiven: "3.1", "2.2" eller "3.2" (t.ex. "EN 10204 3.1" → "3.1").
- requires_english: true ENDAST om texten uttryckligen kräver engelska (t.ex. "Certificate EN 10204 3.1 in English"); annars false.
- required_product_form: produktform på lowercase svenska (rundstång/fyrkantsstång/plattjärn/plåt/fyrkantsrör/rundrör/vinkel/balk) om den framgår; annars tom sträng.
- required_dimensions: beställda dimensioner, samma format som certextraktionens dimensions-regel:
  "<grovlek>" för platta produkter (t.ex. "16" för 16 mm plattjärn),
  "<ytterdiameter>x<vägg>" för rör (t.ex. "20x2"),
  "<sida>x<sida>x<vägg>" för fyrkantsrör/profiler (t.ex. "30x30x3").
  Gement "x" som separator, inga mellanslag, decimaler med punkt.
- required_impact: slagprovskrav om angivet, format "27J/-20°C".
- notes: kort svensk kommentar om något är tvetydigt eller motstridigt mellan texterna; annars tom sträng.`

var requirementsTool = anthropic.ToolParam{
	Name:        "submit_requirements",
	Description: anthropic.String("Lämna parsade artikelkrav (cert/material) för en orderrad."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"required_material":     map[string]any{"type": "string"},
			"required_en_norm":      map[string]any{"type": "string"},
			"required_cert_type":    map[string]any{"type": "string"},
			"requires_english":      map[string]any{"type": "boolean"},
			"required_product_form": map[string]any{"type": "string"},
			"required_dimensions":   map[string]any{"type": "string"},
			"required_impact":       map[string]any{"type": "string"},
			"notes":                 map[string]any{"type": "string"},
		},
		Required: []string{
			"required_material", "required_en_norm", "required_cert_type", "requires_english",
			"required_product_form", "required_dimensions", "required_impact", "notes",
		},
	},
}

// buildRequirementsUserText bygger user-meddelandet: rubricerade sektioner
// för de icke-tomma fälten i inputen. Tomma fält utelämnas helt (mindre brus
// åt modellen) — måtten tas bara med om minst ett är > 0.
func buildRequirementsUserText(in RequirementsInput) string {
	var b strings.Builder
	b.WriteString("Läs kravtexterna nedan för denna artikel/orderrad och fyll i vad som krävs via verktyget submit_requirements.\n")

	section := func(heading, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n%s\n", heading, value)
	}

	section("BESKRIVNING", in.Description)
	section("EXTRA BESKRIVNING", in.ExtraDescription)
	section("GODSMEDDELANDE (RAD)", in.ReceivingMessage)
	section("KONTROLLINSTRUKTION (RAD)", in.RowInspectionInstruction)
	section("MOTTAGNINGSINSTRUKTION (ARTIKEL)", in.PartReceivingInstruction)
	section("INKÖPSKOMMENTAR (ARTIKEL)", in.PartPurchaseComment)
	section("ARTIKELKOMMENTAR (ARTIKEL)", in.PartComment)
	section("GODSMÄRKE (RAD)", in.RowGoodsLabel)
	section("GODSMÄRKE (ORDER)", in.OrderGoodsLabel)
	section("ANTECKNINGAR (RAD)", in.RowNotes)
	section("FRITEXT", in.FreeText)
	section("EXTERN KOMMENTAR (TILL LEVERANTÖREN)", in.ExternalComment)
	section("MATERIALKVALITETSREGISTER: KOD", in.AlloyCode)
	section("MATERIALKVALITETSREGISTER: BESKRIVNING", in.AlloyDescription)

	var dims []string
	if in.PartLength > 0 {
		dims = append(dims, fmt.Sprintf("längd %gm", in.PartLength))
	}
	if in.PartWidth > 0 {
		dims = append(dims, fmt.Sprintf("bredd %gm", in.PartWidth))
	}
	if in.PartHeight > 0 {
		dims = append(dims, fmt.Sprintf("höjd %gm", in.PartHeight))
	}
	if len(dims) > 0 {
		fmt.Fprintf(&b, "\nMÅTT:\n%s\n", strings.Join(dims, ", "))
	}

	return b.String()
}

// ParseRequirements anropar haiku (samma modellkonstant som Classify) och
// tolkar en artikels/orderrads kravtexter till strukturerade krav. Bara
// AI-lagret — sync mot Monitor och DB-kolumnerna kopplas in i nästa task.
func ParseRequirements(ctx context.Context, log Logger, client *anthropic.Client, in RequirementsInput) (*ArticleRequirements, error) {
	userText := buildRequirementsUserText(in)
	return logAICall(log, "haiku parse_requirements("+in.Description+")",
		func() (*ArticleRequirements, anthropic.Usage, error) {
			return callTool[ArticleRequirements](ctx, client, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 1024,
				Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
				System:    []anthropic.TextBlockParam{{Text: requirementsSystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &requirementsTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "submit_requirements"},
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
		func(ar *ArticleRequirements) string {
			return fmt.Sprintf("material=%s norm=%s cert=%s dim=%s", ar.RequiredMaterial, ar.RequiredEnNorm, ar.RequiredCertType, ar.RequiredDimensions)
		},
	)
}
