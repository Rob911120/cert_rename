package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

// GetRaw ska returnera de oavkodade svarsbytena (inkl. {"value":...}-envelopen)
// så diagnostik kan inspektera exakta fältnamn innan typerna fästs.
func TestGetRaw_ReturnsUnparsedBytes(t *testing.T) {
	srv := stubMonitor(t)
	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	raw, err := c.GetRaw(context.Background(), "/api/v1/Purchase/Suppliers", NewQuery().Top(3))
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	// Envelopen ska vara kvar — bevisar att svaret inte avkodats/skalats av.
	if !strings.Contains(string(raw), `"value"`) || !strings.Contains(string(raw), `"SupplierCode":"BV"`) {
		t.Fatalf("GetRaw returnerade inte rå JSON: %s", raw)
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

// Paginering via @odata.nextLink: sida 1 (2 rader + nextLink) + sida 2 (1 rad,
// ingen nextLink) → 3 rader. Verifierar också gemena operatorer i $filter och
// nästlad $expand=PurchaseOrderRow($expand=Part).
func TestGetUpcomingDeliveryRows_PaginatesViaNextLink(t *testing.T) {
	var srv *httptest.Server
	var hits int
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderDeliveryRows"):
			hits++
			if hits == 1 {
				// Sida 1 bär den fulla queryn — verifiera gemena operatorer + nästlad expand.
				f := r.URL.Query().Get("$filter")
				for _, want := range []string{"DeliveryDate ge ", "DeliveryDate le ", "ArrivedQuantity eq 0"} {
					if !strings.Contains(f, want) {
						t.Errorf("$filter %q saknar %q (gemena operatorer?)", f, want)
					}
				}
				if exp := r.URL.Query().Get("$expand"); !strings.Contains(exp, "PurchaseOrderRow") || !strings.Contains(exp, "Part") {
					t.Errorf("$expand = %q, saknar nästlad Part", exp)
				}
				next := srv.URL + "/sv/001.1/api/v1/Purchase/PurchaseOrderDeliveryRows?%24skip=2"
				_, _ = fmt.Fprintf(w, `{"value":[{"Id":"1"},{"Id":"2"}],"@odata.nextLink":%q}`, next)
			} else {
				// Sida 2 via nextLink (bär bara $skip i den här stubben).
				_, _ = w.Write([]byte(`{"value":[{"Id":"3"}]}`))
			}
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login(context.Background(), "kalle", "hemligt"); err != nil {
		t.Fatalf("login: %v", err)
	}
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	rows, err := c.GetUpcomingDeliveryRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingDeliveryRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("vill ha 3 rader (sida1=2 + sida2=1), fick %d: %+v", len(rows), rows)
	}
	if rows[0].ID != 1 || rows[2].ID != 3 {
		t.Errorf("rad-ID:n fel: %+v", rows)
	}
}

// Servern har eget sidtak (2/sida) under vårt $top. Paginering MÅSTE fortsätta
// tills en TOM sida — inte stanna på "färre än begärt" (tyst trunkering).
func TestGetUpcomingDeliveryRows_SkipUntilEmptyDespiteServerCap(t *testing.T) {
	all := []string{`{"Id":"1"}`, `{"Id":"2"}`, `{"Id":"3"}`, `{"Id":"4"}`, `{"Id":"5"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderDeliveryRows"):
			skip, _ := strconv.Atoi(r.URL.Query().Get("$skip"))
			rows := []string{}
			if skip < len(all) {
				end := skip + 2 // servern cappar varje sida till 2 oavsett $top
				if end > len(all) {
					end = len(all)
				}
				rows = all[skip:end]
			}
			_, _ = fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(rows, ","))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	rows, err := c.GetUpcomingDeliveryRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingDeliveryRows: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("vill ha 5 rader trots sidtak 2, fick %d", len(rows))
	}
}

// En 401 mitt i pagineringen ska trigga relogin och fortsätta utan att tappa rader.
func TestGetUpcomingDeliveryRows_ReloginMidPagination(t *testing.T) {
	var srv *httptest.Server
	var logins, hits int
	var session string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			logins++
			session = "s" + strconv.Itoa(logins)
			_, _ = w.Write([]byte(`{"SessionId":"` + session + `"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderDeliveryRows"):
			hits++
			switch hits {
			case 1:
				next := srv.URL + "/sv/001.1/api/v1/Purchase/PurchaseOrderDeliveryRows?%24skip=2"
				_, _ = fmt.Fprintf(w, `{"value":[{"Id":"1"},{"Id":"2"}],"@odata.nextLink":%q}`, next)
			case 2:
				http.Error(w, "session expired", http.StatusUnauthorized) // utgången mitt i pagineringen
			default:
				if got := r.Header.Get(SessionHeader); got != session {
					t.Errorf("retry använde fel session: %q != %q", got, session)
				}
				_, _ = w.Write([]byte(`{"value":[{"Id":"3"}]}`))
			}
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	rows, err := c.GetUpcomingDeliveryRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingDeliveryRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("vill ha 3 rader över relogin, fick %d: %+v", len(rows), rows)
	}
	if logins != 2 {
		t.Errorf("vill ha 2 logins (initial + relogin), fick %d", logins)
	}
}

func TestGetPartsByIds_BatchesAndMaps(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "Inventory/Parts"):
			calls++
			f := r.URL.Query().Get("$filter")
			if !strings.Contains(f, "Id eq ") {
				t.Errorf("$filter saknar 'Id eq': %q", f)
			}
			var parts []string
			for _, tok := range strings.Fields(f) {
				if n, err := strconv.Atoi(tok); err == nil {
					parts = append(parts, fmt.Sprintf(`{"Id":"%d","PartNumber":"P%d"}`, n, n))
				}
			}
			_, _ = fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(parts, ","))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	var ids []ID
	for i := 1; i <= 25; i++ {
		ids = append(ids, ID(i))
	}
	ids = append(ids, 1, 7) // dubletter ska inte ge extra rader/anrop
	m, err := c.GetPartsByIds(context.Background(), ids)
	if err != nil {
		t.Fatalf("GetPartsByIds: %v", err)
	}
	if len(m) != 25 {
		t.Fatalf("vill ha 25 unika parts, fick %d", len(m))
	}
	if calls != 2 { // 25 unika / batch 20 = 2 anrop
		t.Errorf("vill ha 2 batch-anrop, fick %d", calls)
	}
	if m[5].PartNumber != "P5" {
		t.Errorf("part 5 = %+v", m[5])
	}
}

