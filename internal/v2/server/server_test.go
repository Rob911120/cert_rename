package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

func testServer(t *testing.T) (*Server, *http.ServeMux, store.Config) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := store.Config{InboxDir: dir}
	s := New(cfg, db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return s, NewMux(s), cfg
}

func seedCert(t *testing.T, s *Server, cfg store.Config) *domain.Cert {
	t.Helper()
	data := []byte("%PDF-1.4 test\n")
	hash := store.HashPDF(data)
	stored := store.StoredName(hash, "orig.pdf")
	if _, err := store.WriteStoreFile(cfg, stored, data); err != nil {
		t.Fatal(err)
	}
	c := &domain.Cert{
		PdfHash: hash, OriginalFilename: "orig.pdf", StoredName: stored,
		CertType: "3.1", Charge: "43136", Material: "S690QL",
		EnStandardPresent: true, IsEnglish: true, ProductForm: "plåt", Dimensions: "60",
		BNumbers: []string{"B128293"}, Confidence: "high", ReceivedAt: "2026-07-01T08:00:00Z",
	}
	if _, err := s.Repo.InsertCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCertUpdateAndFreezeGuard(t *testing.T) {
	s, mux, cfg := testServer(t)
	c := seedCert(t, s, cfg)

	// Rättelse flyttar det levande namnet
	rec := doJSON(t, mux, "POST", "/api/cert/update",
		map[string]any{"cert_id": fmt.Sprint(c.ID), "field": "material", "value": "S355J2+N"})
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	var cj certJSON
	json.Unmarshal(rec.Body.Bytes(), &cj)
	if cj.ProposedFilename != "43136-plat-60-S355J2+N-B128293.pdf" {
		t.Errorf("proposed = %q", cj.ProposedFilename)
	}

	// Spara → 200
	rec = doJSON(t, mux, "POST", "/api/cert/save", map[string]any{"cert_id": fmt.Sprint(c.ID)})
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}

	// Uppdatering efter spar → 409
	rec = doJSON(t, mux, "POST", "/api/cert/update",
		map[string]any{"cert_id": fmt.Sprint(c.ID), "field": "material", "value": "X"})
	if rec.Code != http.StatusConflict {
		t.Errorf("update efter save: %d, vill ha 409", rec.Code)
	}
}

func TestSaveValidationWarnings422(t *testing.T) {
	s, mux, cfg := testServer(t)
	// Ofullständigt cert (saknar charge/dimensioner)
	data := []byte("%PDF-1.4 ofullstandig\n")
	hash := store.HashPDF(data)
	stored := store.StoredName(hash, "x.pdf")
	if _, err := store.WriteStoreFile(cfg, stored, data); err != nil {
		t.Fatal(err)
	}
	c := &domain.Cert{PdfHash: hash, OriginalFilename: "x.pdf", StoredName: stored,
		CertType: "3.1", Material: "S690QL", EnStandardPresent: true, IsEnglish: true,
		BNumbers: []string{"B1"}, ReceivedAt: "t"}
	if _, err := s.Repo.InsertCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, "POST", "/api/cert/save", map[string]any{"cert_id": fmt.Sprint(c.ID)})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("utan confirm: %d, vill ha 422", rec.Code)
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Warnings) == 0 {
		t.Error("varningslistan saknas i 422-svaret")
	}

	// Spara ändå
	rec = doJSON(t, mux, "POST", "/api/cert/save",
		map[string]any{"cert_id": fmt.Sprint(c.ID), "confirm": true})
	if rec.Code != 200 {
		t.Errorf("med confirm: %d %s", rec.Code, rec.Body)
	}
}

func TestLinkFlow(t *testing.T) {
	s, mux, cfg := testServer(t)
	c := seedCert(t, s, cfg)
	ctx := context.Background()

	if err := s.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 42, OrderNumber: "B127575", PartNumber: "ART-1",
	}, "t"); err != nil {
		t.Fatal(err)
	}

	// Koppla via orderrad
	rec := doJSON(t, mux, "POST", "/api/link",
		map[string]any{"cert_id": fmt.Sprint(c.ID), "delivery_row_id": "42"})
	if rec.Code != 200 {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	var lj linkJSON
	json.Unmarshal(rec.Body.Bytes(), &lj)
	if lj.Status != "bekraftad" || lj.OrderNumber != "B127575" {
		t.Errorf("länk: %+v", lj)
	}

	// Avvisa
	rec = doJSON(t, mux, "POST", "/api/link/reject", map[string]any{"link_id": lj.ID})
	if rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}

	// Okänd länk → 404
	rec = doJSON(t, mux, "POST", "/api/link/reject", map[string]any{"link_id": "99999"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("okänd länk: %d, vill ha 404", rec.Code)
	}
}

