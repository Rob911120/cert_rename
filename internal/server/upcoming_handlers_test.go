package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cert-renamer/internal/store"
)

type driveCall struct {
	routine string
	order   string
	save    bool
}

func newGateTestServer(t *testing.T) (*Server, *[]driveCall) {
	t.Helper()
	db, err := store.InitDB(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{repo: store.NewRepository(db)}
	var calls []driveCall
	s.driveRoutine = func(routine, order string, save bool) error {
		calls = append(calls, driveCall{routine, order, save})
		return nil
	}
	return s, &calls
}

func seedRow(t *testing.T, s *Server, row store.UpcomingDelivery) {
	t.Helper()
	if err := s.repo.MergeUpcomingDeliveries([]store.UpcomingDelivery{row}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func postDeliverIn(t *testing.T, s *Server, rowID int64, confirm bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"delivery_row_id": rowID, "confirm": confirm})
	req := httptest.NewRequest(http.MethodPost, "/api/upcoming/deliver-in", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleUpcomingDeliverIn(rec, req)
	return rec
}

// Materialavvikelse blockerar helt — även med confirm anropas inte Monitor.
func TestDeliverIn_MismatchBlocked(t *testing.T) {
	s, calls := newGateTestServer(t)
	seedRow(t, s, store.UpcomingDelivery{
		DeliveryRowID: 1, OrderNumber: "B127575", PurchaseOrderRowID: 11, PartID: 5,
		MaterialOK: store.MaterialMismatch,
	})
	rec := postDeliverIn(t, s, 1, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, vill ha 409", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("DriveMonitorRoutine anropades trots mismatch: %+v", *calls)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["blocked"] != true {
		t.Errorf("svar saknar blocked=true: %v", resp)
	}
}

// Exakt match utan confirm → förhandsvisning, ingen körning.
func TestDeliverIn_RequiresConfirm(t *testing.T) {
	s, calls := newGateTestServer(t)
	seedRow(t, s, store.UpcomingDelivery{
		DeliveryRowID: 2, OrderNumber: "B127575", PurchaseOrderRowID: 11, PartID: 5,
		MaterialOK: store.MaterialOK,
	})
	rec := postDeliverIn(t, s, 2, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, vill ha 200 (preview)", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("DriveMonitorRoutine anropades utan confirm: %+v", *calls)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["needs_confirm"] != true {
		t.Errorf("svar saknar needs_confirm: %v", resp)
	}
}

// Exakt match + confirm + auto-spara AV → rutinen körs men save=false (inget Ctrl+S).
func TestDeliverIn_ConfirmAutoSaveOff(t *testing.T) {
	s, calls := newGateTestServer(t)
	s.cfg.MonitorUIAutoSave = false
	seedRow(t, s, store.UpcomingDelivery{
		DeliveryRowID: 3, OrderNumber: "B127575", PurchaseOrderRowID: 11, PartID: 5, PlannedQty: 10,
		MaterialOK: store.MaterialOK,
	})
	rec := postDeliverIn(t, s, 3, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, vill ha 200", rec.Code)
	}
	if len(*calls) != 1 {
		t.Fatalf("vill ha 1 DriveMonitorRoutine-anrop, fick %d", len(*calls))
	}
	c := (*calls)[0]
	if c.routine != "report_arrival" || c.order != "B127575" {
		t.Errorf("anrop = %+v", c)
	}
	if c.save {
		t.Errorf("save = true trots att auto-spara är av (inget Ctrl+S ska skickas)")
	}
}

// Auto-spara PÅ → save=true skickas vidare.
func TestDeliverIn_ConfirmAutoSaveOn(t *testing.T) {
	s, calls := newGateTestServer(t)
	s.cfg.MonitorUIAutoSave = true
	seedRow(t, s, store.UpcomingDelivery{
		DeliveryRowID: 4, OrderNumber: "B127575", PurchaseOrderRowID: 11, MaterialOK: store.MaterialOK,
	})
	rec := postDeliverIn(t, s, 4, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(*calls) != 1 || !(*calls)[0].save {
		t.Errorf("vill ha 1 anrop med save=true, fick %+v", *calls)
	}
}

// Okänd rad → 404.
func TestDeliverIn_NotFound(t *testing.T) {
	s, calls := newGateTestServer(t)
	rec := postDeliverIn(t, s, 999, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, vill ha 404", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("DriveMonitorRoutine anropades för okänd rad")
	}
}

// mark-delivered markerar raden och överlever en efterföljande refresh-merge.
func TestMarkDelivered_Endpoint(t *testing.T) {
	s, _ := newGateTestServer(t)
	seedRow(t, s, store.UpcomingDelivery{DeliveryRowID: 7, OrderNumber: "B1"})
	body, _ := json.Marshal(map[string]any{"delivery_row_id": 7})
	req := httptest.NewRequest(http.MethodPost, "/api/upcoming/mark-delivered", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleUpcomingMarkDelivered(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, vill ha 204", rec.Code)
	}
	got, _ := s.repo.GetUpcomingByRowID(7)
	if got == nil || got.LocalStatus != store.UpcomingDelivered {
		t.Errorf("local_status = %+v, vill ha delivered", got)
	}
}

// /run svarar 202 (asynkron kick).
func TestUpcomingRun_Accepts(t *testing.T) {
	s, _ := newGateTestServer(t)
	s.refreshKick = make(chan struct{}, 1)
	req := httptest.NewRequest(http.MethodPost, "/api/upcoming/run", nil)
	rec := httptest.NewRecorder()
	s.handleUpcomingRun(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, vill ha 202", rec.Code)
	}
}