func TestEnumValue_Unmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want EnumValue
	}{
		{`"VariableInspection"`, "VariableInspection"},
		{`3`, "3"},
		{`"None"`, "None"},
		{`null`, ""},
		{`""`, ""},
	}
	for _, tc := range cases {
		var got EnumValue
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Errorf("Unmarshal(%s): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Unmarshal(%s) = %q, vill ha %q", tc.in, got, tc.want)
		}
	}
}

func TestPart_RequiresCert(t *testing.T) {
	cases := []struct {
		rit, tm EnumValue
		want    bool
	}{
		{"None", "", false},
		{"0", "0", false},
		{"", "", false},
		{"Always", "", true},
		{"VariableInspection", "", true},
		{"None", "Batch", true},
		{"None", "2", true},
		{"3", "", true},
	}
	for _, tc := range cases {
		p := Part{ReceivingInspectionType: tc.rit, TraceabilityMode: tc.tm}
		if got := p.RequiresCert(); got != tc.want {
			t.Errorf("RequiresCert(rit=%q tm=%q) = %v, vill ha %v", tc.rit, tc.tm, got, tc.want)
		}
	}
}

// En delivery-row med nästlad PurchaseOrderRow($expand=Part) ska avkoda alla nivåer
// och bevara råbytes på både rad- och artikelnivå (för evidens i UI:t).
func TestDeliveryRow_DecodesNestedAndCapturesRaw(t *testing.T) {
	body := []byte(`{
		"Id": "555",
		"PurchaseOrderId": "100",
		"PurchaseOrderRowId": "11",
		"DeliveryDate": "2026-07-01T00:00:00Z",
		"ArrivedQuantity": 0,
		"ApprovedQuantity": 0,
		"PurchaseOrderRow": {
			"Id": "11",
			"ParentOrderId": "100",
			"PartId": "5",
			"RestQuantity": 10,
			"Part": {
				"Id": "5",
				"PartNumber": "PL-S355-10",
				"Description": "Plåt 10mm",
				"ExtraDescription": "S355J2 +N, cert 3.1",
				"ReceivingInspectionType": "Always",
				"TraceabilityMode": "Batch"
			}
		}
	}`)
	var row PurchaseOrderDeliveryRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.ID != 555 || row.PurchaseOrderId != 100 || row.PurchaseOrderRowId != 11 {
		t.Fatalf("rad-ID:n fel: %+v", row)
	}
	if row.PurchaseOrderRow == nil || row.PurchaseOrderRow.Part == nil {
		t.Fatalf("nästlad PurchaseOrderRow/Part saknas: %+v", row.PurchaseOrderRow)
	}
	part := row.PurchaseOrderRow.Part
	if part.PartNumber != "PL-S355-10" || part.ExtraDescription != "S355J2 +N, cert 3.1" {
		t.Fatalf("artikelfält fel: %+v", part)
	}
	if !part.RequiresCert() {
		t.Errorf("artikeln borde kräva cert (ReceivingInspectionType=Always)")
	}
	if len(row.Raw) == 0 || !strings.Contains(string(row.Raw), "PurchaseOrderRow") {
		t.Errorf("row.Raw inte fångad")
	}
	if len(part.Raw) == 0 || !strings.Contains(string(part.Raw), "ExtraDescription") {
		t.Errorf("part.Raw inte fångad")
	}
}

func TestQuery_Skip(t *testing.T) {
	vals := NewQuery().Skip(20).Top(10).Values()
	if vals.Get("$skip") != "20" {
		t.Errorf("$skip = %q, vill ha 20", vals.Get("$skip"))
	}
	if vals.Get("$top") != "10" {
		t.Errorf("$top = %q, vill ha 10", vals.Get("$top"))
	}
	// Skip 0 ska utelämnas (annars trasslar paginering med skip=0).
	if got := NewQuery().Skip(0).Values().Get("$skip"); got != "" {
		t.Errorf("$skip för Skip(0) = %q, vill ha tomt", got)
	}
}

func TestODataDate(t *testing.T) {
	tm := time.Date(2026, 6, 25, 16, 30, 0, 0, time.UTC)
	if got := odataDate(tm); got != "2026-06-25T16:30:00Z" {
		t.Errorf("odataDate = %q, vill ha 2026-06-25T16:30:00Z", got)
	}
	// Ska normaliseras till UTC oavsett inkommande zon.
	loc := time.FixedZone("CEST", 2*60*60)
	tm2 := time.Date(2026, 6, 25, 18, 30, 0, 0, loc) // = 16:30 UTC
	if got := odataDate(tm2); got != "2026-06-25T16:30:00Z" {
		t.Errorf("odataDate(zon) = %q, vill ha 2026-06-25T16:30:00Z", got)
	}
}