// TestCertJSONExtractionFields täcker de 16 nya kolumnkalibrerade
// extraktionsfälten (Task 1-3) i detalj-JSON:en: ett satt-fall (alla fält
// ifyllda, inkl. pekarvärden) och ett tomt-fall (nollvärdes-cert — pekarna
// ska serialiseras som null, inte 0, och impact_energy_j == 0 betyder
// "ej angivet").
func TestCertJSONExtractionFields(t *testing.T) {
	s, _, cfg := testServer(t)
	ctx := context.Background()

	impactTemp, cev, carbon, phos, sulfur, minTemp := -20.0, 0.43, 0.12, 0.01, 0.002, -40.0

	setData := []byte("%PDF-1.4 set\n")
	setHash := store.HashPDF(setData)
	setStored := store.StoredName(setHash, "set.pdf")
	if _, err := store.WriteStoreFile(cfg, setStored, setData); err != nil {
		t.Fatal(err)
	}
	setCert := &domain.Cert{
		PdfHash: setHash, OriginalFilename: "set.pdf", StoredName: setStored,
		CertType: "3.1", Charge: "1", Material: "S355", EnStandardPresent: true, IsEnglish: true,
		BNumbers: []string{"B1"}, ReceivedAt: "t",
		IsLegible: false, IsUnaltered: false,
		NormSystem: "EN 10025-2", ImpactTempC: &impactTemp, ImpactEnergyJ: 27,
		NormEdition: "2019", PedDirective: "2014/68/EU",
		Cev: &cev, CarbonPct: &carbon, PPct: &phos, SPct: &sulfur,
		HasBendTest: true, HasIntergranularTest: true, HasStampPhoto: true,
		MinTemperatureC: &minTemp, DeliveryCondition: "+N",
	}
	setID, err := s.Repo.InsertCert(ctx, setCert)
	if err != nil {
		t.Fatal(err)
	}
	setView, err := s.App.GetCertView(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	scj := s.certJSON(ctx, setView)

	if scj.IsLegible || scj.IsUnaltered {
		t.Errorf("IsLegible/IsUnaltered ska vara false för satt-fallet: %+v", scj)
	}
	if scj.NormSystem != "EN 10025-2" || scj.NormEdition != "2019" || scj.PedDirective != "2014/68/EU" || scj.DeliveryCondition != "+N" {
		t.Errorf("strängfält tappade: %+v", scj)
	}
	if scj.ImpactTempC == nil || *scj.ImpactTempC != -20 {
		t.Errorf("ImpactTempC = %v, vill ha -20", scj.ImpactTempC)
	}
	if scj.ImpactEnergyJ != 27 {
		t.Errorf("ImpactEnergyJ = %v, vill ha 27", scj.ImpactEnergyJ)
	}
	if scj.Cev == nil || *scj.Cev != 0.43 {
		t.Errorf("Cev = %v, vill ha 0.43", scj.Cev)
	}
	if scj.CarbonPct == nil || *scj.CarbonPct != 0.12 || scj.PPct == nil || *scj.PPct != 0.01 || scj.SPct == nil || *scj.SPct != 0.002 {
		t.Errorf("kemifält tappade: %+v", scj)
	}
	if !scj.HasBendTest || !scj.HasIntergranularTest || !scj.HasStampPhoto {
		t.Errorf("Getinge-flaggor tappade: %+v", scj)
	}
	if scj.MinTemperatureC == nil || *scj.MinTemperatureC != -40 {
		t.Errorf("MinTemperatureC = %v, vill ha -40", scj.MinTemperatureC)
	}

	// Tomt cert: pekarfälten ska serialiseras som JSON-null (inte 0/tomt tal).
	nullData := []byte("%PDF-1.4 null\n")
	nullHash := store.HashPDF(nullData)
	nullStored := store.StoredName(nullHash, "null.pdf")
	if _, err := store.WriteStoreFile(cfg, nullStored, nullData); err != nil {
		t.Fatal(err)
	}
	nullCert := &domain.Cert{
		PdfHash: nullHash, OriginalFilename: "null.pdf", StoredName: nullStored,
		CertType: "3.1", Charge: "2", Material: "S355", EnStandardPresent: true, IsEnglish: true,
		BNumbers: []string{"B2"}, ReceivedAt: "t",
	}
	nullID, err := s.Repo.InsertCert(ctx, nullCert)
	if err != nil {
		t.Fatal(err)
	}
	nullView, err := s.App.GetCertView(ctx, nullID)
	if err != nil {
		t.Fatal(err)
	}
	ncj := s.certJSON(ctx, nullView)

	raw, err := json.Marshal(ncj)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"impact_temp_c", "cev", "carbon_pct", "p_pct", "s_pct", "min_temperature_c"} {
		v, ok := m[key]
		if !ok || v != nil {
			t.Errorf("%s ska serialiseras som null för nollvärdes-cert, fick %#v (finns=%v)", key, v, ok)
		}
	}
	if ncj.ImpactEnergyJ != 0 {
		t.Errorf("ImpactEnergyJ = %v, vill ha 0 (ej angivet)", ncj.ImpactEnergyJ)
	}
}

