package monitor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Typerna speglar Monitor G5 001.1-API:ets entiteter. Fält-casing matchar API:t
// (PascalCase) — verifierat mot dokumentationscrawlen i ~/dev/monitor_api_docs_v2.
// Bara de fält cert-renamer behöver är med; OData $select kan trimma svaren.

// ID är ett Monitor-entitets-ID. Monitor G5 serialiserar 64-bitars-ID:n som
// JSON-strängar ("123456789012345678") för att inte tappa precision i
// JavaScript-klienter, men kan även skicka dem som bara tal (123). Avkodningen
// tål båda formerna (samt null/"" → 0) så ett strängat ID inte kraschar hela
// svaret. Marshalas som tal.
type ID int64

// UnmarshalJSON accepterar både "123" (sträng) och 123 (tal) samt null/"".
func (id *ID) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*id = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("ogiltigt Monitor-ID %q: %w", s, err)
	}
	*id = ID(n)
	return nil
}

// Comment — /api/v1/Common/Comments. Referens från flera entiteter (godsmeddelande,
// mottagningsinstruktion, extern kommentar m.fl.). Måste $expand:as från sin
// referens för att innehållet ska följa med i svaret. Vi läser RawText (texten utan
// HTML-formatering); Text-fältet (HTML) hoppas över. Doc: Common.Comment.html.
type Comment struct {
	ID      ID     `json:"Id"`
	RawText string `json:"RawText"`
}

// Alloy — /api/v1/Inventory/Alloys. Stålsort/legering knuten till en artikel via
// Part.CurrentAlloyId. Code = human-readable materialbeteckning (max 10),
// Description = översatt beskrivning. Doc: Inventory.Alloy.html.
type Alloy struct {
	ID          ID     `json:"Id"`
	Code        string `json:"Code"`
	Description string `json:"Description"`
}

// HyperLink — /api/v1/Inventory/HyperLinks. Länk knuten till en artikel
// (Part.HyperLinks, $expand). Link = URL, Description = beskrivning.
// Doc: Inventory.HyperLink.html.
type HyperLink struct {
	ID          ID     `json:"Id"`
	Link        string `json:"Link"`
	Description string `json:"Description"`
}

// Drawing — /api/v1/Manufacturing/Drawings. Ritning knuten till en artikel
// (Part.Drawings, $expand). Vi sparar bara DrawingNumber nu (numret är unikt per
// Part). Doc: Manufacturing.Drawing.html.
type Drawing struct {
	ID            ID     `json:"Id"`
	DrawingNumber string `json:"DrawingNumber"`
}

// PurchaseOrder — /api/v1/Purchase/PurchaseOrders. Leverantören länkas via
// BusinessContactId (→ Suppliers.Id). GoodsLabel (godsmärke) och
// BusinessContactOrderNumber (leverantörens ordernr) är skalära och kommer utan
// $expand; ExternalComment (extern kommentar) är en Comment-referens som MÅSTE
// expanderas (se GetPurchaseOrder). Doc: Purchase.PurchaseOrder.html.
type PurchaseOrder struct {
	ID                         ID       `json:"Id"`
	OrderNumber                string   `json:"OrderNumber"`
	OrderDate                  string   `json:"OrderDate"`
	Status                     int      `json:"Status"`
	BusinessContactId          ID       `json:"BusinessContactId"`
	GoodsLabel                 string   `json:"GoodsLabel"`                 // godsmärke (max 80)
	BusinessContactOrderNumber string   `json:"BusinessContactOrderNumber"` // leverantörens ordernr (max 30)
	ExternalComment            *Comment `json:"ExternalComment,omitempty"`  // extern kommentar (Comment via $expand)
}

