package store

import (
	"path/filepath"
	"testing"
)

func TestUpcomingNotes_AttachAndSurviveRefresh(t *testing.T) {
	repo := newTestRepo(t)
	rows := []UpcomingDelivery{
		{DeliveryRowID: 1, PurchaseOrderID: 10, OrderNumber: "B1", PartNumber: "A"},
		{DeliveryRowID: 2, PurchaseOrderID: 10, OrderNumber: "B1", PartNumber: "B"},
	}
	if err := repo.MergeUpcomingDeliveries(rows); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := repo.AddUpcomingNote(1, "rob", "ringde 2/7"); err != nil {
		t.Fatalf("add note: %v", err)
	}

	list := mustList(t, repo)
	var found bool
	for _, r := range list {
		if r.DeliveryRowID == 1 {
			if len(r.RowNotes) != 1 || r.RowNotes[0].Text != "ringde 2/7" || r.RowNotes[0].OrderNumber != "B1" {
				t.Errorf("noten fel på raden: %+v", r.RowNotes)
			}
			found = true
		} else if len(r.RowNotes) != 0 {
			t.Errorf("rad %d ska inte ha noter: %+v", r.DeliveryRowID, r.RowNotes)
		}
	}
	if !found {
		t.Fatalf("rad 1 saknas")
	}

	// Noten överlever en refresh där raden inte sågs (stale-delete tar raden,
	// noten ligger kvar i sin egen tabell).
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{rows[1]}); err != nil {
		t.Fatalf("merge2: %v", err)
	}
	notes, err := repo.ListUpcomingNotes()
	if err != nil || len(notes) != 1 {
		t.Errorf("noten borde överleva refresh: %v %v", notes, err)
	}
}

func TestAgentRules_SeedOnceAndDisable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewRepository(db)
	rules, err := repo.ListAgentRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("seed-regeln saknas: %v %v", rules, err)
	}

	// Radera regeln, öppna om databasen — seeden ska INTE återuppstå.
	if err := repo.DisableAgentRule(rules[0].ID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	db.Close()
	db2, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB 2: %v", err)
	}
	defer db2.Close()
	repo2 := NewRepository(db2)
	if rules, _ := repo2.ListAgentRules(); len(rules) != 0 {
		t.Errorf("raderad seed-regel återuppstod: %+v", rules)
	}
}

func TestTasks_SortAndComplete(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.AddTask(Task{Text: "utan datum", Source: "rob"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := repo.AddTask(Task{Text: "fredag", DueDate: "2026-07-03", Source: "sickan"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	tasks, err := repo.ListTasks(true)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("vill ha 2 öppna: %v %v", tasks, err)
	}
	if tasks[0].Text != "fredag" {
		t.Errorf("task med datum ska sorteras först: %+v", tasks)
	}
	if err := repo.CompleteTask(tasks[0].ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	open, _ := repo.ListTasks(true)
	if len(open) != 1 || open[0].Text != "utan datum" {
		t.Errorf("fel öppna tasks: %+v", open)
	}
	all, _ := repo.ListTasks(false)
	if len(all) != 2 {
		t.Errorf("alla tasks ska finnas kvar: %+v", all)
	}
}

func TestBuildDeviationMail_Rest(t *testing.T) {
	rows := []UpcomingDelivery{
		{DeliveryRowID: 1, OrderNumber: "B9", SupplierName: "Stål AB", PartNumber: "A", LocalStatus: UpcomingDelivered},
		{DeliveryRowID: 2, OrderNumber: "B9", SupplierName: "Stål AB", PartNumber: "B", PlannedQty: 3, DeliveryDate: "2026-07-01", LocalStatus: UpcomingPending},
	}
	mail, err := BuildDeviationMail(rows, MailKindRest, "d@x.se")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if mail.Subject != "Order B9 (Stål AB) — ej inlevererade positioner" {
		t.Errorf("subject = %q", mail.Subject)
	}
	if len(mail.Positions) != 1 || mail.Positions[0] != "- B, 3 st, lev.datum 2026-07-01" {
		t.Errorf("positions = %+v", mail.Positions)
	}
	if _, err := BuildDeviationMail(rows, MailKindRest, ""); err == nil {
		t.Errorf("borde kräva adress")
	}
}
