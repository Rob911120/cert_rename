package monitor

import (
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

// PurchaseOrderRow — /api/v1/Purchase/PurchaseOrderRows. Länkas till sin order
// via ParentOrderId och till artikeln via PartId. ArrivalReporting är en
// Monitor-flagga vars exakta semantik är OVERIFIERAD (troligen: om raden ingår i
// godsmottagnings-/inleveransrapporteringsflödet, ev. text-/tjänsterad utan
// lagerartikel) — den används som varning, inte spärr. RestQuantity är
// kvarvarande ej levererat.
type PurchaseOrderRow struct {
	ID                ID      `json:"Id"`
	ParentOrderId     ID      `json:"ParentOrderId"`
	PartId            ID      `json:"PartId"`
	RowIndex          int     `json:"RowIndex"`
	OrderedQuantity   float64 `json:"OrderedQuantity"`
	DeliveredQuantity float64 `json:"DeliveredQuantity"`
	RestQuantity      float64 `json:"RestQuantity"`
	UnitId            ID      `json:"UnitId"`
	ArrivalReporting  bool    `json:"ArrivalReporting"`
	RowStatus         int     `json:"RowStatus"`
}

// Supplier — /api/v1/Purchase/Suppliers.
type Supplier struct {
	ID              ID     `json:"Id"`
	SupplierCode    string `json:"SupplierCode"`
	Name            string `json:"Name"`
	AlternativeName string `json:"AlternativeName"`
}

// ArrivalRow är en rad i en ReportArrivals-write. OBS: ArrivalRow:s exakta
// fältuppsättning är INTE dokumenterad i API-crawlen — PurchaseOrderRowId +
// Quantity är vårt bästa antagande och MÅSTE verifieras live innan skarp drift.
type ArrivalRow struct {
	PurchaseOrderRowId ID      `json:"PurchaseOrderRowId"`
	Quantity           float64 `json:"Quantity"`
}

// ReportArrivalsRequest är payloaden till POST .../PurchaseOrders/ReportArrivals
// (Monitor v2.36+). DeliveryNoteNumber/WaybillNumber kan vara obligatoriska
// beroende på systeminställningar. ArrivalDate/ReportingEmployeeId defaultar i
// API:t (idag resp. inloggad användare) om de utelämnas.
type ReportArrivalsRequest struct {
	ArrivalDate         string       `json:"ArrivalDate,omitempty"`
	ReportingEmployeeId ID           `json:"ReportingEmployeeId,omitempty"`
	DeliveryNoteNumber  string       `json:"DeliveryNoteNumber,omitempty"`
	WaybillNumber       string       `json:"WaybillNumber,omitempty"`
	Rows                []ArrivalRow `json:"Rows"`
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
