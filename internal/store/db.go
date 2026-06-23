package store

import (
	"database/sql"
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

CREATE INDEX IF NOT EXISTS idx_certificates_status ON certificates(status);
CREATE INDEX IF NOT EXISTS idx_certificates_email_id ON certificates(email_id);
CREATE INDEX IF NOT EXISTS idx_certificates_pdf_hash ON certificates(pdf_hash);
CREATE INDEX IF NOT EXISTS idx_ai_decisions_email_id ON ai_decisions(email_id);
CREATE INDEX IF NOT EXISTS idx_ai_decisions_certificate_id ON ai_decisions(certificate_id);
CREATE INDEX IF NOT EXISTS idx_cost_entries_certificate_id ON cost_entries(certificate_id);
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
	log.Printf("🗄️  Databas initierad: %s", dbPath)
	return db, nil
}

// DBPath returnerar sökvägen till databasen (bredvid config.json).
func DBPath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), "cert-renamer.db")
}
