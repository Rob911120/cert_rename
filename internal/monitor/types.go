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

// PurchaseOrder — /api/v1/Purchase/PurchaseOrders. Leverantören länkas via
// BusinessContactId (→ Suppliers.Id).
type PurchaseOrder struct {
	ID                ID     `json:"Id"`
	OrderNumber       string `json:"OrderNumber"`
	OrderDate         string `json:"OrderDate"`
	Status            int    `json:"Status"`
	BusinessContactId ID     `json:"BusinessContactId"`
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
type PurchaseOrderRow struct {
	ID                ID              `json:"Id"`
	ParentOrderId     ID              `json:"ParentOrderId"`
	PartId            ID              `json:"PartId"`
	RowIndex          int             `json:"RowIndex"`
	OrderRowType      int             `json:"OrderRowType"`
	DeliveryDate      string          `json:"DeliveryDate"`
	OrderedQuantity   float64         `json:"OrderedQuantity"`
	DeliveredQuantity float64         `json:"DeliveredQuantity"`
	RestQuantity      float64         `json:"RestQuantity"`
	UnitId            ID              `json:"UnitId"`
	ArrivalReporting  bool            `json:"ArrivalReporting"`
	RowStatus         int             `json:"RowStatus"`
	Part              *Part           `json:"Part,omitempty"` // inline via $expand=Part
	Raw               json.RawMessage `json:"-"`              // hela radens JSON
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

// EnumValue tål att ett Monitor-enumfält serialiseras antingen som tal (3) eller
// som sträng ("VariableInspection"). Lagras normaliserat som sträng så jämförelser
// kan göras oavsett form. VERIFIERA (Steg 0) vilken form Monitor faktiskt skickar.
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

// Part — /api/v1/Inventory/Parts. Beskrivning + ExtraDescription (förväntas bära
// stålsort + ev. cert-krav i fritext). ReceivingInspectionType + TraceabilityMode
// styr om artikeln kräver materialcert (se RequiresCert). Raw bär hela artikelns
// JSON för evidens i UI:t.
//
// VERIFIERA (Steg 0): att ExtraDescription bär stålsort/cert-krav; exakt
// serialisering och värdemängd för ReceivingInspectionType (None/Always/
// VariableInspection) och TraceabilityMode (Batch?); om CurrentAlloyId finns.
type Part struct {
	ID                      ID              `json:"Id"`
	PartNumber              string          `json:"PartNumber"`
	Description             string          `json:"Description"`
	ExtraDescription        string          `json:"ExtraDescription"`
	ReceivingInspectionType EnumValue       `json:"ReceivingInspectionType"` // VERIFIERA
	TraceabilityMode        EnumValue       `json:"TraceabilityMode"`        // VERIFIERA
	CurrentAlloyId          ID              `json:"CurrentAlloyId"`          // VERIFIERA
	Raw                     json.RawMessage `json:"-"`                       // hela artikel-JSON:en
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

// RequiresCert avgör om artikeln kräver materialcert. VERIFIERA: exakt vilka
// värden som gäller — defaulttolkningen är att tomt/None/0 inte kräver cert,
// allt annat på ReceivingInspectionType kräver cert, samt TraceabilityMode=Batch.
func (p *Part) RequiresCert() bool {
	rit := normEnum(string(p.ReceivingInspectionType))
	if rit != "" && rit != "none" && rit != "0" {
		return true
	}
	tm := normEnum(string(p.TraceabilityMode))
	return tm == "batch" || tm == "2" // VERIFIERA enum-värdet för Batch
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
