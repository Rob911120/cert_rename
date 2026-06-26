package store

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const dbSchema = `
CREATE TABLE IF NOT EXISTS emails (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL,
    subject TEXT,
    from_addr TEXT,
    date TEXT,
    body TEXT,
    status TEXT NOT NULL DEFAULT 'processing',
    mail_category TEXT NOT NULL DEFAULT '',
    processed_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS certificates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER REFERENCES emails(id),
    pdf_hash TEXT NOT NULL UNIQUE,
    filename TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    cert_type TEXT NOT NULL,
    charge TEXT NOT NULL,
    material TEXT NOT NULL,
    material_short TEXT NOT NULL,
    product_form TEXT,
    dimensions TEXT,
    b_numbers TEXT,
    confidence TEXT NOT NULL,
    issues TEXT,
    model_used TEXT NOT NULL,
    tokens_input INTEGER,
    tokens_output INTEGER,
    processing_ms INTEGER,
    status TEXT NOT NULL DEFAULT 'queue',
    extracted_at TEXT NOT NULL,
    human_corrected BOOLEAN DEFAULT FALSE,
    corrected_charge TEXT,
    corrected_material TEXT,
    corrected_material_short TEXT,
    corrected_product_form TEXT,
    corrected_dimensions TEXT,
    corrected_b_numbers TEXT,
    correction_notes TEXT,
    correction_log TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS ai_decisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER REFERENCES emails(id),
    certificate_id INTEGER REFERENCES certificates(id),
    step TEXT NOT NULL,
    model TEXT NOT NULL,
    tokens_input INTEGER,
    tokens_output INTEGER,
    tokens_cache_creation INTEGER,
    tokens_cache_read INTEGER,
    duration_ms INTEGER,
    success BOOLEAN NOT NULL,
    error_message TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS cost_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    certificate_id INTEGER REFERENCES certificates(id),
    model TEXT NOT NULL,
    tokens_input INTEGER NOT NULL,
    tokens_output INTEGER NOT NULL,
    tokens_cache_creation INTEGER NOT NULL,
    tokens_cache_read INTEGER NOT NULL,
    usd REAL,
    context TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS chat_sessions (
    id TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    history_json TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS delivery_notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    image_filename TEXT NOT NULL,
    supplier TEXT,
    delivery_date TEXT,
    order_number TEXT,
    charge TEXT,
    material TEXT,
    quantity REAL,
    unit TEXT,
    delivery_note_number TEXT,
    waybill_number TEXT,
    b_numbers TEXT,
    confidence TEXT,
    status TEXT NOT NULL DEFAULT 'unmatched',
    matched_po_id INTEGER,
    matched_row_id INTEGER,
    proposed_quantity REAL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Kommande inleveranser (Monitor PurchaseOrderDeliveryRows) matchade mot cert.
-- PK = Monitor-radens stabila Id (delivery_row_id) → merge dedupar delleveranser
-- och bevarar operatörens local_status över en refresh.
CREATE TABLE IF NOT EXISTS upcoming_deliveries (
    delivery_row_id INTEGER PRIMARY KEY,
    purchase_order_id INTEGER NOT NULL DEFAULT 0,
    purchase_order_row_id INTEGER NOT NULL DEFAULT 0,
    order_number TEXT NOT NULL DEFAULT '',
    supplier_name TEXT NOT NULL DEFAULT '',
    part_id INTEGER NOT NULL DEFAULT 0,
    part_number TEXT NOT NULL DEFAULT '',
    dimensions TEXT NOT NULL DEFAULT '',
    planned_qty REAL NOT NULL DEFAULT 0,
    delivery_date TEXT NOT NULL DEFAULT '',
    cert_required BOOLEAN NOT NULL DEFAULT FALSE,
    cert_status TEXT NOT NULL DEFAULT 'none_required',   -- none_required|missing|matched
    cert_filename TEXT NOT NULL DEFAULT '',
    match_by TEXT NOT NULL DEFAULT 'none',               -- b_number|charge_part|none
    required_material TEXT NOT NULL DEFAULT '',
    required_cert TEXT NOT NULL DEFAULT '',
    our_material TEXT NOT NULL DEFAULT '',
    material_ok TEXT NOT NULL DEFAULT 'unknown',         -- ok|mismatch|unknown
    notes TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '',
    delivery_raw TEXT NOT NULL DEFAULT '',
    part_raw TEXT NOT NULL DEFAULT '',
    local_status TEXT NOT NULL DEFAULT 'pending',        -- pending|delivered
    first_seen TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Nyckel/värde-tabell för app-tillstånd (t.ex. last_upcoming_run för schemat).
CREATE TABLE IF NOT EXISTS app_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- Cache för AI-materialdomen (sonnet) per innehålls-hash, så att identiska rader
-- (samma artikel-ExtraDescription + samma cert) inte betalas varje kväll.
CREATE TABLE IF NOT EXISTS upcoming_classifications (
    cache_key TEXT PRIMARY KEY,
    required_material TEXT NOT NULL DEFAULT '',
    required_cert TEXT NOT NULL DEFAULT '',
    our_material TEXT NOT NULL DEFAULT '',
    material_ok TEXT NOT NULL DEFAULT 'unknown',
    notes TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_certificates_status ON certificates(status);
CREATE INDEX IF NOT EXISTS idx_certificates_email_id ON certificates(email_id);
CREATE INDEX IF NOT EXISTS idx_certificates_pdf_hash ON certificates(pdf_hash);
CREATE INDEX IF NOT EXISTS idx_ai_decisions_email_id ON ai_decisions(email_id);
CREATE INDEX IF NOT EXISTS idx_ai_decisions_certificate_id ON ai_decisions(certificate_id);
CREATE INDEX IF NOT EXISTS idx_cost_entries_certificate_id ON cost_entries(certificate_id);
CREATE INDEX IF NOT EXISTS idx_delivery_notes_status ON delivery_notes(status);
CREATE INDEX IF NOT EXISTS idx_upcoming_delivery_date ON upcoming_deliveries(delivery_date);
CREATE INDEX IF NOT EXISTS idx_upcoming_cert_status ON upcoming_deliveries(cert_status);
`

// InitDB öppnar (eller skapar) SQLite-databasen och kör migrations.
func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite stödjer bara en skrivare åt gången
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(dbSchema); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	log.Printf("🗄️  Databas initierad: %s", dbPath)
	return db, nil
}

// migrate applicerar idempotenta schema-ändringar på en befintlig databas.
// CREATE TABLE IF NOT EXISTS i dbSchema rör inte tabeller som redan finns, så
// nya kolumner på gamla tabeller måste läggas till här (ALTER) för att DB:er
// som skapades före en kolumn ska få den. Körs efter dbSchema (tabellerna
// finns garanterat) och är säker att köra om: redan-applicerade steg hoppas över.
func migrate(db *sql.DB) error {
	if err := ensureColumn(db, "emails", "mail_category", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_emails_mail_category ON emails(mail_category)`); err != nil {
		return err
	}
	return nil
}

// ensureColumn lägger till column på table om den saknas (idempotent).
// ddl är typ + ev. constraints, t.ex. "TEXT NOT NULL DEFAULT ''".
func ensureColumn(db *sql.DB, table, column, ddl string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // kolumnen finns redan
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl))
	return err
}

// DBPath returnerar sökvägen till databasen (bredvid config.json).
func DBPath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), "cert-renamer.db")
}