// TestOverviewOrderRowCertBearingFields täcker Task 6:s nya order_rows-fält i
// orderrads-JSON:en (rå kravtext + artikeldata från Monitor, inkl. hyperlinks
// som lista av {link, description}) — bron mellan store och den utfällda
// artikeln i overview.js.
func TestOverviewOrderRowCertBearingFields(t *testing.T) {
	s, mux, _ := testServer(t)
	ctx := context.Background()

	if err := s.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 77, OrderNumber: "B128293", PartNumber: "ART-1",
		ReceivingMessage: "godsmeddelande", ReceivingInspectionInstruction: "mottagningskontroll",
		RowGoodsLabel: "radgodsmärke", RowNotes: "radnotering",
		SupplierDrawingNumber: "SUP-D1", SupplierRevisionNumber: "A", FreeText: "fritext",
		OrderGoodsLabel: "ordergodsmärke", ExternalComment: "extern kommentar",
		BusinessContactOrderNumber: "LEV-9",
		AlloyCode:                  "S690QL", AlloyDescription: "Höghållfast stål",
		PartReceivingInstruction: "mottagningsinstruktion", PartPurchaseComment: "inköpskommentar",
		PartComment: "artikelkommentar",
		PartLength:  6, PartWidth: 2, PartHeight: 0.06, WeightPerUnit: 850,
		GoodsType: "Plåt", CategoryString: "Stål",
		ExtraFieldsRaw: `[{"Type":1}]`,
		Hyperlinks: []domain.Hyperlink{
			{Link: "https://example.com/ritning.pdf", Description: "Ritning A"},
		},
		DrawingNumbers: "D-100, D-101",
	}, "t"); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, "GET", "/api/overview", nil)
	if rec.Code != 200 {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var ov overviewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Orders) != 1 || len(ov.Orders[0].Rows) != 1 {
		t.Fatalf("orders: %+v", ov.Orders)
	}
	rj := ov.Orders[0].Rows[0]
	if rj.ReceivingMessage != "godsmeddelande" || rj.ReceivingInspectionInstruction != "mottagningskontroll" {
		t.Errorf("radkommentarer: %+v", rj)
	}
	if rj.RowGoodsLabel != "radgodsmärke" || rj.RowNotes != "radnotering" ||
		rj.SupplierDrawingNumber != "SUP-D1" || rj.SupplierRevisionNumber != "A" || rj.FreeText != "fritext" {
		t.Errorf("radfält: %+v", rj)
	}
	if rj.OrderGoodsLabel != "ordergodsmärke" || rj.ExternalComment != "extern kommentar" ||
		rj.BusinessContactOrderNumber != "LEV-9" {
		t.Errorf("orderfält: %+v", rj)
	}
	if rj.AlloyCode != "S690QL" || rj.AlloyDescription != "Höghållfast stål" {
		t.Errorf("legering: %+v", rj)
	}
	if rj.PartReceivingInstruction != "mottagningsinstruktion" || rj.PartPurchaseComment != "inköpskommentar" ||
		rj.PartComment != "artikelkommentar" {
		t.Errorf("artikelkommentarer: %+v", rj)
	}
	if rj.PartLength != 6 || rj.PartWidth != 2 || rj.PartHeight != 0.06 || rj.WeightPerUnit != 850 {
		t.Errorf("dimensioner: %+v", rj)
	}
	if rj.GoodsType != "Plåt" || rj.CategoryString != "Stål" {
		t.Errorf("godsslag/kategori: %+v", rj)
	}
	if rj.ExtraFieldsRaw != `[{"Type":1}]` {
		t.Errorf("ExtraFieldsRaw = %q", rj.ExtraFieldsRaw)
	}
	if len(rj.Hyperlinks) != 1 || rj.Hyperlinks[0].Link != "https://example.com/ritning.pdf" || rj.Hyperlinks[0].Description != "Ritning A" {
		t.Errorf("Hyperlinks: %+v", rj.Hyperlinks)
	}
	if rj.DrawingNumbers != "D-100, D-101" {
		t.Errorf("DrawingNumbers = %q", rj.DrawingNumbers)
	}
}

