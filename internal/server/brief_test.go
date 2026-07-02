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

	// Ombyggd brief samma dag bevarar avfärdandet.
	s.generateBrief()
	if b2 := s.loadBrief(); b2 == nil || len(b2.Dismissed) != 1 {
		t.Errorf("dismissed borde överleva ombyggnad: %+v", b2)
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
