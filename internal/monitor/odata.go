package monitor

import (
	"net/url"
	"strconv"
	"strings"
)

// Query bygger OData-query-options ($filter/$select/$expand/$orderby/$top) för
// Monitors queryable endpoints. Chainbar: NewQuery().Filter(...).Top(...).
type Query struct {
	filter  string
	selects []string
	expand  []string
	orderby string
	top     int
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
	return v
}

// odataEsc escapar enkel-citattecken i ett OData-strängliteral ('' = ett ').
func odataEsc(s string) string { return strings.ReplaceAll(s, "'", "''") }
