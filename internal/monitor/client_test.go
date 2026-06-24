package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestID_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		in      string
		want    ID
		wantErr bool
	}{
		{`"123456789012345678"`, 123456789012345678, false}, // Monitor: strängat 64-bitars-ID
		{`123`, 123, false},                                 // bart tal
		{`"0"`, 0, false},
		{`null`, 0, false},
		{`""`, 0, false},
		{`"abc"`, 0, true}, // ogiltigt → fel, inte panik
	}
	for _, tc := range cases {
		var got ID
		err := json.Unmarshal([]byte(tc.in), &got)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Unmarshal(%s): förväntade fel, fick %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Unmarshal(%s): oväntat fel: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Unmarshal(%s) = %d, vill ha %d", tc.in, got, tc.want)
		}
	}
}

// Regression: Monitor returnerar Id som JSON-sträng — hela listan ska ändå
// avkodas utan att krascha (tidigare bug: int64-fält → unmarshal-fel).
func TestDecodeList_StringIDs(t *testing.T) {
	body := []byte(`{"value":[{"Id":"123456789012345678","OrderNumber":"PO-9","BusinessContactId":"42"}]}`)
	var orders []PurchaseOrder
	if err := decodeList(body, &orders); err != nil {
		t.Fatalf("decodeList med strängade ID:n: %v", err)
	}
	if len(orders) != 1 || orders[0].ID != 123456789012345678 || orders[0].BusinessContactId != 42 {
		t.Fatalf("orders = %+v", orders)
	}
}

// Regression: en utgången session (401) ska få klienten att logga in igen med
// sparade credentials och försöka anropet en gång till — automatiskt.
func TestAutoReloginOn401(t *testing.T) {
	var logins, orderHits int
	var session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			logins++
			session = "sess-" + strconv.Itoa(logins)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"SessionId":"` + session + `"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrders"):
			orderHits++
			if orderHits == 1 {
				http.Error(w, "session expired", http.StatusUnauthorized) // simulera utgången session
				return
			}
			if got := r.Header.Get(SessionHeader); got != session {
				t.Errorf("retry använde fel session: %q != %q", got, session)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"Id":"1","OrderNumber":"PO-1","BusinessContactId":"7"}]}`))
		default:
			http.Error(w, "oväntad path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	orders, err := c.ListPurchaseOrders(context.Background(), NewQuery().Top(1))
	if err != nil {
		t.Fatalf("list efter 401/relogin: %v", err)
	}
	if len(orders) != 1 || orders[0].OrderNumber != "PO-1" {
		t.Fatalf("orders = %+v", orders)
	}
	if logins != 2 {
		t.Errorf("förväntade 2 logins (initial + relogin), fick %d", logins)
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

func TestReportArrivals_PostsPayloadWithSession(t *testing.T) {
	var gotSession string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"SessionId":"sess-123"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/PurchaseOrders/ReportArrivals"):
			gotSession = r.Header.Get("X-Monitor-SessionId")
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Ok":true}`))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	res, err := c.ReportArrivals(context.Background(), ReportArrivalsRequest{
		DeliveryNoteNumber: "CCF000195",
		Rows:               []ArrivalRow{{PurchaseOrderRowId: 11, Quantity: 6}},
	})
	if err != nil {
		t.Fatalf("ReportArrivals: %v", err)
	}
	if gotSession != "sess-123" {
		t.Errorf("session-header = %q, vill ha sess-123", gotSession)
	}
	body := string(gotBody)
	if !strings.Contains(body, `"PurchaseOrderRowId":11`) {
		t.Errorf("body saknar PurchaseOrderRowId: %s", body)
	}
	if !strings.Contains(body, `"DeliveryNoteNumber":"CCF000195"`) {
		t.Errorf("body saknar DeliveryNoteNumber: %s", body)
	}
	if !strings.Contains(string(res), "Ok") {
		t.Errorf("oväntat svar: %s", string(res))
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