// PurchaseOrderRow — /api/v1/Purchase/PurchaseOrderRows. KÄLLAN för "kommande
// inleveranser": en orderrad med RestQuantity gt 0 (ej fullt levererat) och
// DeliveryDate i fönstret = väntat gods. (PurchaseOrderDeliveryRows visade sig i
// Steg-0-dumpen vara REDAN inlevererat gods — tomt DeliveryDate, ArrivedQuantity
// alltid >0, och $expand gav ingen nästlad orderrad — och dög inte.) Länkas till
// sin order via ParentOrderId och till artikeln via PartId. DeliveryDate är
// önskat/planerat leveransdatum. OrderRowType skiljer materialrader (PartId satt)
// från externa operationsrader (legoarbete utan artikel, PartId 0 — hoppas över).
// ArrivalReporting är en Monitor-flagga vars exakta semantik är OVERIFIERAD.
// RestQuantity är kvarvarande ej levererat. Raw bär hela radens JSON för evidens.
//
// Cert-bärande radfält (doc: Purchase.PurchaseOrderRow.html): ReceivingMessage
// (godsmeddelande) och ReceivingInspectionInstruction (mottagningskontroll) är
// Comment-referenser som MÅSTE $expand:as; RowsGoodsLabel (godsmärke), RowNotes,
// SupplierDrawingNumber, SupplierRevisionNumber och FreeText (fritextradens råtext,
// OrderRowType=4 — Part:1/Additional:2/Sum:3/FreeText:4) är skalära och kommer utan
// expand.
type PurchaseOrderRow struct {
	ID                             ID              `json:"Id"`
	ParentOrderId                  ID              `json:"ParentOrderId"`
	PartId                         ID              `json:"PartId"`
	RowIndex                       int             `json:"RowIndex"`
	OrderRowType                   int             `json:"OrderRowType"`
	DeliveryDate                   string          `json:"DeliveryDate"`
	OrderedQuantity                float64         `json:"OrderedQuantity"`
	DeliveredQuantity              float64         `json:"DeliveredQuantity"`
	RestQuantity                   float64         `json:"RestQuantity"`
	UnitId                         ID              `json:"UnitId"`
	ArrivalReporting               bool            `json:"ArrivalReporting"`
	RowStatus                      int             `json:"RowStatus"`
	RowsGoodsLabel                 string          `json:"RowsGoodsLabel"`        // godsmärke (max 80)
	RowNotes                       string          `json:"RowNotes"`              // radnotering (max 100)
	SupplierDrawingNumber          string          `json:"SupplierDrawingNumber"` // leverantörens ritningsnr
	SupplierRevisionNumber         string          `json:"SupplierRevisionNumber"`
	FreeText                       string          `json:"FreeText"`                                 // fritextradens råtext (OrderRowType=4)
	ReceivingMessage               *Comment        `json:"ReceivingMessage,omitempty"`               // godsmeddelande (Comment via $expand)
	ReceivingInspectionInstruction *Comment        `json:"ReceivingInspectionInstruction,omitempty"` // mottagningskontroll (Comment via $expand)
	Part                           *Part           `json:"Part,omitempty"`                           // inline via $expand=Part
	Raw                            json.RawMessage `json:"-"`                                        // hela radens JSON
}

// UnmarshalJSON avkodar de kända fälten (inkl. inline Part) och fångar samtidigt
// råbytes i Raw.
func (r *PurchaseOrderRow) UnmarshalJSON(data []byte) error {
	type alias PurchaseOrderRow
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = PurchaseOrderRow(a)
	r.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// EnumValue tål att ett Monitor-enumfält serialiseras antingen som tal (2) eller
// som namn ("VariableInspection"). Lagras normaliserat som sträng så jämförelser
// kan göras oavsett form (RequiresCert jämför mot båda formerna).
type EnumValue string

// UnmarshalJSON accepterar både 3 (tal), "VariableInspection" (sträng) och null.
func (e *EnumValue) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "null" {
		s = ""
	}
	*e = EnumValue(s)
	return nil
}

