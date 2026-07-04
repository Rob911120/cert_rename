package sickan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// stubMonitorServer spelar de Monitor-endpoints sickan-verktygen rör.
func stubMonitorServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"sess-xyz"}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrders"):
			// Både FindPurchaseOrderByNumber och GetPurchaseOrder(id) träffar hit.
			_, _ = w.Write([]byte(`{"value":[{"Id":1,"OrderNumber":"B127196","Status":1,"BusinessContactId":7}]}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrderRows"):
			_, _ = w.Write([]byte(`{"value":[{"Id":11,"ParentOrderId":1,"PartId":5,"RowIndex":1,"OrderedQuantity":10,"RestQuantity":4}]}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/Suppliers"):
			_, _ = w.Write([]byte(`{"value":[{"Id":7,"SupplierCode":"BV","Name":"BE Group"}]}`))
		case strings.HasSuffix(r.URL.Path, "/Inventory/ProductRecords"):
			_, _ = w.Write([]byte(`{"value":[{"Id":99,"ChargeNumber":"610042","SerialNumber":"S1","PartId":5,"PurchaseOrderId":1}]}`))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func loggedInMonitor(t *testing.T, srv *httptest.Server) *monitor.Client {
	t.Helper()
	c := monitor.New(srv.URL)
	if err := c.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("monitor login: %v", err)
	}
	return c
}

func Test_Dispatch_MonitorTool_NilMonitor(t *testing.T) {
	tb, _ := setupToolbox(t) // Monitor är nil
	if _, err := tb.Dispatch("monitor_find_purchase_order", json.RawMessage(`{"order_number":"B1"}`)); err == nil {
		t.Error("borde ge fel när Monitor inte är konfigurerad")
	}
}

func Test_Dispatch_MonitorFindPurchaseOrder(t *testing.T) {
	tb, _ := setupToolbox(t)
	tb.Monitor = loggedInMonitor(t, stubMonitorServer(t))

	res, err := tb.Dispatch("monitor_find_purchase_order", json.RawMessage(`{"order_number":"B127196"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var resp struct {
		Found        bool   `json:"found"`
		SupplierName string `json:"supplier_name"`
		Order        struct {
			OrderNumber string `json:"OrderNumber"`
		} `json:"order"`
		Rows []struct {
			PartId int64 `json:"PartId"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(res.Summary), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, res.Summary)
	}
	if !resp.Found || resp.Order.OrderNumber != "B127196" || resp.SupplierName != "BE Group" || len(resp.Rows) != 1 {
		t.Fatalf("resp = %+v (%s)", resp, res.Summary)
	}
}

func Test_Dispatch_MonitorFindSupplier(t *testing.T) {
	tb, _ := setupToolbox(t)
	tb.Monitor = loggedInMonitor(t, stubMonitorServer(t))

	res, err := tb.Dispatch("monitor_find_supplier", json.RawMessage(`{"term":"BE"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var resp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(res.Summary), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, res.Summary)
	}
	if resp.Count != 1 {
		t.Errorf("count = %d (%s)", resp.Count, res.Summary)
	}
}

func Test_Dispatch_MonitorFillMissingCertData(t *testing.T) {
	tb, _ := setupToolbox(t)
	tb.Monitor = loggedInMonitor(t, stubMonitorServer(t))

	// Repo med ett cert i kön vars charge=610042.
	db, err := store.InitDB(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	repo := store.NewRepository(db)
	tb.Repo = repo
	if _, err := repo.InsertCertificate(&store.Certificate{
		PDFHash:          "h1",
		Filename:         "610042-S355-B.pdf",
		OriginalFilename: "orig.pdf",
		CertType:         "3.1",
		Charge:           "610042",
		Material:         "S355J2",
		Confidence:       "high",
		ModelUsed:        "test",
		Status:           "queue",
		ExtractedAt:      "2026-01-01",
	}); err != nil {
		t.Fatalf("insert cert: %v", err)
	}

	res, err := tb.Dispatch("monitor_fill_missing_cert_data", json.RawMessage(`{"filename":"610042-S355-B.pdf"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var resp struct {
		Charge         string `json:"charge"`
		MonitorMatches []struct {
			OrderNumber  string `json:"order_number"`
			SupplierName string `json:"supplier_name"`
		} `json:"monitor_matches"`
	}
	if err := json.Unmarshal([]byte(res.Summary), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, res.Summary)
	}
	if resp.Charge != "610042" {
		t.Errorf("charge = %q", resp.Charge)
	}
	if len(resp.MonitorMatches) != 1 || resp.MonitorMatches[0].OrderNumber != "B127196" || resp.MonitorMatches[0].SupplierName != "BE Group" {
		t.Fatalf("matches = %+v (%s)", resp.MonitorMatches, res.Summary)
	}
}
