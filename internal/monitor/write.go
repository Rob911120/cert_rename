package monitor

import (
	"context"
	"encoding/json"
	"fmt"
)

// pathReportArrivals är write-endpointen för inleveransrapportering (v2.36+).
const pathReportArrivals = "/api/v1/Purchase/PurchaseOrders/ReportArrivals"

// ReportArrivals rapporterar inleverans (skarpt write) och returnerar API:ets
// råa svar (ArrivalResult-shapen är ej dokumenterad i crawlen). Kräver giltig
// session + köpt API-write-licens. Anropas av cert-renamer BARA bakom uttrycklig
// bekräftelse och en orderrad i taget — gaten ligger i sickan-verktyget.
func (c *Client) ReportArrivals(ctx context.Context, req ReportArrivalsRequest) (json.RawMessage, error) {
	if len(req.Rows) == 0 {
		return nil, fmt.Errorf("ReportArrivals: minst en rad krävs")
	}
	var raw json.RawMessage
	if err := c.postJSON(ctx, pathReportArrivals, req, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
