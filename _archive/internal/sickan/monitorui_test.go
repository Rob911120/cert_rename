package sickan

import (
	"encoding/json"
	"strings"
	"testing"

	"cert-renamer/internal/store"
)

func uiToolbox(autoSave bool) (*Toolbox, *stubNotifier) {
	sn := &stubNotifier{}
	tb := &Toolbox{
		N:   sn,
		Cfg: store.Config{MonitorUIAutoSave: autoSave},
	}
	return tb, sn
}

func TestMonitorUIReportArrival_PreviewDoesNotDrive(t *testing.T) {
	tb, sn := uiToolbox(false)
	out, err := tb.Dispatch("monitor_ui_report_arrival",
		json.RawMessage(`{"order_number":"B128756"}`))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if sn.driveCalls != 0 {
		t.Fatalf("klienten styrdes under förhandsvisning! anrop=%d", sn.driveCalls)
	}
	if !strings.Contains(out.Summary, "FÖRSLAG") {
		t.Errorf("förväntade förhandsvisning, fick: %s", out.Summary)
	}
}

func TestMonitorUIReportArrival_ConfirmDrives(t *testing.T) {
	tb, sn := uiToolbox(false)
	if _, err := tb.Dispatch("monitor_ui_report_arrival",
		json.RawMessage(`{"order_number":"B128756","confirm":true}`)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if sn.driveCalls != 1 {
		t.Fatalf("förväntade exakt 1 styrning, fick %d", sn.driveCalls)
	}
	if sn.lastRoutine != "report_arrival" || sn.lastOrder != "B128756" {
		t.Errorf("fel argument: routine=%q order=%q", sn.lastRoutine, sn.lastOrder)
	}
	if sn.lastSave {
		t.Error("save ska vara false när det inte begärts")
	}
}

func TestMonitorUIReportArrival_InspectionRoutine(t *testing.T) {
	tb, sn := uiToolbox(false)
	if _, err := tb.Dispatch("monitor_ui_report_arrival",
		json.RawMessage(`{"order_number":"B1","routine":"inspection","confirm":true}`)); err != nil {
		t.Fatalf("inspection: %v", err)
	}
	if sn.driveCalls != 1 || sn.lastRoutine != "inspection" {
		t.Errorf("förväntade styrning av inspection, fick anrop=%d routine=%q", sn.driveCalls, sn.lastRoutine)
	}
}
