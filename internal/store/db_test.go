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
