package ai

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/v2/cert"
)

// extractV2SystemPrompt är V2:s kolumnfokuserade cert-prompt. Till skillnad från
// V1:s extractSystemPrompt (som INTE får ändras) lyfter den fram målet först
// ("fyll kolumnerna, gissa aldrig"), ger en filnamns-ledtråd för flerradscert,
// har en regel per kolumn (inkl. de nya strukturerade fälten) och en bantad
// checklista sist — de generella kontroller som nu blivit egna kolumner (engelska,
// läsbarhet, maskering, ASME/ASTM) är borttagna från checklistan.
const extractV2SystemPrompt = `Du läser ett stålcertifikat (EN 10204). Ditt jobb är att fylla kolumnerna nedan. Fyll så många du kan. Lämna tomt om du inte kan svara med säkerhet — gissa aldrig. För talfält betyder utelämnat/0 "ej angivet på certet". Returnera ALLTID via verktyget submit_extraction_v2.

Filnamns-ledtråd: bilagans originalfilnamn är ofta döpt efter den avsedda chargen. Om certifikatet listar flera rader/heats/tjocklekar: välj raden som matchar bilagans filnamn (t.ex. filnamn "S355-20-68667E3" → charge "68667E3") och hämta dimension, material, kemi (C/P/S/CEV) och slagseghet (impact) från SAMMA rad.

Kolumnregler:
- is_en10204_3_1: true om dokumentet är ett 3.1-certifikat (text "EN 10204:2004/3.1" eller motsv.)
- cert_type: "3.1", "2.2", "3.2" eller "unknown"
- charge: heat-/slab-nummer från tabellen. Om certifikatet listar flera, välj den som matchar bilagans filnamn (t.ex. filnamn "S355-20-68667E3" → charge "68667E3").
- material: fullständig ståldesignation INKLUSIVE EN-standarden när den framgår av certifikatet, t.ex. "S355J2+N EN 10025-2" (inte bara "S355J2+N"). Skriv alltid hela beteckningen, inte en förkortad form.
- en_standard_present: true om certifikatet anger en fullständig EN-standardbeteckning för materialet (t.ex. "EN 10025-2", "EN 10025-3", "EN 10149-2", "EN 10088-2"). false om endast stålsorten anges utan EN-standard.
- is_english: true om engelska är ETT av språken på certifikatet (blandat engelska + annat språk är OK). false BARA om engelsk text helt saknas.
- is_legible: false bara om partier är avskurna/oläsliga; annars true.
- is_unaltered: false bara om information verkar maskerad/borttagen/redigerad; annars true.
- product_form: produktens form (lowercase svenska), t.ex. "rundstång", "fyrkantsstång", "plattjärn", "plåt", "fyrkantsrör", "rundrör", "vinkel", "balk". Använd "okänt" om det inte framgår.
- product_code: produktformens FÖRKORTNING enligt kodtabellen nedan — väljs utifrån product_form OCH den faktiska beteckningen på certifikatet. Detta är koden som används i det sparade filnamnet. Lämna tom sträng om du är osäker (gissa aldrig).
  Kodtabell:
    Stång:        4S=4-kantsstång  6S=6-kantsstång  LS=vinkelstång  PS=plattstång/plattjärn
                  RS=rundstång  TS=T-stång  US=U-stång  ZS=Z-stång
    Balk/Räls:    använd balkens EGEN beteckning som kod: HEA, HEB, IPE, UNP, UPE, USP
                  RÄ=räls
    Profiler (kallformade):  CP=C-profil  LP=L-profil  TP=T-profil  UP=U-profil
    Rör:          RR=runt rör (rundrör)  4R=4-kantsrör (fyrkantsrör)  ÄR=ämnesrör
    Plåt:         PL=plåt
    Bult/övrigt:  HGS=helgängad stång; för bult: använd bultens beteckning (m = mm gänga, t.ex. MP6SS = P6SS)
    Plast:        sätt (P) framför övrig beteckning
    Övrigt:       ÖP=övrig profil  ÖPL=övrig plåt  ÖR=övrig rör  ÖS=övrig stång
- dimensions: produktens dimensioner från certifikatets aktuella rad, som sträng.
  Format: "<grovlek>" för platta produkter (t.ex. "16" för 16 mm plattjärn),
  "<ytterdiameter>x<vägg>" för rör (t.ex. "20x2"),
  "<sida>x<sida>x<vägg>" för fyrkantsrör/profiler (t.ex. "30x30x3").
  Använd gement "x" som separator, inga mellanslag, decimaler med punkt.
- country_of_origin: ursprungsland för materialet/stålverket om det framgår av certifikatet, annars tom sträng.
- norm_system: "ASME" (beteckningar med "SA"-prefix, t.ex. SA-516-70), "ASTM" (beteckningar med "A"-prefix, t.ex. A516-70) eller "EN". Tom sträng om oklart; om blandat — välj tom sträng och lägg en post i issues.
- norm_edition: normens utgåva/edition, t.ex. "2019" eller "SEC.II PART A 2015". Tom sträng om ej angiven.
- ped_directive: PED-märkning, t.ex. "2014/68/EU". Tom sträng om saknas.
- impact_temp_c: provtemperatur för slagprov (Charpy) i °C, t.ex. -20. Utelämna om ej provat.
- impact_energy_j: slagenergi i J, t.ex. 27. 0 eller utelämnad om ej provat.
- cev: kolekvivalent (CEV/CE), t.ex. 0.45. Utelämna om ej angiven.
- carbon_pct: kolhalt (C) i % från kemianalysen — från SAMMA rad/charge som valts via filnamns-ledtråden. Utelämna om ej angiven.
- p_pct: fosforhalt (P) i % från kemianalysen — från SAMMA rad/charge. Utelämna om ej angiven.
- s_pct: svavelhalt (S) i % från kemianalysen — från SAMMA rad/charge. Utelämna om ej angiven.
- has_bend_test: true om bocktest redovisas på certifikatet.
- has_intergranular_test: true om intergranulärtest (t.ex. ISO 3651-2) redovisas.
- has_stamp_photo: true om foto på originalstämpel medföljer.
- min_temperature_c: minsta tillåtna användningstemperatur i °C om certifikatet anger en (förekommer på lyftartiklar). Utelämna annars.
- delivery_condition: leveranstillstånd/temper, t.ex. "T6", "+N", "QT". Tom sträng om ej angiven.
- confidence: "high"/"medium"/"low"
- issues: lista över varningar/oklarheter, på svenska. Kontrollera ALLTID certifikatet mot checklistan nedan och lägg till en post i issues för varje avvikelse du hittar (en kort, konkret formulering, t.ex. "OBS: SIEMENS-leverans med MCD-norm - ej tillåtet enligt regel"):

Generell kontroll:
- Att "EN 10204 3.1" (eller motsvarande) faktiskt står med på certifikatet.
- Att chargenumret finns med.

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

// extractionToolV2 är V2:s tool-schema: kopia av extractionTool:s properties plus
// de nya strukturerade fälten. Talfälten (impact_temp_c, impact_energy_j, cev,
// carbon_pct, p_pct, s_pct, min_temperature_c) är avsiktligt EJ required — modellen
// ska kunna utelämna dem när de inte framgår (nil/0 = "ej angivet").
var extractionToolV2 = anthropic.ToolParam{
	Name:        "submit_extraction_v2",
	Description: anthropic.String("Lämna extraherade fält från certifikatet (V2, kolumnfokuserad)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"is_en10204_3_1":         map[string]any{"type": "boolean"},
			"cert_type":              map[string]any{"type": "string"},
			"charge":                 map[string]any{"type": "string"},
			"material":               map[string]any{"type": "string"},
			"en_standard_present":    map[string]any{"type": "boolean"},
			"is_english":             map[string]any{"type": "boolean"},
			"is_legible":             map[string]any{"type": "boolean"},
			"is_unaltered":           map[string]any{"type": "boolean"},
			"product_form":           map[string]any{"type": "string"},
			"product_code":           map[string]any{"type": "string"},
			"dimensions":             map[string]any{"type": "string"},
			"country_of_origin":      map[string]any{"type": "string"},
			"norm_system":            map[string]any{"type": "string"},
			"norm_edition":           map[string]any{"type": "string"},
			"ped_directive":          map[string]any{"type": "string"},
			"impact_temp_c":          map[string]any{"type": "number"},
			"impact_energy_j":        map[string]any{"type": "number"},
			"cev":                    map[string]any{"type": "number"},
			"carbon_pct":             map[string]any{"type": "number"},
			"p_pct":                  map[string]any{"type": "number"},
			"s_pct":                  map[string]any{"type": "number"},
			"has_bend_test":          map[string]any{"type": "boolean"},
			"has_intergranular_test": map[string]any{"type": "boolean"},
			"has_stamp_photo":        map[string]any{"type": "boolean"},
			"min_temperature_c":      map[string]any{"type": "number"},
			"delivery_condition":     map[string]any{"type": "string"},
			"confidence":             map[string]any{"type": "string"},
			"issues":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{
			// V1:s befintliga required-lista
			"is_en10204_3_1", "cert_type", "charge", "material", "en_standard_present",
			"is_english", "product_form", "product_code", "dimensions", "country_of_origin", "confidence", "issues",
			// nya boolean- och strängfält (talfälten är avsiktligt utelämnade)
			"is_legible", "is_unaltered", "has_bend_test", "has_intergranular_test", "has_stamp_photo",
			"norm_system", "norm_edition", "ped_directive", "delivery_condition",
		},
	},
}

// ExtractV2 är V2:s cert-extraktion: samma signatur och anropsmönster som Extract
// (internal/ai/tasks.go), men med den kolumnfokuserade prompten/verktyget och fler
// tokens (2048) för de extra fälten. Wiring till V2-intaget sker i en senare task.
func ExtractV2(ctx context.Context, log Logger, client *anthropic.Client, pdf []byte, subject, body, filename string) (*cert.Extraction, error) {
	b64 := base64.StdEncoding.EncodeToString(pdf)
	userText := fmt.Sprintf("Extrahera fält från detta certifikat.\n\nMejlets ämnesrad: %s\nBilagans originalfilnamn: %s\n\nMejlets brödtext:\n%s\n",
		subject, filename, body)
	return logAICall(log, "sonnet extract_v2("+filename+")",
		func() (*cert.Extraction, anthropic.Usage, error) {
			return callTool[cert.Extraction](ctx, client, anthropic.MessageNewParams{
				Model:     ModelExtract,
				MaxTokens: 2048,
				Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
				System:    []anthropic.TextBlockParam{{Text: extractV2SystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &extractionToolV2}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "submit_extraction_v2"},
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
