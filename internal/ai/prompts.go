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

const classifyCategorySystemPrompt = `Du kategoriserar inkommande mejl till en stål-/metallinköpares inkorg.
Returnera ALLTID via verktyget classify_mail_category med exakt EN kategori:
- certificate: mejlet levererar ett material-/inspektionscertifikat (EN 10204 2.2/3.1/3.2), kontrollrapport eller OFP/NDT-rapport.
- delivery_note: följesedel/packsedel (utan att i sig vara ett cert).
- invoice: faktura.
- order_confirmation: orderbekräftelse/orderbesked.
- technical_doc: ritning, datablad, kvalitetsplan eller specifikation.
- reklam: marknadsföring, nyhetsbrev eller utskick utan konkret affärsärende.
- other: passar ingen av kategorierna ovan.

Vid genuin tveksamhet mellan certificate och något annat: välj certificate — det är
kärnflödet och verifieras separat i nästa steg.
- category: exakt en av strängarna ovan (gemener).
- confidence: "high"/"medium"/"low".
- reason: kort motivering på svenska.`

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

const deliveryNoteSystemPrompt = `Du läser av en FÖLJESEDEL (packsedel) från en stål-/metalleverantör utifrån en bild.
Returnera ALLTID via verktyget extract_delivery_note.
- supplier: leverantörens namn.
- delivery_date: leveransdatum (ISO YYYY-MM-DD om möjligt, annars som det står).
- order_number: kundens inköpsordernummer (ofta "B" + siffror, t.ex. B127196).
- b_numbers: lista över B-nummer/ordernummer som nämns.
- charge: charge-/heat-/slabnummer om det står på följesedeln.
- material: materialdesignation (t.ex. S355J2).
- quantity: levererad mängd som tal (utan enhet).
- unit: enhet för mängden (t.ex. "st", "m", "kg").
- delivery_note_number: följesedelns eget nummer.
- confidence: "high"/"medium"/"low".
Lämna fält tomma (eller 0 för quantity) om de inte framgår. Svara inte med text utanför verktyget.`

const upcomingSystemPrompt = `Du är materialgranskare på en stålverkstad. Du får en BESTÄLLD artikel (med beskrivning och en extra beskrivning som ofta innehåller stålsort och cert-krav) och det MATERIALCERT vi redan har matchat mot ordern. Avgör om certets material stämmer med det beställda.

Returnera ALLTID via verktyget judge_material:
- required_material: den ståldesignation som är BESTÄLLD enligt artikelns beskrivningar (t.ex. "S355J2"). Tom om det inte framgår.
- required_cert: vilken certnivå som krävs om det framgår (t.ex. "3.1"). Tom annars.
- our_material: materialet enligt certet vi har (normalisera från det givna cert-materialet).
- material_ok: "ok" om certets material uppfyller det beställda, "mismatch" om de tydligt skiljer sig (annan stålsort/-klass, t.ex. S275 mot S355), "unknown" om underlaget inte räcker för en säker dom.
- notes: kort motivering på svenska — peka på det som avgjorde domen.

Var konservativ: hellre "unknown" än en gissad "ok". Likvärdiga beteckningar (S355J2 vs S355J2+N) kan vara ok; faktiska avvikelser i stålsort/-klass är mismatch.`

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

var deliveryNoteTool = anthropic.ToolParam{
	Name:        "extract_delivery_note",
	Description: anthropic.String("Lämna fält avlästa från en följesedel-bild."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"supplier":             map[string]any{"type": "string"},
			"delivery_date":        map[string]any{"type": "string"},
			"order_number":         map[string]any{"type": "string"},
			"b_numbers":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"charge":               map[string]any{"type": "string"},
			"material":             map[string]any{"type": "string"},
			"quantity":             map[string]any{"type": "number"},
			"unit":                 map[string]any{"type": "string"},
			"delivery_note_number": map[string]any{"type": "string"},
			"confidence":           map[string]any{"type": "string"},
		},
		Required: []string{"supplier", "delivery_date", "order_number", "b_numbers", "charge", "material", "quantity", "unit", "delivery_note_number", "confidence"},
	},
}

var classifyCategoryTool = anthropic.ToolParam{
	Name:        "classify_mail_category",
	Description: anthropic.String("Kategorisera ett inkommande mejl i exakt en kategori."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"category": map[string]any{
				"type": "string",
				"enum": []string{"certificate", "delivery_note", "invoice", "order_confirmation", "technical_doc", "reklam", "other"},
			},
			"confidence": map[string]any{"type": "string"},
			"reason":     map[string]any{"type": "string"},
		},
		Required: []string{"category", "confidence", "reason"},
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

var upcomingTool = anthropic.ToolParam{
	Name:        "judge_material",
	Description: anthropic.String("Lämna materialdomen för en kommande inleverans (beställt vs cert)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"required_material": map[string]any{"type": "string"},
			"required_cert":     map[string]any{"type": "string"},
			"our_material":      map[string]any{"type": "string"},
			"material_ok":       map[string]any{"type": "string", "enum": []string{"ok", "mismatch", "unknown"}},
			"notes":             map[string]any{"type": "string"},
		},
		Required: []string{"required_material", "required_cert", "our_material", "material_ok", "notes"},
	},
}
