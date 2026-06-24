package sickan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ---------------------------------------------------------------------------
// Monitor read-tools (Fas 3). Alla är LÄSANDE — de slår upp data i Monitor ERP
// och föreslår, men skriver aldrig (write-vägen ReportArrivals kommer i Fas 4
// och gateas bakom uttrycklig bekräftelse).
// ---------------------------------------------------------------------------

var monitorFindPurchaseOrderTool = anthropic.ToolParam{
	Name:        "monitor_find_purchase_order",
	Description: anthropic.String("Slår upp en inköpsorder i Monitor ERP via dess ordernummer och returnerar ordern, leverantörsnamn och orderrader. Använd för att se vad som är beställt och vad som återstår att leverera."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number": map[string]any{"type": "string", "description": "Inköpsorderns nummer, t.ex. \"B127196\"."},
		},
		Required: []string{"order_number"},
	},
}

var monitorFindSupplierTool = anthropic.ToolParam{
	Name:        "monitor_find_supplier",
	Description: anthropic.String("Söker leverantörer i Monitor ERP på leverantörskod (exakt) eller namn (delsträng). Returnerar matchande leverantörer med Id, kod och namn."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"term": map[string]any{"type": "string", "description": "Leverantörskod eller del av namnet."},
		},
		Required: []string{"term"},
	},
}

var monitorFillMissingCertDataTool = anthropic.ToolParam{
	Name:        "monitor_fill_missing_cert_data",
	Description: anthropic.String("Slår upp ett kö-certifikats charge i Monitor (ProductRecords → inköpsorder → leverantör) och föreslår ifyllnad av saknade fält (t.ex. B-nummer/ordernummer). LÄSER bara och returnerar ett förslag — skriver inget. Tillämpa ändringar via update_queue_item efter användarens ja."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"filename": map[string]any{"type": "string", "description": "Cert-filnamnet i kön."},
		},
		Required: []string{"filename"},
	},
}

// monitorCtx ger en bakgrundskontext med timeout för ett Monitor-anrop.
func monitorCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// monitorReady kontrollerar att klienten finns (Monitor kan vara okonfigurerad).
func (tb *Toolbox) monitorReady() error {
	if tb.Monitor == nil {
		return fmt.Errorf("Monitor är inte konfigurerad — öppna ⚙️ Inställningar och fyll i URL, användarnamn och lösenord (eller sätt miljövariablerna MONITOR_URL/USER/PASSWORD)")
	}
	return nil
}

func (tb *Toolbox) monitorFindPurchaseOrder(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	var args struct {
		OrderNumber string `json:"order_number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if args.OrderNumber == "" {
		return "", fmt.Errorf("order_number krävs")
	}
	ctx, cancel := monitorCtx()
	defer cancel()
	po, err := tb.Monitor.FindPurchaseOrderByNumber(ctx, args.OrderNumber)
	if err != nil {
		return "", err
	}
	if po == nil {
		out, _ := json.Marshal(map[string]any{"found": false, "order_number": args.OrderNumber})
		return string(out), nil
	}
	rows, err := tb.Monitor.GetPurchaseOrderRows(ctx, po.ID)
	if err != nil {
		return "", err
	}
	supplierName := ""
	if sup, _ := tb.Monitor.GetSupplier(ctx, po.BusinessContactId); sup != nil {
		supplierName = sup.Name
	}
	out, _ := json.Marshal(map[string]any{
		"found":         true,
		"order":         po,
		"supplier_name": supplierName,
		"rows":          rows,
	})
	return string(out), nil
}

func (tb *Toolbox) monitorFindSupplier(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	var args struct {
		Term string `json:"term"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if args.Term == "" {
		return "", fmt.Errorf("term krävs")
	}
	ctx, cancel := monitorCtx()
	defer cancel()
	sups, err := tb.Monitor.FindSupplier(ctx, args.Term)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"suppliers": sups, "count": len(sups)})
	return string(out), nil
}

func (tb *Toolbox) monitorFillMissingCertData(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if !safeName(args.Filename) {
		return "", fmt.Errorf("ogiltigt filnamn")
	}
	c, err := tb.Repo.GetCertificateByFilename(args.Filename)
	if err != nil {
		return "", fmt.Errorf("hittar inte cert %q i DB: %w", args.Filename, err)
	}
	charge := c.Charge
	if c.CorrectedCharge != "" {
		charge = c.CorrectedCharge
	}
	if charge == "" {
		return "", fmt.Errorf("certet saknar charge — kan inte slå upp i Monitor")
	}
	ctx, cancel := monitorCtx()
	defer cancel()
	recs, err := tb.Monitor.FindProductRecords(ctx, charge)
	if err != nil {
		return "", err
	}
	type match struct {
		OrderNumber  string `json:"order_number"`
		SupplierName string `json:"supplier_name"`
		PartId       int64  `json:"part_id"`
		SerialNumber string `json:"serial_number"`
	}
	matches := make([]match, 0, len(recs))
	for i, r := range recs {
		if i >= 10 {
			break
		}
		m := match{PartId: int64(r.PartId), SerialNumber: r.SerialNumber}
		if r.PurchaseOrderId != 0 {
			if po, _ := tb.Monitor.GetPurchaseOrder(ctx, r.PurchaseOrderId); po != nil {
				m.OrderNumber = po.OrderNumber
				if sup, _ := tb.Monitor.GetSupplier(ctx, po.BusinessContactId); sup != nil {
					m.SupplierName = sup.Name
				}
			}
		}
		matches = append(matches, m)
	}
	var currentBNumbers []string
	if c.BNumbers != "" {
		_ = json.Unmarshal([]byte(c.BNumbers), &currentBNumbers)
	}
	out, _ := json.Marshal(map[string]any{
		"filename": args.Filename,
		"charge":   charge,
		"current": map[string]any{
			"material":       c.Material,
			"material_short": c.MaterialShort,
			"dimensions":     c.Dimensions,
			"b_numbers":      currentBNumbers,
		},
		"monitor_matches": matches,
		"note":            "Förslag — skriv INGET utan användarens uttryckliga ja. Tillämpa via update_queue_item.",
	})
	return string(out), nil
}
