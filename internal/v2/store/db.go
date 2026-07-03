// Package store är V2:s persistens: SQLite-schema med numrerade migrationer,
// dum CRUD-repository och filhantering (certlager + utmapp). Ingen affärslogik
// här — den bor i internal/v2/domain och internal/v2/app.
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"

	v1store "cert-renamer/internal/store"

	_ "modernc.org/sqlite"
)

// migrations är den inbäddade, numrerade migrationslistan. PRAGMA user_version
// pekar på senast applicerade migration (1-baserat). Lägg ALLTID nya steg sist —
// redigera aldrig ett redan utrullat steg. Varje steg körs i en egen transaktion.
var migrations = []string{
	// 001 — grundschema. Tre kärntabeller (order_rows, certs, links) + aux.
	// Databasen är sanningen: certens PDF ligger orörd i certlagret och döps
	// om först vid Spara. Se plan §2.
	`
-- Kärntabell 1: Monitors inköpsorderrader + artikeldata, förstklassigt
-- persisterade. Ingen stale-DELETE: sync sätter in_monitor=0 för osedda rader
-- så att länkar överlever tills certet sparats.
CREATE TABLE order_rows (
    delivery_row_id   INTEGER PRIMARY KEY,
    purchase_order_id INTEGER NOT NULL,
    order_number      TEXT NOT NULL,
    supplier_name     TEXT NOT NULL DEFAULT '',
    part_id           INTEGER NOT NULL,
    part_number       TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    extra_description TEXT NOT NULL DEFAULT '',
    planned_qty       REAL NOT NULL DEFAULT 0,
    delivery_date     TEXT NOT NULL DEFAULT '',
    cert_required     INTEGER NOT NULL DEFAULT 0,
    delivery_raw      TEXT NOT NULL DEFAULT '',
    part_raw          TEXT NOT NULL DEFAULT '',
    delivered         INTEGER NOT NULL DEFAULT 0,
    in_monitor        INTEGER NOT NULL DEFAULT 1,
    first_seen        TEXT NOT NULL,
    last_seen         TEXT NOT NULL
);
CREATE INDEX idx_order_rows_order ON order_rows(order_number);
CREATE INDEX idx_order_rows_date  ON order_rows(delivery_date);

-- Kärntabell 2: cert med rå extraktion (skrivs aldrig över), rättelser
-- (effective-value-mönstret: '' = ej rättad), levande namn-override och
-- livscykel. PDF:en lagras en gång under stored_name och döps aldrig om.
CREATE TABLE certs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    pdf_hash            TEXT NOT NULL UNIQUE,
    original_filename   TEXT NOT NULL,
    stored_name         TEXT NOT NULL,
    email_subject       TEXT NOT NULL DEFAULT '',
    email_from          TEXT NOT NULL DEFAULT '',
    email_date          TEXT NOT NULL DEFAULT '',
    cert_type           TEXT NOT NULL DEFAULT '',
    charge              TEXT NOT NULL DEFAULT '',
    material            TEXT NOT NULL DEFAULT '',
    en_standard_present INTEGER NOT NULL DEFAULT 0,
    is_english          INTEGER NOT NULL DEFAULT 1,
    product_form        TEXT NOT NULL DEFAULT '',
    dimensions          TEXT NOT NULL DEFAULT '',
    country_of_origin   TEXT NOT NULL DEFAULT '',
    b_numbers           TEXT NOT NULL DEFAULT '[]',
    confidence          TEXT NOT NULL DEFAULT '',
    issues              TEXT NOT NULL DEFAULT '[]',
    model_used          TEXT NOT NULL DEFAULT '',
    tokens_input        INTEGER NOT NULL DEFAULT 0,
    tokens_output       INTEGER NOT NULL DEFAULT 0,
    processing_ms       INTEGER NOT NULL DEFAULT 0,
    corrected_charge        TEXT NOT NULL DEFAULT '',
    corrected_material      TEXT NOT NULL DEFAULT '',
    corrected_product_form  TEXT NOT NULL DEFAULT '',
    corrected_dimensions    TEXT NOT NULL DEFAULT '',
    corrected_cert_type     TEXT NOT NULL DEFAULT '',
    corrected_b_numbers     TEXT NOT NULL DEFAULT '',
    correction_log          TEXT NOT NULL DEFAULT '[]',
    name_override       TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'mottagen',
    final_filename      TEXT NOT NULL DEFAULT '',
    output_path         TEXT NOT NULL DEFAULT '',
    saved_at            TEXT NOT NULL DEFAULT '',
    received_at         TEXT NOT NULL
);
CREATE INDEX idx_certs_status ON certs(status);

-- Kärntabell 3: cert ↔ orderrad, arbetsläget. order_number denormaliserat för
-- visning och för fri koppling till B-nummer utanför Monitor-fönstret.
CREATE TABLE links (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    cert_id         INTEGER NOT NULL REFERENCES certs(id),
    delivery_row_id INTEGER NOT NULL DEFAULT 0,
    order_number    TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'foreslagen',
    match_source    TEXT NOT NULL DEFAULT '',
    required_material     TEXT NOT NULL DEFAULT '',
    required_cert         TEXT NOT NULL DEFAULT '',
    our_material          TEXT NOT NULL DEFAULT '',
    material_ok           TEXT NOT NULL DEFAULT 'unknown',
    required_product_form TEXT NOT NULL DEFAULT '',
    product_form_ok       TEXT NOT NULL DEFAULT 'unknown',
    ai_notes              TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(cert_id, delivery_row_id, order_number)
);
CREATE INDEX idx_links_cert ON links(cert_id);
CREATE INDEX idx_links_row  ON links(delivery_row_id);

-- Intags-audit (slimmad V1 emails).
CREATE TABLE emails (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    filename      TEXT NOT NULL,
    subject       TEXT NOT NULL DEFAULT '',
    from_addr     TEXT NOT NULL DEFAULT '',
    date          TEXT NOT NULL DEFAULT '',
    mail_category TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'processing',
    error_message TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);

-- AI-parbedömningscache. Nyckeln är breddad mot V1 (inkluderar effektiva
-- dimensioner/form m.m.) så att rättelser invaliderar cachen.
CREATE TABLE ai_match_cache (
    cache_key             TEXT PRIMARY KEY,
    required_material     TEXT NOT NULL DEFAULT '',
    required_cert         TEXT NOT NULL DEFAULT '',
    our_material          TEXT NOT NULL DEFAULT '',
    material_ok           TEXT NOT NULL DEFAULT 'unknown',
    required_product_form TEXT NOT NULL DEFAULT '',
    product_form_ok       TEXT NOT NULL DEFAULT 'unknown',
    notes                 TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL
);

-- Generaliserade noteringar: kind = 'order_row' | 'cert'.
CREATE TABLE notes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL,
    ref_id       INTEGER NOT NULL,
    order_number TEXT NOT NULL DEFAULT '',
    part_number  TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL,
    text         TEXT NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_notes_ref ON notes(kind, ref_id);

-- Sickans delade minne (1:1 från V1).
CREATE TABLE agent_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT NOT NULL,
    source     TEXT NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
CREATE TABLE tasks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    text         TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'open',
    due_date     TEXT NOT NULL DEFAULT '',
    order_number TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT 'rob',
    created_at   TEXT NOT NULL,
    done_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tasks_status ON tasks(status);

-- Enad AI-kostnads-/auditlogg.
CREATE TABLE ai_calls (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    cert_id               INTEGER NOT NULL DEFAULT 0,
    step                  TEXT NOT NULL,
    model                 TEXT NOT NULL,
    tokens_input          INTEGER NOT NULL DEFAULT 0,
    tokens_output         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_creation INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read     INTEGER NOT NULL DEFAULT 0,
    duration_ms           INTEGER NOT NULL DEFAULT 0,
    success               INTEGER NOT NULL DEFAULT 1,
    error_message         TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL
);

-- Nyckel/värde: last_sync, seed-markörer, morgonkommentar m.m.
CREATE TABLE app_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`,
}

