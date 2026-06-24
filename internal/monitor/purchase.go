package monitor

import (
	"context"
	"fmt"
)

// Endpoint-paths (relativt apiBase()). Verifierade mot dokumentationscrawlen.
const (
	pathPurchaseOrders    = "/api/v1/Purchase/PurchaseOrders"
	pathPurchaseOrderRows = "/api/v1/Purchase/PurchaseOrderRows"
	pathSuppliers         = "/api/v1/Purchase/Suppliers"
	pathProductRecords    = "/api/v1/Inventory/ProductRecords"
)

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