// FIX 11: de regelrätta domarna (engelska/cert-typ/slagseghet) ska trådas rätt i
// overview.go — en rad med parsade krav + ett länkat cert ska via GET /api/overview
// ge english_verdict=mismatch, cert_type_verdict=ok, impact_verdict=ok, och raden
// ska exponera req_*-fälten. Skyddar mot argumentväxling i overview.go:347-349.
func TestOverviewVerdictWiring(t *testing.T) {
	s, mux, cfg := testServer(t)
	ctx := context.Background()

	// Cert: engelska EJ uppfyllt, cert-typ 3.1, slagseghet 27J vid -20°C.
	data := []byte("%PDF-1.4 verdict\n")
	hash := store.HashPDF(data)
	stored := store.StoredName(hash, "verdict.pdf")
	if _, err := store.WriteStoreFile(cfg, stored, data); err != nil {
		t.Fatal(err)
	}
	temp := -20.0
	certID, err := s.Repo.InsertCert(ctx, &domain.Cert{
		PdfHash: hash, OriginalFilename: "verdict.pdf", StoredName: stored,
		CertType: "3.1", Charge: "43136", Material: "S690QL",
		EnStandardPresent: true, IsEnglish: false, ProductForm: "plåt", Dimensions: "60",
		ImpactEnergyJ: 27, ImpactTempC: &temp,
		BNumbers: []string{"B128293"}, Confidence: "high", ReceivedAt: "2026-07-01T08:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Rad + parsade krav: kräver engelska, cert-typ 3.1 och slag 27J/-20°C.
	if err := s.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 55, OrderNumber: "B128293", PartNumber: "ART-1",
		CertRequired: true, DeliveryDate: "2026-07-10",
	}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.App.SetRowRequirements(ctx, 55, domain.RowRequirements{
		CertType: "3.1", English: true, Impact: "27J/-20°C",
	}); err != nil {
		t.Fatal(err)
	}

	// Länka certet till raden (bekräftad) så domarna beräknas vid rendering.
	if _, err := s.App.ConfirmLink(ctx, certID, 55, "B128293", "test"); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, "GET", "/api/overview", nil)
	if rec.Code != 200 {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var ov overviewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Orders) != 1 || len(ov.Orders[0].Rows) != 1 {
		t.Fatalf("orders: %+v", ov.Orders)
	}
	rj := ov.Orders[0].Rows[0]
	if !rj.ReqEnglish || rj.ReqCertType != "3.1" || rj.ReqImpact != "27J/-20°C" {
		t.Errorf("req_*-fält saknas på raden: english=%v cert=%q impact=%q",
			rj.ReqEnglish, rj.ReqCertType, rj.ReqImpact)
	}
	if len(rj.Links) != 1 || rj.Links[0].Cert == nil {
		t.Fatalf("länkat cert saknas på raden: %+v", rj.Links)
	}
	lj := rj.Links[0]
	if lj.EnglishVerdict != "mismatch" {
		t.Errorf("english_verdict = %q, vill ha mismatch", lj.EnglishVerdict)
	}
	if lj.CertTypeVerdict != "ok" {
		t.Errorf("cert_type_verdict = %q, vill ha ok", lj.CertTypeVerdict)
	}
	if lj.ImpactVerdict != "ok" {
		t.Errorf("impact_verdict = %q, vill ha ok", lj.ImpactVerdict)
	}
}

