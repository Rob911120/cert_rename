package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Endpoint-paths (relativt apiBase()). Verifierade mot dokumentationscrawlen.
const (
	pathPurchaseOrders    = "/api/v1/Purchase/PurchaseOrders"
	pathPurchaseOrderRows = "/api/v1/Purchase/PurchaseOrderRows"
	pathSuppliers         = "/api/v1/Purchase/Suppliers"
	pathProductRecords    = "/api/v1/Inventory/ProductRecords"
	pathParts             = "/api/v1/Inventory/Parts"
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

// GetUpcomingOrderRows hämtar kommande inleveranser i fönstret [from, to] direkt
// från PurchaseOrderRows: orderrader som inte är fullt levererade (RestQuantity
// gt 0) och vars DeliveryDate ligger i intervallet. Artikeln kommer inline via
// $expand=Part (eliminerar ett GetPart-anrop per rad). Paginerat via getAllPages
// (loopar tills tom sida / följer @odata.nextLink).
//
// Steg-0-dumpen bekräftade valet av endpoint: PurchaseOrderDeliveryRows bar bara
// REDAN inlevererat gods (tomt DeliveryDate, ArrivedQuantity alltid >0, ingen
// nästlad orderrad via $expand), medan PurchaseOrderRows har ifyllt DeliveryDate,
// RestQuantity och fungerande $expand=Part.
//
// Datumfönstret avgränsas KLIENTSIDAN: Monitors OData-parser avvisar
// datumliteraler i $filter (400 "invalid BoolCompExprTail" redan vid första
// datumet — den läser "2026" som ett tal). Servern filtrerar därför bara på
// RestQuantity gt 0 (+ sorterar på DeliveryDate, vilket dumpen visade fungerar),
// och vi släpper rader utanför [from, to] efteråt. Externa operationsrader
// (legoarbete utan artikel, PartId 0) släpps igenom här men filtreras i worker-lagret.
func (c *Client) GetUpcomingOrderRows(ctx context.Context, from, to time.Time) ([]PurchaseOrderRow, UpcomingFetchStats, error) {
	q := NewQuery().
		Filter("RestQuantity gt 0").
		Expand("Part").
		OrderBy("DeliveryDate asc")
	rows, err := getAllPages[PurchaseOrderRow](ctx, c, pathPurchaseOrderRows, q, upcomingPageSize)
	if err != nil {
		return nil, UpcomingFetchStats{}, err
	}
	stats := UpcomingFetchStats{Fetched: len(rows)}
	fromD, toD := from.Format("2006-01-02"), to.Format("2006-01-02")
	out := make([]PurchaseOrderRow, 0, len(rows))
	for _, r := range rows {
		d := dateOnly(r.DeliveryDate)
		if d != "" { // spåra datumspann över ALLA hämtade rader (för diagnostik)
			if stats.MinDate == "" || d < stats.MinDate {
				stats.MinDate = d
			}
			if d > stats.MaxDate {
				stats.MaxDate = d
			}
		}
		if d == "" || d < fromD || d > toD {
			continue // tomt/0001-01-01 eller utanför fönstret
		}
		out = append(out, r)
	}
	return out, stats, nil
}

// UpcomingFetchStats beskriver vad servern gav INNAN datumfönstret filtrerades
// bort klientsidan — så worker-loggen kan visa hela tratten och göra det
// uppenbart om noll rader beror på Monitor (Fetched=0), på fel fönster
// (datumspann utanför [from,to]) eller på tomma datum (Min/MaxDate tomma).
type UpcomingFetchStats struct {
	Fetched int    // antal öppna rader (RestQuantity gt 0) servern gav
	MinDate string // tidigaste ifyllda DeliveryDate (YYYY-MM-DD), "" om inga
	MaxDate string // senaste ifyllda DeliveryDate (YYYY-MM-DD), "" om inga
}

// dateOnly plockar YYYY-MM-DD ur ett Monitor-datum ("2026-06-26T00:00:00+02:00"
// → "2026-06-26"). Tomt om strängen är kortare än ett datum. YYYY-MM-DD sorterar
// lexikalt = kronologiskt, så fönsterjämförelsen kan göras direkt på strängen.
func dateOnly(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		return s[:10]
	}
	return ""
}

// GetPartsByIds hämtar artiklar för en uppsättning ID:n, batchat i bitar om
// partsBatchSize ("Id eq A or Id eq B …"), och returnerar dem som en karta per ID.
// Tänkt som komplement när inline-$expand-datan saknas för någon rad — anroparen
// faller annars tillbaka på Part som redan kom via GetUpcomingOrderRows.
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
