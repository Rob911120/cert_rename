package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cert-renamer/internal/store"
)

func TestBuildBrief_Sections(t *testing.T) {
	today := "2026-07-02"
	rows := []store.UpcomingDelivery{
		// Väntas idag.
		{DeliveryRowID: 1, PurchaseOrderID: 10, OrderNumber: "B1", PartNumber: "A", DeliveryDate: today},
		// Försenad (kom inte).
		{DeliveryRowID: 2, PurchaseOrderID: 20, OrderNumber: "B2", PartNumber: "B", DeliveryDate: "2026-06-30",
			RowNotes: []store.UpcomingNote{{Text: "ringde 1/7"}}},
		// Försenad men levererad → varken idag eller försenad.
		{DeliveryRowID: 3, PurchaseOrderID: 20, OrderNumber: "B2", PartNumber: "C", DeliveryDate: "2026-06-30", LocalStatus: store.UpcomingDelivered},
		// Framtida med cert saknas.
		{DeliveryRowID: 4, PurchaseOrderID: 30, OrderNumber: "B3", PartNumber: "D", DeliveryDate: "2026-07-10",
			CertStatus: store.CertMissing, RequiredCert: "3.1"},
	}
	tasks := []store.Task{{ID: 1, Text: "kolla chargen", Status: store.TaskOpen}}

	b := buildBrief(rows, tasks, today)

	if len(b.ExpectedToday) != 1 || b.ExpectedToday[0].OrderNumber != "B1" {
		t.Errorf("expected_today: %+v", b.ExpectedToday)
	}
	if len(b.Overdue) != 1 || b.Overdue[0].OrderNumber != "B2" || b.Overdue[0].LastNote != "ringde 1/7" {
		t.Errorf("overdue: %+v", b.Overdue)
	}
	if len(b.CertMissing) != 1 || b.CertMissing[0].RequiredCert != "3.1" {
		t.Errorf("cert_missing: %+v", b.CertMissing)
	}
	// B2 har både levererad och pending rad → delleverans.
	if len(b.PartialOrders) != 1 || b.PartialOrders[0] != "B2" {
		t.Errorf("partial_orders: %+v", b.PartialOrders)
	}
	if len(b.Tasks) != 1 || b.Tasks[0].Text != "kolla chargen" {
		t.Errorf("tasks: %+v", b.Tasks)
	}
}

