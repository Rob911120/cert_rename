package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Test_Migrate_AddsMailCategoryToExistingDB verifierar att en databas som
// skapades INNAN mail_category-kolumnen fanns får kolumnen via migrate() —
// utan att tappa befintliga rader. Detta är säkerhetsnätet för Robs riktiga
// cert-renamer.db som redan finns på disk.
func Test_Migrate_AddsMailCategoryToExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Skapa det GAMLA emails-schemat (utan mail_category) + en befintlig rad.
	if _, err := db.Exec(`CREATE TABLE emails (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL,
		subject TEXT,
		from_addr TEXT,
		date TEXT,
		body TEXT,
		status TEXT NOT NULL DEFAULT 'processing',
		processed_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO emails (filename, status, processed_at) VALUES ('gammal.eml', 'completed', '2026-01-01')`); err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	// Kör migrationen — och en gång till för att bevisa idempotens.
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate (andra körningen ska vara no-op): %v", err)
	}

	// Den gamla raden är kvar, med default-kategori "".
	repo := NewRepository(db)
	emails, err := repo.ListEmailsByCategory("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("förväntade 1 bevarad rad efter migration, fick %d", len(emails))
	}
	if emails[0].Filename != "gammal.eml" {
		t.Errorf("filename = %q, vill ha 'gammal.eml'", emails[0].Filename)
	}
	if emails[0].MailCategory != "" {
		t.Errorf("mail_category default = %q, vill ha tom sträng", emails[0].MailCategory)
	}

	// Den nya kolumnen är skrivbar och filtrerbar.
	if err := repo.UpdateEmailCategory(emails[0].ID, "invoice"); err != nil {
		t.Fatalf("update category: %v", err)
	}
	got, err := repo.ListEmailsByCategory("invoice")
	if err != nil {
		t.Fatalf("list invoice: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("förväntade 1 invoice efter update, fick %d", len(got))
	}
}

func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepository(db)
}

func upcomingIDs(list []UpcomingDelivery) map[int64]bool {
	m := map[int64]bool{}
	for _, u := range list {
		m[u.DeliveryRowID] = true
	}
	return m
}

func TestMergeUpcomingDeliveries_Lifecycle(t *testing.T) {
	repo := newTestRepo(t)
	mk := func(id int64, qty float64) UpcomingDelivery {
		return UpcomingDelivery{
			DeliveryRowID: id, PurchaseOrderID: 100, OrderNumber: "B127575",
			PartID: 5, PartNumber: "PL-10", PlannedQty: qty, DeliveryDate: "2026-07-01",
			CertRequired: true, CertStatus: CertMissing, MatchBy: MatchByNone, MaterialOK: MaterialUnknown,
		}
	}

	// Tom DB → 2 infogade.
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{mk(1, 10), mk(2, 20)}); err != nil {
		t.Fatalf("merge1: %v", err)
	}
	if list, _ := repo.ListUpcoming(); len(list) != 2 {
		t.Fatalf("efter merge1 vill ha 2, fick %d", len(list))
	}

	// Ändrad qty → uppdaterad, inte duplicerad.
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{mk(1, 99), mk(2, 20)}); err != nil {
		t.Fatalf("merge2: %v", err)
	}
	if list, _ := repo.ListUpcoming(); len(list) != 2 {
		t.Fatalf("efter merge2 vill ha 2 (ej duplicerad), fick %d", len(list))
	}
	got1, _ := repo.GetUpcomingByRowID(1)
	if got1 == nil || got1.PlannedQty != 99 {
		t.Fatalf("rad 1 qty = %+v, vill ha 99", got1)
	}

	// Operatören markerar rad 1 som levererad.
	if err := repo.MarkUpcomingDelivered(1); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	// Refresh som INTE ser rad 1 (men ser rad 2 + ny rad 3). Kärn-regression:
	// den levererade raden måste överleva hela refreshen.
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{mk(2, 20), mk(3, 30)}); err != nil {
		t.Fatalf("merge3: %v", err)
	}
	list, _ := repo.ListUpcoming()
	ids := upcomingIDs(list)
	if ids[1] {
		t.Errorf("levererad rad 1 ska döljas från listan")
	}
	if !ids[2] || !ids[3] {
		t.Errorf("rad 2/3 saknas efter merge3: %v", ids)
	}
	// Kärn-regression: den levererade raden måste överleva refreshen i tabellen.
	got1, _ = repo.GetUpcomingByRowID(1)
	if got1 == nil || got1.LocalStatus != UpcomingDelivered {
		t.Errorf("rad 1 local_status = %+v, vill ha %q (borde överleva refresh)", got1, UpcomingDelivered)
	}

	// Försvunnen PENDING-rad (3) → borttagen vid nästa refresh; delivered (1) kvar.
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{mk(2, 20)}); err != nil {
		t.Fatalf("merge4: %v", err)
	}
	ids = upcomingIDs(mustList(t, repo))
	if ids[3] {
		t.Errorf("pending rad 3 (ej sedd) borde ha raderats")
	}
	if ids[1] {
		t.Errorf("delivered rad 1 ska döljas från listan")
	}
	if got, _ := repo.GetUpcomingByRowID(1); got == nil {
		t.Errorf("delivered rad 1 borde fortfarande finnas i tabellen")
	}
	if !ids[2] {
		t.Errorf("rad 2 borde finnas")
	}

	// Idempotens: samma merge igen ändrar inte antalet.
	before := mustList(t, repo)
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{mk(2, 20)}); err != nil {
		t.Fatalf("merge5: %v", err)
	}
	after := mustList(t, repo)
	if len(before) != len(after) {
		t.Errorf("idempotens bröts: %d → %d", len(before), len(after))
	}
}

