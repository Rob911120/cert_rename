package ai

import "github.com/anthropics/anthropic-sdk-go"

const extractSystemPrompt = `Du extraherar fält från ståls inspektionscertifikat (EN 10204).
Returnera ALLTID via verktyget submit_extraction.
- is_en10204_3_1: true om dokumentet är ett 3.1-certifikat (text "EN 10204:2004/3.1" eller motsv.)
- cert_type: "3.1", "2.2", "3.2" eller "unknown"
- charge: heat-/slab-nummer från tabellen. Om certifikatet listar flera, välj den som matchar bilagans filnamn (t.ex. filnamn "S355-20-68667E3" → charge "68667E3").
- material: full ståldesignation, t.ex. "S355J2+N"
- material_short: kort form för filnamn, t.ex. "S355"
- product_form: produktens form (lowercase svenska), t.ex. "rundstång", "fyrkantsstång", "plattjärn", "plåt", "fyrkantsrör", "rundrör", "vinkel", "balk". Använd "okänt" om det inte framgår.
- dimensions: produktens dimensioner från certifikatets aktuella rad, som sträng.
  Format: "<grovlek>" för platta produkter (t.ex. "16" för 16 mm plattjärn),
  "<ytterdiameter>x<vägg>" för rör (t.ex. "20x2"),
  "<sida>x<sida>x<vägg>" för fyrkantsrör/profiler (t.ex. "30x30x3").
  Använd gement "x" som separator, inga mellanslag, decimaler med punkt.
- confidence: "high"/"medium"/"low"
- issues: lista över varningar/oklarheter
Svara på svenska i issues-fältet.`

const classifySystemPrompt = `Avgör om mejlet sannolikt levererar ett EN 10204 3.1-stålinspektionscertifikat.
Returnera ALLTID via verktyget classify_email.
- is_cert_mail: true om mejlet sannolikt är ett cert-mejl
- confidence: "high"/"medium"/"low"
- reason: kort motivering på svenska
Var generös — säg ja vid tveksamhet. Säg nej bara när det är uppenbart att inget är ett cert.`

const verifySystemPrompt = `Du får 1+ PDF-bilagor från ett mejl. Avgör om NÅGON är ett EN 10204 3.1 MATERIAL-certifikat.

Ett 3.1-cert intygar specifikt material som har levererats. Det innehåller minst:
- Charge-/heat-/slabnummer
- Material-designation (t.ex. S355J2+N, S690QL, P355NL2)
- Tabell med kemiska och/eller mekaniska egenskaper för den specifika chargen

Följande räknas INTE som 3.1-cert, även om de avser stål:
- Allmänna kontroll-/inspektionsrapporter ("Kontrollrapport", "Inspection report")
- NDT/VT/MT/PT/UT/RT-rapporter (oförstörande provning)
- Viktrapporter, weight compensation-rapporter
- Ritningar, datablad, certifikatssammanfattningar utan chargedata
- Kvalitetsplaner, leveransspecifikationer

Returnera ALLTID via verktyget verify_pdfs.
- any_is_cert: true om minst en PDF uppfyller 3.1-kriterierna ovan
- reason: kort motivering på svenska, namnge gärna filen om en specifik bilaga är (eller inte är) ett cert

Vid genuin tveksamhet om en PDF *kan* vara ett 3.1-cert (t.ex. del av cert, dålig OCR), säg ja.`

var extractionTool = anthropic.ToolParam{
	Name:        "submit_extraction",
	Description: anthropic.String("Lämna extraherade fält från certifikatet."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"is_en10204_3_1": map[string]any{"type": "boolean"},
			"cert_type":      map[string]any{"type": "string"},
			"charge":         map[string]any{"type": "string"},
			"material":       map[string]any{"type": "string"},
			"material_short": map[string]any{"type": "string"},
			"product_form":   map[string]any{"type": "string"},
			"dimensions":    map[string]any{"type": "string"},
			"confidence":     map[string]any{"type": "string"},
			"issues":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"is_en10204_3_1", "cert_type", "charge", "material", "material_short", "product_form", "dimensions", "confidence", "issues"},
	},
}

var classifyTool = anthropic.ToolParam{
	Name:        "classify_email",
	Description: anthropic.String("Klassificera om mejlet är ett 3.1-stålcert-mejl."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"is_cert_mail": map[string]any{"type": "boolean"},
			"confidence":   map[string]any{"type": "string"},
			"reason":       map[string]any{"type": "string"},
		},
		Required: []string{"is_cert_mail", "confidence", "reason"},
	},
}

var verifyTool = anthropic.ToolParam{
	Name:        "verify_pdfs",
	Description: anthropic.String("Avgör om någon av bifogade PDF:er är ett 3.1-stålcert."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"any_is_cert": map[string]any{"type": "boolean"},
			"reason":      map[string]any{"type": "string"},
		},
		Required: []string{"any_is_cert", "reason"},
	},
}