func TestBriefEndpoint_GeneratesAndDismisses(t *testing.T) {
	s, _ := newGateTestServer(t)
	seedRow(t, s, store.UpcomingDelivery{
		DeliveryRowID: 7, PurchaseOrderID: 1, OrderNumber: "B7", PartNumber: "X",
		DeliveryDate: "2020-01-01", // garanterat försenad
	})

	// GET bygger dagens brief on-demand.
	req := httptest.NewRequest(http.MethodGet, "/api/brief", nil)
	rec := httptest.NewRecorder()
	s.handleBrief(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/brief = %d (%s)", rec.Code, rec.Body.String())
	}
	var b BriefData
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(b.Overdue) != 1 {
		t.Fatalf("overdue: %+v", b.Overdue)
	}

	// Avfärda med anledning → nyckeln sparas + noten skrivs på raden.
	body := strings.NewReader(`{"key":"overdue:7","reason":"leverantören mailade, kommer imorgon"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/brief/dismiss", body)
	rec = httptest.NewRecorder()
	s.handleBriefDismiss(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d (%s)", rec.Code, rec.Body.String())
	}
	var after BriefData
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if len(after.Dismissed) != 1 || after.Dismissed[0] != "overdue:7" {
		t.Errorf("dismissed: %+v", after.Dismissed)
	}
	notes, _ := s.repo.ListUpcomingNotes()
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "kommer imorgon") {
		t.Errorf("avfärdande-noten saknas: %+v", notes)
	}

	// Även expected-nycklar accepteras (avfärdanden i Väntas idag).
	body = strings.NewReader(`{"key":"expected:7"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/brief/dismiss", body)
	rec = httptest.NewRecorder()
	s.handleBriefDismiss(rec, req)
	var after2 BriefData
	_ = json.Unmarshal(rec.Body.Bytes(), &after2)
	if len(after2.Dismissed) != 2 || after2.Dismissed[1] != "expected:7" {
		t.Errorf("expected-dismiss: %+v", after2.Dismissed)
	}

	// Ombyggd brief samma dag bevarar avfärdandena.
	s.generateBrief()
	if b2 := s.loadBrief(); b2 == nil || len(b2.Dismissed) != 2 {
		t.Errorf("dismissed borde överleva ombyggnad: %+v", b2)
	}
}

// Briefens rader ska bära hela detaljbilden (charge, material, B-nr, noter …)
// så Översikt är självförsörjande — men INTE de råa Monitor-blobbarna, som
// skulle svälla app_state i onödan.
func TestBriefRow_CarriesDetailAndStripsRaw(t *testing.T) {
	r := store.UpcomingDelivery{
		DeliveryRowID: 42, OrderNumber: "B9", SupplierName: "Stål AB",
		PartNumber: "RS4711", Description: "Plåt varmvalsad", Dimensions: "10x2000",
		PlannedQty: 12, DeliveryDate: "2026-07-02",
		CertStatus: store.CertMatched, CertFilename: "b9-cert.pdf",
		RequiredMaterial: "S355", CertMaterial: "S355J2", MaterialOK: "ok",
		CertCharge: "CH123", CertBNumbers: "B9",
		EvidenceJSON: `{"part_number":"RS4711"}`,
		DeliveryRaw:  `{"huge":"monitor-blob"}`, PartRaw: `{"huge":"monitor-blob"}`,
		RowNotes: []store.UpcomingNote{{Text: "första"}, {Text: "senaste"}},
	}

	br := briefRow(r)

	if br.CertCharge != "CH123" || br.MaterialOK != "ok" || br.CertBNumbers != "B9" ||
		br.Dimensions != "10x2000" || br.CertFilename != "b9-cert.pdf" {
		t.Errorf("detaljfält saknas: %+v", br)
	}
	if br.EvidenceJSON == "" {
		t.Error("evidence_json ska behållas (UI:ts diagnostik läser den)")
	}
	if br.DeliveryRaw != "" || br.PartRaw != "" {
		t.Error("delivery_raw/part_raw ska nollas")
	}
	if br.LastNote != "senaste" || br.NoteCount != 2 {
		t.Errorf("last_note/note_count: %q/%d", br.LastNote, br.NoteCount)
	}
	raw, _ := json.Marshal(br)
	for _, want := range []string{`"planned_qty":12`, `"supplier_name":"Stål AB"`, `"row_notes"`, `"delivery_row_id":"42"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON saknar %s i: %s", want, raw)
		}
	}
}

// Sickans kommentar-prompt ska få en SLIM projektion av briefen — inte de
// rika raderna (token-bloat) — men last_note måste med (prompten refererar den).
func TestBriefPromptView_Slim(t *testing.T) {
	rows := []store.UpcomingDelivery{{
		DeliveryRowID: 1, OrderNumber: "B1", PartNumber: "A", DeliveryDate: "2026-07-01",
		EvidenceJSON: `{"x":1}`, DeliveryRaw: `{"huge":"blob"}`,
		RowNotes:     []store.UpcomingNote{{Text: "ringde"}},
	}}
	b := buildBrief(rows, nil, "2026-07-02")

	raw, err := json.Marshal(briefPromptView(&b))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"last_note":"ringde"`) {
		t.Errorf("projektionen ska bära last_note: %s", s)
	}
	if strings.Contains(s, "evidence_json") || strings.Contains(s, "delivery_raw") || strings.Contains(s, "row_notes") {
		t.Errorf("projektionen ska vara slim: %s", s)
	}
}

func TestBriefComment_Gating(t *testing.T) {
	s, _ := newGateTestServer(t)

	// Utan API-nyckel → 400 (kommentaren är ett AI-tillägg, stommen bär).
	req := httptest.NewRequest(http.MethodPost, "/api/brief/comment", nil)
	rec := httptest.NewRecorder()
	s.handleBriefComment(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("utan API-nyckel = %d (%s)", rec.Code, rec.Body.String())
	}

	// Pågående körning → 409.
	s.cfg.ApiKey = "sk-test"
	s.briefCommenting.Store(true)
	rec = httptest.NewRecorder()
	s.handleBriefComment(rec, httptest.NewRequest(http.MethodPost, "/api/brief/comment", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("pågående körning = %d (%s)", rec.Code, rec.Body.String())
	}
}
