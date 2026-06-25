package monitor

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Query bygger OData-query-options ($filter/$select/$expand/$orderby/$top/$skip)
// för Monitors queryable endpoints. Chainbar: NewQuery().Filter(...).Top(...).
type Query struct {
	filter  string
	selects []string
	expand  []string
	orderby string
	top     int
	skip    int
}

// NewQuery skapar en tom query.
func NewQuery() *Query { return &Query{} }

// Filter sätter $filter-uttrycket (OData-syntax, t.ex. "ChargeNumber eq '610042'").
func (q *Query) Filter(f string) *Query { q.filter = f; return q }

// Select begränsar returnerade fält ($select).
func (q *Query) Select(fields ...string) *Query { q.selects = append(q.selects, fields...); return q }

// Expand inkluderar relaterade entiteter ($expand), t.ex. "Rows".
func (q *Query) Expand(e ...string) *Query { q.expand = append(q.expand, e...); return q }

// OrderBy sätter $orderby, t.ex. "OrderDate desc".
func (q *Query) OrderBy(o string) *Query { q.orderby = o; return q }

// Top begränsar antalet rader ($top).
func (q *Query) Top(n int) *Query { q.top = n; return q }

// Skip hoppar över de n första raderna ($skip). Används för paginering: hämta
// sida för sida (Skip(0), Skip(top), Skip(2*top) …) tills en tom sida kommer.
func (q *Query) Skip(n int) *Query { q.skip = n; return q }

// Values bygger url.Values med OData-options. Tomma options utelämnas.
func (q *Query) Values() url.Values {
	v := url.Values{}
	if q == nil {
		return v
	}
	if q.filter != "" {
		v.Set("$filter", q.filter)
	}
	if len(q.selects) > 0 {
		v.Set("$select", strings.Join(q.selects, ","))
	}
	if len(q.expand) > 0 {
		v.Set("$expand", strings.Join(q.expand, ","))
	}
	if q.orderby != "" {
		v.Set("$orderby", q.orderby)
	}
	if q.top > 0 {
		v.Set("$top", strconv.Itoa(q.top))
	}
	if q.skip > 0 {
		v.Set("$skip", strconv.Itoa(q.skip))
	}
	return v
}

// odataEsc escapar enkel-citattecken i ett OData-strängliteral ('' = ett ').
func odataEsc(s string) string { return strings.ReplaceAll(s, "'", "''") }

// odataDate formaterar en tidpunkt som ett OData-datumliteral (ISO 8601, UTC),
// t.ex. "2026-06-25T16:30:00Z" — för filter som "DeliveryDate ge <from>". OData
// vill ha datum/tid OCITERAT (Edm.DateTimeOffset), till skillnad från strängar.
//
// VERIFIERA: om Monitors DeliveryDate är Edm.Date (datum utan tid) vill servern
// i stället ha "2026-06-25" utan tidsdel — justeras efter Steg-0-dumpen.
func odataDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