func TestOverviewShape(t *testing.T) {
	s, mux, cfg := testServer(t)
	c := seedCert(t, s, cfg)
	ctx := context.Background()

	if err := s.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 42, OrderNumber: "B128293", PartNumber: "ART-1",
		ExtraDescription: "Plåt t=60 S690QL", DeliveryDate: "2026-07-10",
	}, "t"); err != nil {
		t.Fatal(err)
	}
	// Auto-förslag: certet är okopplat med förslag
	if _, err := s.App.SuggestLinksByBNumber(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, "GET", "/api/overview", nil)
	if rec.Code != 200 {
		t.Fatalf("overview: %d", rec.Code)
	}
	var ov overviewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Orders) != 1 || ov.Orders[0].OrderNumber != "B128293" {
		t.Errorf("orders: %+v", ov.Orders)
	}
	if len(ov.UnlinkedCerts) != 1 || len(ov.UnlinkedCerts[0].Suggestions) != 1 {
		t.Fatalf("unlinked: %+v", ov.UnlinkedCerts)
	}
	if ov.UnlinkedCerts[0].ProposedFilename == "" {
		t.Error("levande namn saknas i okopplat cert")
	}

	// Bekräfta förslaget → certet lämnar okopplade och syns på raden
	sug := ov.UnlinkedCerts[0].Suggestions[0]
	rec = doJSON(t, mux, "POST", "/api/link",
		map[string]any{"cert_id": ov.UnlinkedCerts[0].ID, "delivery_row_id": sug.DeliveryRowID})
	if rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, mux, "GET", "/api/overview", nil)
	json.Unmarshal(rec.Body.Bytes(), &ov)
	if len(ov.UnlinkedCerts) != 0 {
		t.Errorf("cert kvar bland okopplade efter bekräftelse")
	}
	if len(ov.Orders[0].Rows[0].Links) != 1 || ov.Orders[0].Rows[0].Links[0].Cert == nil {
		t.Errorf("länkat cert saknas på raden: %+v", ov.Orders[0].Rows[0].Links)
	}
}

// TestOverviewChangedThrottle skyddar fix B: första ändringen sänds direkt
// (leading edge) och en snabb skur av ändringar därefter slås ihop till EN
// eftersläpande ping — annars skulle en lång sync trigga en full
// /api/overview-refetch per rad och frysa UI:t.
func TestOverviewChangedThrottle(t *testing.T) {
	s, _, _ := testServer(t)

	// Prenumerera direkt på SSE-bussen och räkna overview-pingar.
	ch := make(chan ssEvent, 100)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()

	count := func() int {
		n := 0
		for {
			select {
			case ev := <-ch:
				if ev.Event == "overview" {
					n++
				}
			default:
				return n
			}
		}
	}

	// En skur av 50 ändringar inom fönstret → exakt 1 omedelbar ping (leading).
	for i := 0; i < 50; i++ {
		s.OverviewChanged()
	}
	if got := count(); got != 1 {
		t.Fatalf("leading edge: väntade 1 omedelbar ping, fick %d", got)
	}

	// Efter fönstret ska den eftersläpande, koalescerade pingen ha sänts.
	time.Sleep(overviewThrottle + 250*time.Millisecond)
	if got := count(); got != 1 {
		t.Fatalf("trailing edge: väntade 1 eftersläpande ping, fick %d", got)
	}

	// När allt lugnat sig ska fönstret vara stängt igen (nästa ändring = ny
	// leading edge, inte tyst).
	time.Sleep(overviewThrottle + 250*time.Millisecond)
	s.OverviewChanged()
	if got := count(); got != 1 {
		t.Fatalf("nytt fönster: väntade 1 ny leading-ping, fick %d", got)
	}
}

// TestStaticJSMimeForced skyddar Windows-fixen: <script type="module"> kräver att
// .js serveras med en JavaScript-MIME. På Windows kan registret felaktigt mappa
// .js → text/plain, vilket får webbläsaren att vägra köra modulen och HELA UI:t
// dör. Vi förgiftar MIME-tabellen (simulerar registret) och verifierar att
// servern ÄNDÅ tvingar text/javascript för alla UI-moduler.
func TestStaticJSMimeForced(t *testing.T) {
	// Simulera Windows-registret som mappar .js → text/plain.
	if err := mime.AddExtensionType(".js", "text/plain"); err != nil {
		t.Fatalf("kunde inte förgifta MIME-tabellen: %v", err)
	}

	s, mux, _ := testServer(t)
	_ = s
	for _, path := range []string{"/js/overview.js", "/js/api.js", "/js/shell.js", "/js/sickan.js"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/javascript") {
			t.Errorf("%s: Content-Type = %q, vill ha text/javascript — modulen skulle vägras av webbläsaren", path, ct)
		}
	}

	// CSS ska också tvingas rätt.
	req := httptest.NewRequest("GET", "/app.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("/app.css: Content-Type = %q, vill ha text/css", ct)
	}
}
