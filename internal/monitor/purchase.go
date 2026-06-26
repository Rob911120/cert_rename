package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Endpoint-paths (relativt apiBase()). Verifierade mot dokumentationscrawlen.
const (
	pathPurchaseOrders            = "/api/v1/Purchase/PurchaseOrders"
	pathPurchaseOrderRows         = "/api/v1/Purchase/PurchaseOrderRows"
	pathPurchaseOrderDeliveryRows = "/api/v1/Purchase/PurchaseOrderDeliveryRows"
	pathSuppliers                 = "/api/v1/Purchase/Suppliers"
	pathProductRecords            = "/api/v1/Inventory/ProductRecords"
	pathParts                     = "/api/v1/Inventory/Parts"
)

// upcomingPageSize är $top per sida vid hämtning av kommande inleveranser.
const upcomingPageSize = 200

// partsBatchSize är hur många artikel-ID:n som slås ihop per "Id eq … or …"-anrop.
const partsBatchSize = 20

// ListPurchaseOrders listar inköpsorder enligt query (nil = default Top 50).
func (c *Client) ListPurchaseOrders(ctx context.Context, q *Query) ([]PurchaseOrder, error) {
	if q == nil {
		q = NewQuery().Top(50)
	}
	var out []PurchaseOrder
	err := c.getList(ctx, pathPurchaseOrders, q, &out)
	return out, err
}

// FindPurchaseOrderByNumber hämtar en order via dess OrderNumber. Returnerar nil
// utan fel om ingen hittas.
func (c *Client) FindPurchaseOrderByNumber(ctx context.Context, orderNumber string) (*PurchaseOrder, error) {
	q := NewQuery().Filter(fmt.Sprintf("OrderNumber eq '%s'", odataEsc(orderNumber))).Top(1)
	orders, err := c.ListPurchaseOrders(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, nil
	}
	return &orders[0], nil
}

// GetPurchaseOrder hämtar en inköpsorder via dess Id. nil utan fel om saknas.
func (c *Client) GetPurchaseOrder(ctx context.Context, id ID) (*PurchaseOrder, error) {
	q := NewQuery().Filter(fmt.Sprintf("Id eq %d", id)).Top(1)
	orders, err := c.ListPurchaseOrders(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, nil
	}
	return &orders[0], nil
}

// GetSupplier hämtar en leverantör via dess Id. nil utan fel om saknas.
func (c *Client) GetSupplier(ctx context.Context, id ID) (*Supplier, error) {
	q := NewQuery().Filter(fmt.Sprintf("Id eq %d", id)).Top(1)
	sups, err := c.ListSuppliers(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(sups) == 0 {
		return nil, nil
	}
	return &sups[0], nil
}

// GetPurchaseOrderRows hämtar raderna för en inköpsorder (ParentOrderId).
func (c *Client) GetPurchaseOrderRows(ctx context.Context, orderID ID) ([]PurchaseOrderRow, error) {
	q := NewQuery().Filter(fmt.Sprintf("ParentOrderId eq %d", orderID)).Top(200)
	var out []PurchaseOrderRow
	err := c.getList(ctx, pathPurchaseOrderRows, q, &out)
	return out, err
}

// ListSuppliers listar leverantörer enligt query (nil = default Top 50).
func (c *Client) ListSuppliers(ctx context.Context, q *Query) ([]Supplier, error) {
	if q == nil {
		q = NewQuery().Top(50)
	}
	var out []Supplier
	err := c.getList(ctx, pathSuppliers, q, &out)
	return out, err
}

// FindSupplier söker leverantör på SupplierCode (exakt) eller namn (contains).
func (c *Client) FindSupplier(ctx context.Context, term string) ([]Supplier, error) {
	esc := odataEsc(term)
	q := NewQuery().
		Filter(fmt.Sprintf("SupplierCode eq '%s' or contains(Name,'%s')", esc, esc)).
		Top(20)
	return c.ListSuppliers(ctx, q)
}

// FindProductRecords hittar ProductRecords (charge/B-nr-bärare) via ChargeNumber.
// Varje träff bär PurchaseOrderId + PartId som länkar till orderraden (matchning
// sker via (PurchaseOrderId, PartId) eftersom det inte finns en direkt rad-FK).
func (c *Client) FindProductRecords(ctx context.Context, charge string) ([]ProductRecord, error) {
	q := NewQuery().Filter(fmt.Sprintf("ChargeNumber eq '%s'", odataEsc(charge))).Top(50)
	var out []ProductRecord
	err := c.getList(ctx, pathProductRecords, q, &out)
	return out, err
}

// GetUpcomingDeliveryRows hämtar kommande inleveransrader i fönstret [from, to]:
// rader vars DeliveryDate ligger i intervallet och som ännu inte anlänt
// (ArrivedQuantity eq 0). Orderraden och dess artikel kommer inline via
// $expand=PurchaseOrderRow($expand=Part) (eliminerar ett GetPart-anrop per rad).
// Paginerat via getAllPages (loopar tills tom sida / följer @odata.nextLink).
//
// VERIFIERA (Steg 0): att PurchaseOrderDeliveryRows används i Pellys Monitor
// (annars fallback till PurchaseOrderRows + RestQuantity gt 0), exakt semantik
// för "ej anländ" (ArrivedQuantity eq 0?), samt att DeliveryDate är ifyllt.
func (c *Client) GetUpcomingDeliveryRows(ctx context.Context, from, to time.Time) ([]PurchaseOrderDeliveryRow, error) {
	filter := fmt.Sprintf(
		"DeliveryDate ge %s and DeliveryDate le %s and ArrivedQuantity eq 0",
		odataDate(from), odataDate(to),
	)
	q := NewQuery().
		Filter(filter).
		Expand("PurchaseOrderRow($expand=Part)").
		OrderBy("DeliveryDate asc")
	return getAllPages[PurchaseOrderDeliveryRow](ctx, c, pathPurchaseOrderDeliveryRows, q, upcomingPageSize)
}

// GetPartsByIds hämtar artiklar för en uppsättning ID:n, batchat i bitar om
// partsBatchSize ("Id eq A or Id eq B …"), och returnerar dem som en karta per ID.
// Tänkt som komplement när inline-$expand-datan saknas för någon rad — anroparen
// faller annars tillbaka på Part som redan kom via GetUpcomingDeliveryRows.
func (c *Client) GetPartsByIds(ctx context.Context, ids []ID) (map[ID]Part, error) {
	out := map[ID]Part{}
	uniq := dedupeIDs(ids)
	for i := 0; i < len(uniq); i += partsBatchSize {
		end := min(i+partsBatchSize, len(uniq))
		chunk := uniq[i:end]
		clauses := make([]string, 0, len(chunk))
		for _, id := range chunk {
			clauses = append(clauses, fmt.Sprintf("Id eq %d", id))
		}
		q := NewQuery().Filter(strings.Join(clauses, " or ")).Top(len(chunk))
		var parts []Part
		if err := c.getList(ctx, pathParts, q, &parts); err != nil {
			return out, err
		}
		for _, p := range parts {
			out[p.ID] = p
		}
	}
	return out, nil
}

// dedupeIDs returnerar de unika ID:na (0 utelämnas) i ursprunglig ordning.
func dedupeIDs(ids []ID) []ID {
	seen := make(map[ID]bool, len(ids))
	out := make([]ID, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