// Part — /api/v1/Inventory/Parts. Beskrivning + ExtraDescription (bär ofta stålsort
// + ev. cert-krav i fritext). ReceivingInspectionType + TraceabilityMode styr om
// artikeln sannolikt kräver materialcert (se RequiresCert). Raw bär hela artikelns
// JSON för evidens i UI:t. Doc: Inventory.Part.html (+ Inventory.Alloy/HyperLink,
// Manufacturing.Drawing, Common.Comment/ExtraField för de expanderade fälten).
//
// Cert-bärande navigeringar (alla Expandable, måste $expand:as): CurrentAlloy
// (stålsort), ReceivingInstruction/PurchaseComment/Comment (Comment-referenser),
// HyperLinks, Drawings, ExtraFields. Dimensionerna Length/Width/Height anges i
// meter och WeightPerUnit i kg (Decimal? i API:t). GoodsType (godsslag) och
// CategoryString är skalära strängar. ExtraFields sparas RÅTT (json.RawMessage) —
// varje ExtraField har många värdefält beroende på Type, så vi behåller hela
// arrayen och inventerar den mot live-data i senare task.
type Part struct {
	ID                      ID              `json:"Id"`
	PartNumber              string          `json:"PartNumber"`
	Description             string          `json:"Description"`
	ExtraDescription        string          `json:"ExtraDescription"`
	ReceivingInspectionType EnumValue       `json:"ReceivingInspectionType"` // None:0/Always:1/VariableInspection:2
	TraceabilityMode        EnumValue       `json:"TraceabilityMode"`        // None:0/Batch:1/Individual:2/IndividualOnlyWithdrawal:4
	CurrentAlloyId          ID              `json:"CurrentAlloyId"`
	CurrentAlloy            *Alloy          `json:"CurrentAlloy,omitempty"`         // stålsort (Alloy via $expand)
	ReceivingInstruction    *Comment        `json:"ReceivingInstruction,omitempty"` // mottagningsinstruktion (Comment via $expand)
	PurchaseComment         *Comment        `json:"PurchaseComment,omitempty"`      // inköpskommentar (Comment via $expand)
	Comment                 *Comment        `json:"Comment,omitempty"`              // artikelkommentar (Comment via $expand)
	Length                  float64         `json:"Length"`                         // fysisk längd i meter
	Width                   float64         `json:"Width"`                          // fysisk bredd i meter
	Height                  float64         `json:"Height"`                         // fysisk höjd i meter
	WeightPerUnit           float64         `json:"WeightPerUnit"`                  // vikt per enhet i kg
	GoodsType               string          `json:"GoodsType"`                      // godsslag (max 35)
	CategoryString          string          `json:"CategoryString"`                 // kategori
	HyperLinks              []HyperLink     `json:"HyperLinks,omitempty"`           // länkar (via $expand)
	Drawings                []Drawing       `json:"Drawings,omitempty"`             // ritningar (via $expand)
	ExtraFields             json.RawMessage `json:"ExtraFields,omitempty"`          // rå ExtraFields-array (via $expand)
	Raw                     json.RawMessage `json:"-"`                              // hela artikel-JSON:en
}

// UnmarshalJSON avkodar de kända fälten och fångar samtidigt råbytes i Raw.
func (p *Part) UnmarshalJSON(data []byte) error {
	type alias Part
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = Part(a)
	p.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// RequiresCert avgör (heuristiskt) om artikeln sannolikt kräver materialcert.
// Monitor G5-API:t har INGEN explicit "3.1-flagga" — kravet läses egentligen ur de
// cert-bärande texterna (ReceivingInstruction/-Message m.fl.), vilket parsas i en
// senare task. Fram tills dess härleds ett troligt krav ur de doc-verifierade
// enum-fälten (Inventory.Part.html):
//   - ReceivingInspectionType {None:0, Always:1, VariableInspection:2}: aktiv
//     mottagningskontroll (≠ None) ⇒ sannolikt cert.
//   - TraceabilityMode {None:0, Batch:1, Individual:2, IndividualOnlyWithdrawal:4}:
//     aktiv spårbarhet (≠ None) ⇒ sannolikt cert.
//
// EnumValue kan komma som namn eller tal, så vi jämför mot båda formerna av "None".
// Avsikten (försiktigt "ja" för allt utom uttrycklig None/0) är oförändrad — bara
// uttryckt i de verifierade värdena (Individual/IndividualOnlyWithdrawal räknas nu
// också som aktiv spårbarhet).
func (p *Part) RequiresCert() bool {
	if enumActive(p.ReceivingInspectionType) { // Always, VariableInspection (eller okänt framtida värde)
		return true
	}
	return enumActive(p.TraceabilityMode) // Batch, Individual, IndividualOnlyWithdrawal
}

// enumActive är true om enum-värdet är satt och inte är None/0 (oavsett om Monitor
// skickar namnet "None" eller talet 0).
func enumActive(v EnumValue) bool {
	s := normEnum(string(v))
	return s != "" && s != "none" && s != "0"
}

func normEnum(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Supplier — /api/v1/Purchase/Suppliers.
type Supplier struct {
	ID              ID     `json:"Id"`
	SupplierCode    string `json:"SupplierCode"`
	Name            string `json:"Name"`
	AlternativeName string `json:"AlternativeName"`
}

// ProductRecord — /api/v1/Inventory/ProductRecords. Bär charge/B-nr
// (ChargeNumber/SerialNumber) och länkar till en inköpsorder via PurchaseOrderId
// + artikeln via PartId. OBS: ingen direkt FK till PurchaseOrderRow — matchning
// till orderrad sker via (PurchaseOrderId, PartId).
type ProductRecord struct {
	ID              ID     `json:"Id"`
	SerialNumber    string `json:"SerialNumber"`
	ChargeNumber    string `json:"ChargeNumber"`
	PartId          ID     `json:"PartId"`
	PurchaseOrderId ID     `json:"PurchaseOrderId"`
}
