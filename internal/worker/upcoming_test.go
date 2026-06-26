package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// soonDate är ett leveransdatum några dagar fram (YYYY-MM-DD) — alltid inom
// standardfönstret (14 dagar) räknat från time.Now(), så stub-rader hamnar i
// fönstret oavsett när testet körs. (GetUpcomingOrderRows avgränsar fönstret
// klientsidan på DeliveryDate.)
func soonDate() string { return time.Now().AddDate(0, 0, 3).Format("2006-01-02") }

func TestShouldCatchUp(t *testing.T) {
	cfg := store.Config{UpcomingTime: "16:30"}
	loc := time.UTC
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, loc) }
	cases := []struct {
		name string
		last time.Time
		now  time.Time
		want bool
	}{
		{"före måltid → nej", at(2026, 6, 25, 8, 0), at(2026, 6, 25, 16, 0), false},
		{"strax efter måltid, ej körd idag → ja", at(2026, 6, 24, 16, 30), at(2026, 6, 25, 16, 31), true},
		{"avstängd över måltiden, uppstart kväll → ja", at(2026, 6, 24, 12, 0), at(2026, 6, 25, 18, 0), true},
		{"körd idag efter måltid → nej", at(2026, 6, 25, 16, 30), at(2026, 6, 25, 17, 0), false},
		{"aldrig körd, efter måltid → ja", time.Time{}, at(2026, 6, 25, 16, 31), true},
		{"aldrig körd, före måltid → nej", time.Time{}, at(2026, 6, 25, 9, 0), false},
	}
	for _, tc := range cases {
		if got := ShouldCatchUp(tc.last, tc.now, cfg); got != tc.want {
			t.Errorf("%s: ShouldCatchUp = %v, vill ha %v", tc.name, got, tc.want)
		}
	}
}

func TestNextRun(t *testing.T) {
	cfg := store.Config{UpcomingTime: "16:30"}
	loc := time.UTC
	// Före måltid → idag 16:30.
	now := time.Date(2026, 6, 25, 9, 0, 0, 0, loc)
	if got := NextRun(now, cfg); !got.Equal(time.Date(2026, 6, 25, 16, 30, 0, 0, loc)) {
		t.Errorf("före: NextRun = %v", got)
	}
	// Efter måltid → imorgon 16:30.
	now = time.Date(2026, 6, 25, 17, 0, 0, 0, loc)
	if got := NextRun(now, cfg); !got.Equal(time.Date(2026, 6, 26, 16, 30, 0, 0, loc)) {
		t.Errorf("efter: NextRun = %v", got)
	}
	// DST-skifte (Europe/Stockholm vårskifte 2026-03-29 02:00→03:00): 16:30 oförändrat.
	if sthlm, err := time.LoadLocation("Europe/Stockholm"); err == nil {
		now = time.Date(2026, 3, 29, 9, 0, 0, 0, sthlm)
		want := time.Date(2026, 3, 29, 16, 30, 0, 0, sthlm)
		if got := NextRun(now, cfg); !got.Equal(want) {
			t.Errorf("DST: NextRun = %v, vill ha %v", got, want)
		}
		if ShouldCatchUp(time.Date(2026, 3, 28, 16, 30, 0, 0, sthlm), time.Date(2026, 3, 29, 17, 0, 0, 0, sthlm), cfg) != true {
			t.Errorf("DST: ShouldCatchUp borde vara true över skiftet")
		}
	}
}

