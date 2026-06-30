package ai

import "github.com/anthropics/anthropic-sdk-go"

const extractSystemPrompt = `Du extraherar fält från ståls inspektionscertifikat (EN 10204).
Returnera ALLTID via verktyget submit_extraction.
- is_en10204_3_1: true om dokumentet är ett 3.1-certifikat (text "EN 10204:2004/3.1" eller motsv.)
- cert_type: "3.1", "2.2", "3.2" eller "unknown"
- charge: heat-/slab-nummer från tabellen. Om certifikatet listar flera, välj den som matchar bilagans filnamn (t.ex. filnamn "S355-20-68667E3" → charge "68667E3").
- material: fullständig ståldesignation INKLUSIVE EN-standarden när den framgår av certifikatet, t.ex. "S355J2+N EN 10025-2" (inte bara "S355J2+N"). Skriv alltid hela beteckningen, inte en förkortad form.
- en_standard_present: true om certifikatet anger en fullständig EN-standardbeteckning för materialet (t.ex. "EN 10025-2", "EN 10025-3", "EN 10149-2", "EN 10088-2"). false om endast stålsorten anges utan EN-standard.
- product_form: produktens form (lowercase svenska), t.ex. "rundstång", "fyrkantsstång", "plattjärn", "plåt", "fyrkantsrör", "rundrör", "vinkel", "balk". Använd "okänt" om det inte framgår.
- dimensions: produktens dimensioner från certifikatets aktuella rad, som sträng.
  Format: "<grovlek>" för platta produkter (t.ex. "16" för 16 mm plattjärn),
  "<ytterdiameter>x<vägg>" för rör (t.ex. "20x2"),
  "<sida>x<sida>x<vägg>" för fyrkantsrör/profiler (t.ex. "30x30x3").
  Använd gement "x" som separator, inga mellanslag, decimaler med punkt.
- country_of_origin: ursprungsland för materialet/stålverket om det framgår av certifikatet, annars tom sträng.
- confidence: "high"/"medium"/"low"
- issues: lista över varningar/oklarheter, på svenska. Kontrollera ALLTID certifikatet mot checklistan nedan och lägg till en post i issues för varje avvikelse du hittar (en kort, konkret formulering, t.ex. "OBS: SIEMENS-leverans med MCD-norm - ej tillåtet enligt regel"):

Generell kontroll:
- Att "EN 10204 3.1" (eller motsvarande) faktiskt står med på certifikatet.
- Att chargenumret finns med.
- Att all information är på engelska (flagga om delar är på ett annat språk).
- Att all information är läsbar (inga avskurna/oläsliga partier).
- Att ingen information verkar vara borttagen/maskerad/redigerad.
- Skilj på ASME (beteckningar med "SA"-prefix, t.ex. SA-516-70) och ASTM (beteckningar med "A"-prefix, t.ex. A516-70) — flagga om det är oklart eller blandat.

Materialspecifika regler:
- S355J2+N: ska vara slagseghetstestad vid -20°C, 27J, utskrivet för gods över 5,9 mm tjocklek.
- S355MCD / Alform (MC-normer): ska vara testad -20°C, 40J, för gods över 5,9 mm tjocklek.
- Lågtemperaturstål: EN 10025-3 S355NL ska ha 27J/-50°C, EN 10025-4 S355ML ska ha 27J/-50°C, EN 10149-2 S355MCE ska ha 27J/-40°C — flagga avvikelser.

Kundspecifika regler (avgör relevans utifrån kund-/mottagarnamn i mejlets ämne/avsändare/brödtext eller på certifikatet självt):
- SIEMENS: MC/MCD/Alform-normer är EJ tillåtna om dimensionen är 3 mm eller större (för dimension under 3 mm finns inget alternativ och det är ok). Var extra noga med materialstandarder av typen "MATxxxx56" — dessa har ofta särskilda slagseghetskrav.
- Alfa Laval: kontrollera att rätt norm används - ASME ska vara SA-typ med rätt edition (2019, äldre 2015 kan godkännas om P och S är max 0,02%), EN ska vara enligt AD2000-W1. SA-516-70 ska ha draghållfasthet Rp0,2 angiven.
- Getinge: materialet ska följa rätt norm. ASME-certifikat ska vara enligt SEC.II PART A med revision från 2015 eller senare. Svart material ska vara bocktestat (SA20). Interkristallintest ska vara enligt ISO 3651-2. Foto på originalstämpel ska finnas med.
- NOV: CEV bör vara max 0,45 om angivet. Charpy-V ska vara testad enligt Form V, 27J vid -20°C (för axlar 42J vid -20°C).
- Rosemount: certifikat utskrivna efter 2016-07-20 ska ange "PED 2014/68/EU". På material A182 ska kol-halten (C) vara max 0,23%.
- SAAB Dynamics: på aluminium måste rätt leveranstillstånd-notation anges, t.ex. "6082-T6" — inte felaktig ordning som "T6082-T651".
- SAAB Kockums: EN-normen ska stå på materialet. Flänsar till SAAB levereras ofta med 1.4432/1.4436 (inte 1.4435) - det är normalt och ska inte flaggas som fel.
- Tomal: material ska vara CE-märkt med DoP (Declaration of Performance) enligt EN 1090-1.

Ursprungsland: certifikat med ursprungsland Ryssland eller Belarus är INTE tillåtna - flagga alltid tydligt om country_of_origin pekar på något av dessa länder.`

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
			"is_en10204_3_1":      map[string]any{"type": "boolean"},
			"cert_type":           map[string]any{"type": "string"},
			"charge":              map[string]any{"type": "string"},
			"material":            map[string]any{"type": "string"},
			"en_standard_present": map[string]any{"type": "boolean"},
			"product_form":        map[string]any{"type": "string"},
			"dimensions":          map[string]any{"type": "string"},
			"country_of_origin":   map[string]any{"type": "string"},
			"confidence":          map[string]any{"type": "string"},
			"issues":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"is_en10204_3_1", "cert_type", "charge", "material", "en_standard_present", "product_form", "dimensions", "country_of_origin", "confidence", "issues"},
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