// Open öppnar (eller skapar) V2-databasen och applicerar väntande migrationer.
func Open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: en skrivare åt gången
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := seedAgentRules(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate kör alla migrationer med index >= user_version, var och en i egen
// transaktion, och stämplar user_version efteråt. Idempotent och framåt-enkel:
// nya steg läggs sist i migrations.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("läsa user_version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("databasen har user_version=%d men binären känner bara till %d migrationer — nyare app-version krävs", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %03d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %03d (stämpel): %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %03d (commit): %w", i+1, err)
		}
	}
	return nil
}

// seedAgentRules lägger in grundregeln exakt en gång per databas (markör i
// app_state) — raderar användaren regeln ska den inte återuppstå.
func seedAgentRules(db *sql.DB) error {
	var done int
	if err := db.QueryRow(`SELECT count(*) FROM app_state WHERE key = 'agent_rules_seeded'`).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if _, err := db.Exec(`INSERT INTO agent_rules (text, source, created_at) VALUES (?, 'seed', datetime('now'))`,
		"Skriv inte om/jaga inte cert som saknas förrän leveransen faktiskt har dykt upp (godset kommer ofta före certet)."); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO app_state (key, value) VALUES ('agent_rules_seeded', '1')`)
	return err
}

// DBPath returnerar V2-databasens sökväg (bredvid config.json, egen fil —
// V1:s cert-renamer.db röres aldrig).
func DBPath() string {
	return filepath.Join(filepath.Dir(v1store.ConfigPath()), "cert-renamer-v2.db")
}
