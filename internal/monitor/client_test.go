package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubMonitor returnerar en httptest-server som spelar Monitor-API:t: en
// login-endpoint som returnerar SessionId, och OData-queryable endpoints som
// svarar med {"value":[...]}. Den verifierar också att session-headern följer med.
func stubMonitor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/001.1/login"):
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if req["Username"] != "kalle" || req["Password"] != "hemligt" {
				http.Error(w, "bad creds", http.StatusUnauthorized)
				return
			}
			if req["ForceRelogin"] != true {
				t.Errorf("login: ForceRelogin borde vara true, fick %v", req["ForceRelogin"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"SessionId":"sess-123"}`))

		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrders"):
			if got := r.Header.Get("X-Monitor-SessionId"); got != "sess-123" {
				t.Errorf("PurchaseOrders: session-header = %q, vill ha sess-123", got)
			}
			if f := r.URL.Query().Get("$filter"); !strings.Contains(f, "OrderNumber eq 'PO-1'") {
				t.Errorf("PurchaseOrders: $filter = %q, saknar OrderNumber-filter", f)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"Id":1,"OrderNumber":"PO-1","Status":1,"BusinessContactId":7}]}`))

		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrderRows"):
			if f := r.URL.Query().Get("$filter"); !strings.Contains(f, "ParentOrderId eq 1") {
				t.Errorf("PurchaseOrderRows: $filter = %q, saknar ParentOrderId-filter", f)
			}
			// bare array — testa att decodern klarar även icke-wrappat svar
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":11,"ParentOrderId":1,"PartId":5,"RowIndex":1,"OrderedQuantity":10,"RestQuantity":10}]`))

		case strings.HasSuffix(r.URL.Path, "/Inventory/ProductRecords"):
			if f := r.URL.Query().Get("$filter"); !strings.Contains(f, "ChargeNumber eq '610042'") {
				t.Errorf("ProductRecords: $filter = %q, saknar ChargeNumber-filter", f)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"Id":99,"ChargeNumber":"610042","SerialNumber":"S1","PartId":5,"PurchaseOrderId":1}]}`))

		case strings.HasSuffix(r.URL.Path, "/Purchase/Suppliers"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"Id":7,"SupplierCode":"BV","Name":"BE Group"}]}`))

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLogin_SetsSession(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if c.SessionID() != "sess-123" {
		t.Errorf("SessionID = %q, vill ha sess-123", c.SessionID())
	}
}

func TestLogin_BadCredentials(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	if err := c.Login(context.Background(), "fel", "fel"); err == nil {
		t.Error("login med fel creds borde returnera fel")
	}
}

func TestListPurchaseOrders_FilterParseAndSession(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	q := NewQuery().Filter("OrderNumber eq 'PO-1'").Top(1)
	orders, err := c.ListPurchaseOrders(context.Background(), q)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("förväntade 1 order, fick %d", len(orders))
	}
	if orders[0].OrderNumber != "PO-1" || orders[0].BusinessContactId != 7 {
		t.Errorf("order = %+v", orders[0])
	}
}

func TestGetPurchaseOrderRows_BareArray(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	rows, err := c.GetPurchaseOrderRows(context.Background(), 1)
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(rows) != 1 || rows[0].PartId != 5 || rows[0].ParentOrderId != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestFindProductRecords_ByCharge(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	recs, err := c.FindProductRecords(context.Background(), "610042")
	if err != nil {
		t.Fatalf("product records: %v", err)
	}
	if len(recs) != 1 || recs[0].ChargeNumber != "610042" || recs[0].PurchaseOrderId != 1 || recs[0].PartId != 5 {
		t.Fatalf("recs = %+v", recs)
	}
}

func TestQuery_BuildsODataParams(t *testing.T) {
	vals := NewQuery().
		Filter("ChargeNumber eq '1'").
		Select("Id", "ChargeNumber").
		Expand("Rows").
		OrderBy("Id desc").
		Top(5).
		Values()
	if vals.Get("$filter") != "ChargeNumber eq '1'" {
		t.Errorf("$filter = %q", vals.Get("$filter"))
	}
	if vals.Get("$select") != "Id,ChargeNumber" {
		t.Errorf("$select = %q", vals.Get("$select"))
	}
	if vals.Get("$expand") != "Rows" {
		t.Errorf("$expand = %q", vals.Get("$expand"))
	}
	if vals.Get("$orderby") != "Id desc" {
		t.Errorf("$orderby = %q", vals.Get("$orderby"))
	}
	if vals.Get("$top") != "5" {
		t.Errorf("$top = %q", vals.Get("$top"))
	}
}
