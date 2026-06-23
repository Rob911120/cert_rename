package monitor

// Typerna speglar Monitor G5 001.1-API:ets entiteter. Fält-casing matchar API:t
// (PascalCase) — verifierat mot dokumentationscrawlen i ~/dev/monitor_api_docs_v2.
// Bara de fält cert-renamer behöver är med; OData $select kan trimma svaren.

// PurchaseOrder — /api/v1/Purchase/PurchaseOrders. Leverantören länkas via
// BusinessContactId (→ Suppliers.Id).
type PurchaseOrder struct {
	ID                int64  `json:"Id"`
	OrderNumber       string `json:"OrderNumber"`
	OrderDate         string `json:"OrderDate"`
	Status            int    `json:"Status"`
	BusinessContactId int64  `json:"BusinessContactId"`
}

// PurchaseOrderRow — /api/v1/Purchase/PurchaseOrderRows. Länkas till sin order
// via ParentOrderId och till artikeln via PartId. ArrivalReporting säger om
// raden inleveransrapporteras; RestQuantity är kvarvarande ej levererat.
type PurchaseOrderRow struct {
	ID                int64   `json:"Id"`
	ParentOrderId     int64   `json:"ParentOrderId"`
	PartId            int64   `json:"PartId"`
	RowIndex          int     `json:"RowIndex"`
	OrderedQuantity   float64 `json:"OrderedQuantity"`
	DeliveredQuantity float64 `json:"DeliveredQuantity"`
	RestQuantity      float64 `json:"RestQuantity"`
	UnitId            int64   `json:"UnitId"`
	ArrivalReporting  bool    `json:"ArrivalReporting"`
	RowStatus         int     `json:"RowStatus"`
}

// Supplier — /api/v1/Purchase/Suppliers.
type Supplier struct {
	ID              int64  `json:"Id"`
	SupplierCode    string `json:"SupplierCode"`
	Name            string `json:"Name"`
	AlternativeName string `json:"AlternativeName"`
}

// ProductRecord — /api/v1/Inventory/ProductRecords. Bär charge/B-nr
// (ChargeNumber/SerialNumber) och länkar till en inköpsorder via PurchaseOrderId
// + artikeln via PartId. OBS: ingen direkt FK till PurchaseOrderRow — matchning
// till orderrad sker via (PurchaseOrderId, PartId).
type ProductRecord struct {
	ID              int64  `json:"Id"`
	SerialNumber    string `json:"SerialNumber"`
	ChargeNumber    string `json:"ChargeNumber"`
	PartId          int64  `json:"PartId"`
	PurchaseOrderId int64  `json:"PurchaseOrderId"`
}
