package sickan

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cert-renamer/internal/store"
)

// setupAgentToolbox: toolbox med riktig temp-DB (noter/regler/tasks behöver repo).
func setupAgentToolbox(t *testing.T) (*Toolbox, *store.Repository) {
	t.Helper()
	cfg := setupCfg(t)
	db, err := store.InitDB(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	repo := store.NewRepository(db)
	return &Toolbox{Cfg: cfg, N: &stubNotifier{}, Repo: repo}, repo
}

func seedUpcoming(t *testing.T, repo *store.Repository, rows ...store.UpcomingDelivery) {
	t.Helper()
	if err := repo.MergeUpcomingDeliveries(rows); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func Test_Dispatch_UpcomingNotes_RoundTrip(t *testing.T) {
	tb, repo := setupAgentToolbox(t)
	seedUpcoming(t, repo, store.UpcomingDelivery{
		DeliveryRowID: 42, PurchaseOrderID: 1, OrderNumber: "B100", PartNumber: "ART-1",
	})

	if _, err := tb.Dispatch("add_upcoming_note", json.RawMessage(`{"delivery_row_id":"42","text":"ringde 2/7"}`)); err != nil {
		t.Fatalf("add note: %v", err)
	}
	res, err := tb.Dispatch("get_upcoming_notes", json.RawMessage(`{"order_number":"B100"}`))
	if err != nil {
		t.Fatalf("get notes: %v", err)
	}
	if !strings.Contains(res.Summary, "ringde 2/7") || !strings.Contains(res.Summary, `"author":"sickan"`) {
		t.Errorf("noten saknas i svaret: %s", res.Summary)
	}

	// list_upcoming ska bära noterna på raden.
	res, err = tb.Dispatch("list_upcoming", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_upcoming: %v", err)
	}
	if !strings.Contains(res.Summary, "ringde 2/7") {
		t.Errorf("list_upcoming saknar noten: %s", res.Summary)
	}
}

func Test_Dispatch_MarkDelivered(t *testing.T) {
	tb, repo := setupAgentToolbox(t)
	seedUpcoming(t, repo,
		store.UpcomingDelivery{DeliveryRowID: 1, PurchaseOrderID: 1, OrderNumber: "B1"},
		store.UpcomingDelivery{DeliveryRowID: 2, PurchaseOrderID: 1, OrderNumber: "B1"},
	)
	if _, err := tb.Dispatch("mark_delivered", json.RawMessage(`{"delivery_row_ids":["1","2"]}`)); err != nil {
		t.Fatalf("mark_delivered: %v", err)
	}
	for _, id := range []int64{1, 2} {
		got, _ := repo.GetUpcomingByRowID(id)
		if got == nil || got.LocalStatus != store.UpcomingDelivered {
			t.Errorf("rad %d borde vara delivered: %+v", id, got)
		}
	}
}

func Test_Dispatch_ComposeDeviationMail(t *testing.T) {
	tb, repo := setupAgentToolbox(t)
	tb.Cfg.ReportEmail = "daniel@example.com"
	seedUpcoming(t, repo, store.UpcomingDelivery{
		DeliveryRowID: 1, PurchaseOrderID: 1, OrderNumber: "B100",
		SupplierName: "Stål AB", PartNumber: "ART-1", PlannedQty: 5, DeliveryDate: "2026-07-01",
		CertRequired: true, CertStatus: store.CertMissing, RequiredCert: "3.1",
	})
	res, err := tb.Dispatch("compose_deviation_mail", json.RawMessage(`{"order_number":"B100","kind":"cert_missing"}`))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	for _, want := range []string{"B100 (Stål AB)", "cert saknas", "krav: 3.1", "mailto:", "%20"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("utkastet saknar %q: %s", want, res.Summary)
		}
	}

	// Utan adress → fel med hänvisning till inställningen.
	tb.Cfg.ReportEmail = ""
	if _, err := tb.Dispatch("compose_deviation_mail", json.RawMessage(`{"order_number":"B100","kind":"cert_missing"}`)); err == nil {
		t.Errorf("borde kräva avvikelseadress")
	}
}

func Test_Dispatch_RulesAndTasks(t *testing.T) {
	tb, repo := setupAgentToolbox(t)

	if _, err := tb.Dispatch("remember_rule", json.RawMessage(`{"text":"en rename åt gången"}`)); err != nil {
		t.Fatalf("remember_rule: %v", err)
	}
	res, err := tb.Dispatch("list_rules", nil)
	if err != nil {
		t.Fatalf("list_rules: %v", err)
	}
	// Seed-regeln + den nya.
	if !strings.Contains(res.Summary, "en rename åt gången") || !strings.Contains(res.Summary, "dykt upp") {
		t.Errorf("regler saknas: %s", res.Summary)
	}

	if _, err := tb.Dispatch("add_task", json.RawMessage(`{"text":"kolla chargen med Daniel","due_date":"2026-07-03"}`)); err != nil {
		t.Fatalf("add_task: %v", err)
	}
	res, err = tb.Dispatch("list_tasks", nil)
	if err != nil {
		t.Fatalf("list_tasks: %v", err)
	}
	if !strings.Contains(res.Summary, "kolla chargen") {
		t.Errorf("task saknas: %s", res.Summary)
	}
	tasks, _ := repo.ListTasks(true)
	if len(tasks) != 1 {
		t.Fatalf("vill ha 1 öppen task, fick %d", len(tasks))
	}
	if _, err := tb.Dispatch("complete_task", json.RawMessage(`{"id":`+jsonID(tasks[0].ID)+`}`)); err != nil {
		t.Fatalf("complete_task: %v", err)
	}
	if left, _ := repo.ListTasks(true); len(left) != 0 {
		t.Errorf("tasken borde vara avbockad: %+v", left)
	}
}

func jsonID(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

func Test_ReadOnly_BlocksMutatingTools(t *testing.T) {
	tb, repo := setupAgentToolbox(t)
	tb.ReadOnly = true
	seedUpcoming(t, repo, store.UpcomingDelivery{DeliveryRowID: 1, PurchaseOrderID: 1, OrderNumber: "B1"})

	// Muterande verktyg avvisas...
	for _, name := range []string{"mark_delivered", "update_queue_item", "monitor_ui_report_arrival", "complete_task"} {
		if _, err := tb.Dispatch(name, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "läs-läge") {
			t.Errorf("%s borde spärras i läs-läge, fick err=%v", name, err)
		}
	}
	// ...men noter och tasks är tillåtna (förslag, inte handlingar).
	if _, err := tb.Dispatch("add_upcoming_note", json.RawMessage(`{"delivery_row_id":"1","text":"proaktiv not"}`)); err != nil {
		t.Errorf("add_upcoming_note borde vara tillåten i läs-läge: %v", err)
	}
	if _, err := tb.Dispatch("add_task", json.RawMessage(`{"text":"följ upp"}`)); err != nil {
		t.Errorf("add_task borde vara tillåten i läs-läge: %v", err)
	}

	// ToolDefs utelämnar de muterande i läs-läge.
	names := map[string]bool{}
	for _, td := range ToolDefs(true) {
		names[td.OfTool.Name] = true
	}
	for name := range mutatingTools {
		if names[name] {
			t.Errorf("ToolDefs(true) borde utelämna %s", name)
		}
	}
	if !names["list_upcoming"] || !names["add_upcoming_note"] {
		t.Errorf("läsverktygen saknas i ToolDefs(true): %v", names)
	}
	// Fullt läge har alla.
	all := map[string]bool{}
	for _, td := range ToolDefs(false) {
		all[td.OfTool.Name] = true
	}
	for name := range mutatingTools {
		if !all[name] {
			t.Errorf("ToolDefs(false) saknar %s", name)
		}
	}
}