func mustList(t *testing.T, repo *Repository) []UpcomingDelivery {
	t.Helper()
	list, err := repo.ListUpcoming()
	if err != nil {
		t.Fatalf("ListUpcoming: %v", err)
	}
	return list
}

// Tom refresh (Monitor returnerar inget) ska rensa pending men bevara delivered.
func TestMergeUpcomingDeliveries_EmptyRefreshClearsPendingKeepsDelivered(t *testing.T) {
	repo := newTestRepo(t)
	rows := []UpcomingDelivery{
		{DeliveryRowID: 1, OrderNumber: "B1", LocalStatus: UpcomingPending},
		{DeliveryRowID: 2, OrderNumber: "B2", LocalStatus: UpcomingPending},
	}
	if err := repo.MergeUpcomingDeliveries(rows); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if err := repo.MarkUpcomingDelivered(2); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := repo.MergeUpcomingDeliveries(nil); err != nil {
		t.Fatalf("empty merge: %v", err)
	}
	ids := upcomingIDs(mustList(t, repo))
	if ids[1] {
		t.Errorf("pending rad 1 borde raderats av tom refresh")
	}
	if got, _ := repo.GetUpcomingByRowID(2); got == nil {
		t.Errorf("delivered rad 2 borde överleva tom refresh (i tabellen)")
	}
}

func TestListCertificatesMatchingOrder(t *testing.T) {
	repo := newTestRepo(t)
	mkCert := func(hash, fname, bnums string) int64 {
		id, err := repo.InsertCertificate(&Certificate{
			PDFHash: hash, Filename: fname, OriginalFilename: fname,
			CertType: "3.1", Charge: "610042", Material: "S355J2", EnStandardPresent: true,
			BNumbers: bnums, Confidence: "high", ModelUsed: "test",
			Status: "queue", ExtractedAt: "2026-01-01",
		})
		if err != nil {
			t.Fatalf("insert cert: %v", err)
		}
		return id
	}
	mkCert("h1", "f1.pdf", `["B127575"]`)
	id2 := mkCert("h2", "f2.pdf", `["B999999"]`)
	if err := repo.UpdateCertificateCorrection(id2, "corrected_b_numbers", "", `["B127575"]`); err != nil {
		t.Fatalf("correction: %v", err)
	}

	matches, err := repo.ListCertificatesMatchingOrder("B127575")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("vill ha 2 träffar (b_numbers + corrected_b_numbers), fick %d", len(matches))
	}
	if none, _ := repo.ListCertificatesMatchingOrder("B000000"); len(none) != 0 {
		t.Errorf("okänt B-nr borde ge 0 träffar, fick %d", len(none))
	}
	if empty, _ := repo.ListCertificatesMatchingOrder(""); len(empty) != 0 {
		t.Errorf("tomt ordernr borde ge 0 träffar, fick %d", len(empty))
	}
}

