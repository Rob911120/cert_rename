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
		Expand("Rows").
		OrderBy("Id desc").
		Top(5).
		Values()
	if vals.Get("$filter") != "ChargeNumber eq '1'" {
		t.Errorf("$filter = %q", vals.Get("$filter"))
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
// $expand=Part.
func TestGetUpcomingOrderRows_PaginatesViaNextLink(t *testing.T) {
	var srv *httptest.Server
	var hits int
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			hits++
			if hits == 1 {
				// Sida 1 bär queryn — datum får INTE ligga i $filter (Monitor 400:ar
				// på datumliteraler); bara RestQuantity gt 0 + $expand=Part.
				f := r.URL.Query().Get("$filter")
				if !strings.Contains(f, "RestQuantity gt 0") {
					t.Errorf("$filter %q saknar 'RestQuantity gt 0'", f)
				}
				if strings.Contains(f, "DeliveryDate") {
					t.Errorf("$filter %q innehåller DeliveryDate — datum ska filtreras klientsidan", f)
				}
				if exp := r.URL.Query().Get("$expand"); !strings.Contains(exp, "Part") {
					t.Errorf("$expand = %q, saknar Part", exp)
				}
				next := srv.URL + "/sv/001.1/api/v1/Purchase/PurchaseOrderRows?%24skip=2"
				_, _ = fmt.Fprintf(w, `{"value":[{"Id":"1","DeliveryDate":"2026-06-26"},{"Id":"2","DeliveryDate":"2026-06-26"}],"@odata.nextLink":%q}`, next)
			} else {
				// Sida 2 via nextLink (bär bara $skip i den här stubben).
				_, _ = w.Write([]byte(`{"value":[{"Id":"3","DeliveryDate":"2026-06-26"}]}`))
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
	rows, _, err := c.GetUpcomingOrderRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingOrderRows: %v", err)
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
func TestGetUpcomingOrderRows_SkipUntilEmptyDespiteServerCap(t *testing.T) {
	all := []string{
		`{"Id":"1","DeliveryDate":"2026-06-26"}`, `{"Id":"2","DeliveryDate":"2026-06-26"}`,
		`{"Id":"3","DeliveryDate":"2026-06-26"}`, `{"Id":"4","DeliveryDate":"2026-06-26"}`,
		`{"Id":"5","DeliveryDate":"2026-06-26"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
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
	rows, _, err := c.GetUpcomingOrderRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingOrderRows: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("vill ha 5 rader trots sidtak 2, fick %d", len(rows))
	}
}

// En 401 mitt i pagineringen ska trigga relogin och fortsätta utan att tappa rader.
func TestGetUpcomingOrderRows_ReloginMidPagination(t *testing.T) {
	var srv *httptest.Server
	var logins, hits int
	var session string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			logins++
			session = "s" + strconv.Itoa(logins)
			_, _ = w.Write([]byte(`{"SessionId":"` + session + `"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			hits++
			switch hits {
			case 1:
				next := srv.URL + "/sv/001.1/api/v1/Purchase/PurchaseOrderRows?%24skip=2"
				_, _ = fmt.Fprintf(w, `{"value":[{"Id":"1","DeliveryDate":"2026-06-26"},{"Id":"2","DeliveryDate":"2026-06-26"}],"@odata.nextLink":%q}`, next)
			case 2:
				http.Error(w, "session expired", http.StatusUnauthorized) // utgången mitt i pagineringen
			default:
				if got := r.Header.Get(SessionHeader); got != session {
					t.Errorf("retry använde fel session: %q != %q", got, session)
				}
				_, _ = w.Write([]byte(`{"value":[{"Id":"3","DeliveryDate":"2026-06-26"}]}`))
			}
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	rows, _, err := c.GetUpcomingOrderRows(context.Background(), from, from.AddDate(0, 0, 14))
	if err != nil {
		t.Fatalf("GetUpcomingOrderRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("vill ha 3 rader över relogin, fick %d: %+v", len(rows), rows)
	}
	if logins != 2 {
		t.Errorf("vill ha 2 logins (initial + relogin), fick %d", logins)
	}
}

// Datumfönstret filtreras klientsidan: bara rader inom [from,to] returneras, men
// stats rapporterar HELA hämtade mängden + datumspann (för diagnostik-loggen).
func TestGetUpcomingOrderRows_WindowFilterAndStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			if r.URL.Query().Get("$skip") != "" && r.URL.Query().Get("$skip") != "0" {
				_, _ = w.Write([]byte(`{"value":[]}`))
				return
			}
			// Id 1 i fönstret, Id 2 efter fönstret, Id 3 utan datum.
			_, _ = w.Write([]byte(`{"value":[
				{"Id":"1","PartId":"5","RestQuantity":1,"DeliveryDate":"2026-06-26T00:00:00+02:00"},
				{"Id":"2","PartId":"5","RestQuantity":1,"DeliveryDate":"2026-07-20"},
				{"Id":"3","PartId":"5","RestQuantity":1,"DeliveryDate":""}
			]}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	rows, stats, err := c.GetUpcomingOrderRows(context.Background(), from, from.AddDate(0, 0, 14)) // → 2026-07-09
	if err != nil {
		t.Fatalf("GetUpcomingOrderRows: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("vill ha bara Id 1 i fönstret, fick %+v", rows)
	}
	if stats.Fetched != 3 {
		t.Errorf("stats.Fetched = %d, vill ha 3 (hela hämtade mängden)", stats.Fetched)
	}
	if stats.MinDate != "2026-06-26" || stats.MaxDate != "2026-07-20" {
		t.Errorf("datumspann = %s–%s, vill ha 2026-06-26–2026-07-20", stats.MinDate, stats.MaxDate)
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
			// Parts ska expandera de cert-bärande navigeringarna (stålsort,
			// kommentarer, hyperlänkar, ritningar).
			exp := r.URL.Query().Get("$expand")
			for _, want := range []string{"CurrentAlloy", "ReceivingInstruction", "HyperLinks", "Drawings"} {
				if !strings.Contains(exp, want) {
					t.Errorf("Parts $expand %q saknar %q", exp, want)
				}
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
	// Doc-verifierade enum-värden (Inventory.Part.html):
	// ReceivingInspectionType {None:0, Always:1, VariableInspection:2}
	// TraceabilityMode {None:0, Batch:1, Individual:2, IndividualOnlyWithdrawal:4}
	cases := []struct {
		rit, tm EnumValue
		want    bool
	}{
		{"None", "", false},
		{"0", "0", false},
		{"", "", false},
		{"None", "None", false},
		{"Always", "", true},
		{"1", "", true}, // Always som tal
		{"VariableInspection", "", true},
		{"2", "None", true}, // VariableInspection som tal
		{"None", "Batch", true},
		{"None", "1", true}, // Batch som tal
		{"None", "2", true}, // Individual
		{"None", "Individual", true},
		{"None", "IndividualOnlyWithdrawal", true}, // spårbarhet aktiv
		{"None", "4", true},                        // IndividualOnlyWithdrawal som tal
		{"3", "", true},                            // okänt framtida RIT-värde ⇒ försiktig ja
	}
	for _, tc := range cases {
		p := Part{ReceivingInspectionType: tc.rit, TraceabilityMode: tc.tm}
		if got := p.RequiresCert(); got != tc.want {
			t.Errorf("RequiresCert(rit=%q tm=%q) = %v, vill ha %v", tc.rit, tc.tm, got, tc.want)
		}
	}
}

// En orderrad med inline $expand=Part ska avkoda båda nivåer och bevara råbytes på
// både rad- och artikelnivå (för evidens i UI:t).
func TestOrderRow_DecodesInlinePartAndCapturesRaw(t *testing.T) {
	body := []byte(`{
		"Id": "11",
		"ParentOrderId": "100",
		"PartId": "5",
		"OrderRowType": 1,
		"DeliveryDate": "2026-07-01T00:00:00Z",
		"RestQuantity": 10,
		"Part": {
			"Id": "5",
			"PartNumber": "PL-S355-10",
			"Description": "Plåt 10mm",
			"ExtraDescription": "S355J2 +N, cert 3.1",
			"ReceivingInspectionType": "Always",
			"TraceabilityMode": "Batch"
		}
	}`)
	var row PurchaseOrderRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.ID != 11 || row.ParentOrderId != 100 || row.PartId != 5 || row.RestQuantity != 10 {
		t.Fatalf("rad-fält fel: %+v", row)
	}
	if row.Part == nil {
		t.Fatalf("inline Part saknas: %+v", row)
	}
	part := row.Part
	if part.PartNumber != "PL-S355-10" || part.ExtraDescription != "S355J2 +N, cert 3.1" {
		t.Fatalf("artikelfält fel: %+v", part)
	}
	if !part.RequiresCert() {
		t.Errorf("artikeln borde kräva cert (ReceivingInspectionType=Always)")
	}
	if len(row.Raw) == 0 || !strings.Contains(string(row.Raw), "RestQuantity") {
		t.Errorf("row.Raw inte fångad")
	}
	if len(part.Raw) == 0 || !strings.Contains(string(part.Raw), "ExtraDescription") {
		t.Errorf("part.Raw inte fångad")
	}
}

// Radnivåns nya cert-bärande fält: expanderade Comment-referenser (RawText) plus
// skalära godsmärke/notering/leverantörsritning + FreeText (OrderRowType=4).
// Doc: Purchase.PurchaseOrderRow.html + Common.Comment.html.
func TestOrderRow_DecodesCommentRefsAndRowFields(t *testing.T) {
	body := []byte(`{
		"Id": "21",
		"ParentOrderId": "100",
		"PartId": "5",
		"OrderRowType": 4,
		"RowsGoodsLabel": "MARK-42",
		"RowNotes": "Ankomstkontroll kravs",
		"SupplierDrawingNumber": "SD-999",
		"SupplierRevisionNumber": "B",
		"FreeText": "Fri text rad",
		"ReceivingMessage": {"Id": "7", "RawText": "Medskickas: cert 3.1"},
		"ReceivingInspectionInstruction": {"Id": "8", "RawText": "Kontrollera charge"}
	}`)
	var row PurchaseOrderRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.RowsGoodsLabel != "MARK-42" || row.RowNotes != "Ankomstkontroll kravs" {
		t.Errorf("godsmarke/notering fel: %+v", row)
	}
	if row.SupplierDrawingNumber != "SD-999" || row.SupplierRevisionNumber != "B" {
		t.Errorf("leverantorsritning fel: %+v", row)
	}
	if row.FreeText != "Fri text rad" {
		t.Errorf("FreeText = %q", row.FreeText)
	}
	if row.ReceivingMessage == nil || row.ReceivingMessage.RawText != "Medskickas: cert 3.1" {
		t.Errorf("ReceivingMessage fel: %+v", row.ReceivingMessage)
	}
	if row.ReceivingInspectionInstruction == nil || row.ReceivingInspectionInstruction.RawText != "Kontrollera charge" {
		t.Errorf("ReceivingInspectionInstruction fel: %+v", row.ReceivingInspectionInstruction)
	}
}

// Artikelns nya fält: CurrentAlloy (stålsort), Comment-referenser, dimensioner
// (meter/kg), godsslag/kategori, HyperLinks, Drawings, ExtraFields (rått).
// Doc: Inventory.Part.html + Inventory.Alloy/HyperLink, Manufacturing.Drawing,
// Common.Comment/ExtraField.
func TestPart_DecodesAlloyDimensionsLinksDrawings(t *testing.T) {
	body := []byte(`{
		"Id": "5",
		"PartNumber": "PL-S355-10",
		"Length": 6.0,
		"Width": 1.5,
		"Height": 0.01,
		"WeightPerUnit": 78.5,
		"GoodsType": "Stalplat",
		"CategoryString": "RAMATERIAL",
		"CurrentAlloy": {"Id": "3", "Code": "S355J2", "Description": "Konstruktionsstal"},
		"ReceivingInstruction": {"Id": "9", "RawText": "Mat tjocklek"},
		"PurchaseComment": {"Id": "10", "RawText": "Kop bara med cert"},
		"Comment": {"Id": "11", "RawText": "Allman notis"},
		"HyperLinks": [
			{"Id": "1", "Link": "https://ex.se/cert.pdf", "Description": "Cert"},
			{"Id": "2", "Link": "https://ex.se/ritning", "Description": "Ritning"}
		],
		"Drawings": [{"Id": "4", "DrawingNumber": "RIT-1001"}],
		"ExtraFields": [{"Id": "12", "Identifier": "CERTKRAV", "StringValue": "3.1"}]
	}`)
	var p Part
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.CurrentAlloy == nil || p.CurrentAlloy.Code != "S355J2" || p.CurrentAlloy.Description != "Konstruktionsstal" {
		t.Errorf("CurrentAlloy fel: %+v", p.CurrentAlloy)
	}
	if p.Length != 6.0 || p.Width != 1.5 || p.Height != 0.01 || p.WeightPerUnit != 78.5 {
		t.Errorf("dimensioner fel: L=%v W=%v H=%v vikt=%v", p.Length, p.Width, p.Height, p.WeightPerUnit)
	}
	if p.GoodsType != "Stalplat" || p.CategoryString != "RAMATERIAL" {
		t.Errorf("godsslag/kategori fel: %q / %q", p.GoodsType, p.CategoryString)
	}
	if p.ReceivingInstruction == nil || p.ReceivingInstruction.RawText != "Mat tjocklek" {
		t.Errorf("ReceivingInstruction fel: %+v", p.ReceivingInstruction)
	}
	if p.PurchaseComment == nil || p.PurchaseComment.RawText != "Kop bara med cert" {
		t.Errorf("PurchaseComment fel: %+v", p.PurchaseComment)
	}
	if p.Comment == nil || p.Comment.RawText != "Allman notis" {
		t.Errorf("Comment fel: %+v", p.Comment)
	}
	if len(p.HyperLinks) != 2 || p.HyperLinks[0].Link != "https://ex.se/cert.pdf" || p.HyperLinks[0].Description != "Cert" {
		t.Errorf("HyperLinks fel: %+v", p.HyperLinks)
	}
	if len(p.Drawings) != 1 || p.Drawings[0].DrawingNumber != "RIT-1001" {
		t.Errorf("Drawings fel: %+v", p.Drawings)
	}
	// ExtraFields sparas rått — hela arrayen ska finnas kvar för senare inventering.
	if len(p.ExtraFields) == 0 || !strings.Contains(string(p.ExtraFields), "CERTKRAV") {
		t.Errorf("ExtraFields inte fangade ratt: %s", p.ExtraFields)
	}
	// Raw ska fortfarande fångas (bakåtkompatibilitet).
	if len(p.Raw) == 0 || !strings.Contains(string(p.Raw), "CurrentAlloy") {
		t.Errorf("Part.Raw inte fangad")
	}
}

// GetUpcomingOrderRows ska expandera radnivåns Comment-referenser samt Part med
// nästlad expand av dess cert-navigeringar (stålsort, kommentarer, länkar, ritningar).
func TestGetUpcomingOrderRows_ExpandsCertNavigations(t *testing.T) {
	var gotExpand string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			gotExpand = r.URL.Query().Get("$expand")
			_, _ = w.Write([]byte(`{"value":[]}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	from := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	if _, _, err := c.GetUpcomingOrderRows(context.Background(), from, from.AddDate(0, 0, 14)); err != nil {
		t.Fatalf("GetUpcomingOrderRows: %v", err)
	}
	for _, want := range []string{
		"ReceivingMessage", "ReceivingInspectionInstruction",
		"Part($expand=", "CurrentAlloy", "HyperLinks", "Drawings",
		"ReceivingInstruction", "PurchaseComment",
	} {
		if !strings.Contains(gotExpand, want) {
			t.Errorf("$expand %q saknar %q", gotExpand, want)
		}
	}
}

// GetPurchaseOrder ska expandera ExternalComment och avkoda dess RawText samt de
// nya skalära orderfälten (GoodsLabel, BusinessContactOrderNumber).
func TestGetPurchaseOrder_ExpandsExternalComment(t *testing.T) {
	var gotExpand string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrders"):
			gotExpand = r.URL.Query().Get("$expand")
			_, _ = w.Write([]byte(`{"value":[{"Id":1,"OrderNumber":"PO-1","GoodsLabel":"GL-7","BusinessContactOrderNumber":"BCN-9","ExternalComment":{"Id":5,"RawText":"Extern notis"}}]}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Login(context.Background(), "kalle", "hemligt")
	po, err := c.GetPurchaseOrder(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetPurchaseOrder: %v", err)
	}
	if !strings.Contains(gotExpand, "ExternalComment") {
		t.Errorf("$expand %q saknar ExternalComment", gotExpand)
	}
	if po == nil || po.GoodsLabel != "GL-7" || po.BusinessContactOrderNumber != "BCN-9" {
		t.Fatalf("order-falt fel: %+v", po)
	}
	if po.ExternalComment == nil || po.ExternalComment.RawText != "Extern notis" {
		t.Errorf("ExternalComment fel: %+v", po.ExternalComment)
	}
}

func TestQuery_Skip(t *testing.T) {
	// Paginering sätter skip-fältet direkt (som client.go gör).
	q := NewQuery().Top(10)
	q.skip = 20
	vals := q.Values()
	if vals.Get("$skip") != "20" {
		t.Errorf("$skip = %q, vill ha 20", vals.Get("$skip"))
	}
	if vals.Get("$top") != "10" {
		t.Errorf("$top = %q, vill ha 10", vals.Get("$top"))
	}
	// Skip 0 ska utelämnas (annars trasslar paginering med skip=0).
	if got := NewQuery().Values().Get("$skip"); got != "" {
		t.Errorf("$skip för skip=0 = %q, vill ha tomt", got)
	}
}