// stubUpcomingMonitor spelar Monitor för RefreshUpcoming-orkestreringen: en
// delivery-rad med nästlad Part (kräver cert), order B127575 + leverantör.
func stubUpcomingMonitor(t *testing.T) *monitor.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			if r.URL.Query().Get("$skip") != "" && r.URL.Query().Get("$skip") != "0" {
				_, _ = w.Write([]byte(`{"value":[]}`)) // sida 2: slut → paginering stannar
				return
			}
			_, _ = fmt.Fprintf(w, `{"value":[{
				"Id":"11","ParentOrderId":"100","PartId":"5","OrderRowType":1,"RestQuantity":10,
				"DeliveryDate":%q,
				"Part":{"Id":"5","PartNumber":"PL-S355-10","Description":"Plåt 10mm",
					"ExtraDescription":"S355J2 +N, cert 3.1","ReceivingInspectionType":"Always"}
			}]}`, soonDate())
		case strings.Contains(r.URL.Path, "Purchase/PurchaseOrders"):
			_, _ = w.Write([]byte(`{"value":[{"Id":"100","OrderNumber":"B127575","BusinessContactId":"7"}]}`))
		case strings.Contains(r.URL.Path, "Purchase/Suppliers"):
			_, _ = w.Write([]byte(`{"value":[{"Id":"7","Name":"BE Group","SupplierCode":"BV"}]}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	mc := monitor.New(srv.URL)
	if err := mc.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("login: %v", err)
	}
	return mc
}

func newUpcomingRepo(t *testing.T) *store.Repository {
	t.Helper()
	db, err := store.InitDB(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewRepository(db)
}

// Cert saknas i repot → cert_status=missing, material_ok=unknown. Verifierar
// rad→Part-join, ordernr/leverantör-hämtning och evidens. Ingen API-nyckel → ingen AI.
func TestRefreshUpcoming_NoCert(t *testing.T) {
	mc := stubUpcomingMonitor(t)
	repo := newUpcomingRepo(t)
	n := &testNotifier{repo: repo, monErr: errNoMonitorInTest}

	count, err := RefreshUpcoming(context.Background(), mc, repo, store.Config{UpcomingWindowDays: 14}, n)
	if err != nil {
		t.Fatalf("RefreshUpcoming: %v", err)
	}
	if count != 1 {
		t.Fatalf("vill ha 1 rad, fick %d", count)
	}
	list, _ := repo.ListUpcoming()
	if len(list) != 1 {
		t.Fatalf("vill ha 1 i DB, fick %d", len(list))
	}
	u := list[0]
	if u.DeliveryRowID != 11 || u.OrderNumber != "B127575" || u.SupplierName != "BE Group" {
		t.Errorf("rad fel: %+v", u)
	}
	if u.PartNumber != "PL-S355-10" || u.DeliveryDate != soonDate() || u.PlannedQty != 10 {
		t.Errorf("part/datum/qty fel: %+v", u)
	}
	if !u.CertRequired {
		t.Errorf("cert_required borde vara true (ReceivingInspectionType=Always)")
	}
	if u.CertStatus != store.CertMissing {
		t.Errorf("cert_status = %q, vill ha missing", u.CertStatus)
	}
	if u.MaterialOK != store.MaterialUnknown {
		t.Errorf("material_ok = %q, vill ha unknown", u.MaterialOK)
	}
	if !strings.Contains(u.EvidenceJSON, "S355J2") || u.DeliveryRaw == "" {
		t.Errorf("evidens/rådata saknas: evidence=%q rawLen=%d", u.EvidenceJSON, len(u.DeliveryRaw))
	}
}

// Rad utan inline Part → artikeln batch-hämtas via GetPartsByIds, och
// cert_required härleds från den hämtade artikeln.
func TestRefreshUpcoming_FallbackPartFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"s1"}`))
		case strings.Contains(r.URL.Path, "PurchaseOrderRows"):
			if r.URL.Query().Get("$skip") != "" && r.URL.Query().Get("$skip") != "0" {
				_, _ = w.Write([]byte(`{"value":[]}`))
				return
			}
			// Ingen inline Part — bara PartId.
			_, _ = fmt.Fprintf(w, `{"value":[{
				"Id":"12","ParentOrderId":"100","PartId":"5","OrderRowType":1,"RestQuantity":3,
				"DeliveryDate":%q
			}]}`, soonDate())
		case strings.Contains(r.URL.Path, "Purchase/PurchaseOrders"):
			_, _ = w.Write([]byte(`{"value":[{"Id":"100","OrderNumber":"B1","BusinessContactId":"7"}]}`))
		case strings.Contains(r.URL.Path, "Purchase/Suppliers"):
			_, _ = w.Write([]byte(`{"value":[{"Id":"7","Name":"Lev"}]}`))
		case strings.Contains(r.URL.Path, "Inventory/Parts"):
			if f := r.URL.Query().Get("$filter"); !strings.Contains(f, "Id eq 5") {
				t.Errorf("Parts $filter = %q, saknar 'Id eq 5'", f)
			}
			_, _ = w.Write([]byte(`{"value":[{"Id":"5","PartNumber":"PL-FETCH","ReceivingInspectionType":"Always"}]}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	mc := monitor.New(srv.URL)
	if err := mc.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("login: %v", err)
	}
	repo := newUpcomingRepo(t)
	n := &testNotifier{repo: repo, monErr: errNoMonitorInTest}

	if _, err := RefreshUpcoming(context.Background(), mc, repo, store.Config{UpcomingWindowDays: 14}, n); err != nil {
		t.Fatalf("RefreshUpcoming: %v", err)
	}
	list, _ := repo.ListUpcoming()
	if len(list) != 1 {
		t.Fatalf("vill ha 1 rad, fick %d", len(list))
	}
	u := list[0]
	if u.PartNumber != "PL-FETCH" {
		t.Errorf("part_number = %q, vill ha PL-FETCH (batch-hämtad)", u.PartNumber)
	}
	if !u.CertRequired {
		t.Errorf("cert_required borde härledas från batch-hämtad artikel")
	}
}

// Cert finns (B-nummer i b_numbers) → cert_status=matched, match_by=b_number,
// our_material från certet. Utan API-nyckel görs ingen AI-dom (material_ok=unknown).
func TestRefreshUpcoming_CertMatchedNoAI(t *testing.T) {
	mc := stubUpcomingMonitor(t)
	repo := newUpcomingRepo(t)
	n := &testNotifier{repo: repo, monErr: errNoMonitorInTest}

	if _, err := repo.InsertCertificate(&store.Certificate{
		PDFHash: "h1", Filename: "82908-plat-10-S355-B127575.pdf", OriginalFilename: "o.pdf",
		CertType: "3.1", Charge: "610042", Material: "S355J2+N", MaterialShort: "S355",
		Dimensions: "10", BNumbers: `["B127575"]`, Confidence: "high", ModelUsed: "test",
		Status: "queue", ExtractedAt: "2026-01-01",
	}); err != nil {
		t.Fatalf("insert cert: %v", err)
	}

	if _, err := RefreshUpcoming(context.Background(), mc, repo, store.Config{UpcomingWindowDays: 14}, n); err != nil {
		t.Fatalf("RefreshUpcoming: %v", err)
	}
	list, _ := repo.ListUpcoming()
	u := list[0]
	if u.CertStatus != store.CertMatched {
		t.Errorf("cert_status = %q, vill ha matched", u.CertStatus)
	}
	if u.MatchBy != store.MatchByBNumber {
		t.Errorf("match_by = %q, vill ha b_number", u.MatchBy)
	}
	if u.CertFilename != "82908-plat-10-S355-B127575.pdf" {
		t.Errorf("cert_filename = %q", u.CertFilename)
	}
	if u.OurMaterial != "S355J2+N" {
		t.Errorf("our_material = %q, vill ha S355J2+N", u.OurMaterial)
	}
	if u.MaterialOK != store.MaterialUnknown {
		t.Errorf("material_ok = %q, vill ha unknown (ingen AI-nyckel)", u.MaterialOK)
	}
}