func TestAppState_Roundtrip(t *testing.T) {
	repo := newTestRepo(t)
	if v, err := repo.GetAppState("missing"); err != nil || v != "" {
		t.Fatalf("saknad nyckel = (%q, %v), vill ha (\"\", nil)", v, err)
	}
	if err := repo.SetAppState("last_upcoming_run", "2026-06-25T16:30:00Z"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := repo.SetAppState("last_upcoming_run", "2026-06-26T16:30:00Z"); err != nil {
		t.Fatalf("set (upsert): %v", err)
	}
	v, err := repo.GetAppState("last_upcoming_run")
	if err != nil || v != "2026-06-26T16:30:00Z" {
		t.Errorf("get = (%q, %v)", v, err)
	}
}

// En gammal DB utan upcoming_deliveries/app_state ska få tabellerna via InitDB
// utan att tappa befintlig data.
func Test_InitDB_AddsUpcomingTablesToOldDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Gammalt schema: bara emails (utan mail_category) + en rad.
	if _, err := db.Exec(`CREATE TABLE emails (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL,
		subject TEXT, from_addr TEXT, date TEXT, body TEXT,
		status TEXT NOT NULL DEFAULT 'processing',
		processed_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO emails (filename, status, processed_at) VALUES ('gammal.eml','completed','2026-01-01')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	// Öppna via InitDB → nya tabeller skapas, gamla data bevaras.
	db2, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB på gammal DB: %v", err)
	}
	defer db2.Close()
	repo := NewRepository(db2)

	emails, err := repo.ListEmailsByCategory("")
	if err != nil || len(emails) != 1 || emails[0].Filename != "gammal.eml" {
		t.Fatalf("gammal emails-rad tappades: %+v err=%v", emails, err)
	}
	// Nya tabeller är användbara.
	if err := repo.MergeUpcomingDeliveries([]UpcomingDelivery{{DeliveryRowID: 1, OrderNumber: "B1"}}); err != nil {
		t.Fatalf("upcoming_deliveries saknas efter migration: %v", err)
	}
	if err := repo.SetAppState("k", "v"); err != nil {
		t.Fatalf("app_state saknas efter migration: %v", err)
	}
}

func TestUpcomingClassificationCache_Roundtrip(t *testing.T) {
	repo := newTestRepo(t)
	if got, err := repo.GetUpcomingClassification("k"); err != nil || got != nil {
		t.Fatalf("cache-miss = (%v, %v), vill ha (nil, nil)", got, err)
	}
	want := UpcomingClassificationCache{
		RequiredMaterial: "S355J2", RequiredCert: "3.1", OurMaterial: "S275JR",
		MaterialOK: MaterialMismatch, Notes: "fel sort",
	}
	if err := repo.SaveUpcomingClassification("k", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Upsert ändrar domen.
	want.MaterialOK = MaterialOK
	if err := repo.SaveUpcomingClassification("k", want); err != nil {
		t.Fatalf("save (upsert): %v", err)
	}
	got, err := repo.GetUpcomingClassification("k")
	if err != nil || got == nil {
		t.Fatalf("get = (%v, %v)", got, err)
	}
	if got.MaterialOK != MaterialOK || got.RequiredMaterial != "S355J2" {
		t.Errorf("cache = %+v", got)
	}
}

func TestConfig_NormalizeUpcoming(t *testing.T) {
	// Tom tid + 0 dagar → defaults.
	c := Config{}
	c.NormalizeUpcoming()
	if c.UpcomingTime != DefaultUpcomingTime || c.UpcomingWindowDays != DefaultUpcomingWindowDays {
		t.Errorf("defaults fel: %+v", c)
	}
	if c.UpcomingBackDays != DefaultUpcomingBackDays {
		t.Errorf("back-days default fel: %d, vill ha %d", c.UpcomingBackDays, DefaultUpcomingBackDays)
	}
	// Giltigt back-värde behålls.
	c = Config{UpcomingBackDays: 90}
	c.NormalizeUpcoming()
	if c.UpcomingBackDays != 90 {
		t.Errorf("giltigt back-värde borde behållas, fick %d", c.UpcomingBackDays)
	}
	// Ogiltig tid → default.
	c = Config{UpcomingTime: "25:99", UpcomingWindowDays: 7}
	c.NormalizeUpcoming()
	if c.UpcomingTime != DefaultUpcomingTime {
		t.Errorf("ogiltig tid borde bli default, fick %q", c.UpcomingTime)
	}
	if c.UpcomingWindowDays != 7 {
		t.Errorf("giltigt window borde behållas, fick %d", c.UpcomingWindowDays)
	}
	// Giltig tid behålls.
	c = Config{UpcomingTime: "08:15", UpcomingWindowDays: 30}
	c.NormalizeUpcoming()
	if c.UpcomingTime != "08:15" {
		t.Errorf("giltig tid borde behållas, fick %q", c.UpcomingTime)
	}
}
