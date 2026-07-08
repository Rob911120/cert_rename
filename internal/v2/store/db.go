// Package store är V2:s persistens: SQLite-schema med numrerade migrationer,
// dum CRUD-repository och filhantering (certlager + utmapp). Ingen affärslogik
// här — den bor i internal/v2/domain och internal/v2/app.
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

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
	// 002 — kolumnkalibrerade extraktionsfält (Task 1, cert.Extraction):
	// slag-, kemi- och märkningsdata. is_legible/is_unaltered defaultar till 1
	// ("inget problem upptäckt") för befintliga rader; övriga booleans till 0.
	// Nullable-tal (impact_temp_c m.fl.) får ingen default — NULL kvarstår.
	`
ALTER TABLE certs ADD COLUMN is_legible INTEGER NOT NULL DEFAULT 1;
ALTER TABLE certs ADD COLUMN is_unaltered INTEGER NOT NULL DEFAULT 1;
ALTER TABLE certs ADD COLUMN norm_system TEXT NOT NULL DEFAULT '';
ALTER TABLE certs ADD COLUMN impact_temp_c REAL;
ALTER TABLE certs ADD COLUMN impact_energy_j REAL NOT NULL DEFAULT 0;
ALTER TABLE certs ADD COLUMN norm_edition TEXT NOT NULL DEFAULT '';
ALTER TABLE certs ADD COLUMN ped_directive TEXT NOT NULL DEFAULT '';
ALTER TABLE certs ADD COLUMN cev REAL;
ALTER TABLE certs ADD COLUMN carbon_pct REAL;
ALTER TABLE certs ADD COLUMN p_pct REAL;
ALTER TABLE certs ADD COLUMN s_pct REAL;
ALTER TABLE certs ADD COLUMN has_bend_test INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certs ADD COLUMN has_intergranular_test INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certs ADD COLUMN has_stamp_photo INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certs ADD COLUMN min_temperature_c REAL;
ALTER TABLE certs ADD COLUMN delivery_condition TEXT NOT NULL DEFAULT '';
`,
	// 003 — Task 6 (B0b): cert-bärande fält som internal/monitor nu hämtar
	// (Task 5) på order_rows — rå kravtext + artikeldata. AI-parsning av
	// kravtexterna kommer i en senare task; hyperlinks lagras som JSON-TEXT
	// (marshal/unmarshal i store-lagret, tom lista → '').
	`
ALTER TABLE order_rows ADD COLUMN receiving_message TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN receiving_inspection_instruction TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN row_goods_label TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN row_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN supplier_drawing_number TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN supplier_revision_number TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN free_text TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN order_goods_label TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN external_comment TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN business_contact_order_number TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN alloy_code TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN alloy_description TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN part_receiving_instruction TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN part_purchase_comment TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN part_comment TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN part_length REAL NOT NULL DEFAULT 0;
ALTER TABLE order_rows ADD COLUMN part_width REAL NOT NULL DEFAULT 0;
ALTER TABLE order_rows ADD COLUMN part_height REAL NOT NULL DEFAULT 0;
ALTER TABLE order_rows ADD COLUMN weight_per_unit REAL NOT NULL DEFAULT 0;
ALTER TABLE order_rows ADD COLUMN goods_type TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN category_string TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN extra_fields_raw TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN hyperlinks TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN drawing_numbers TEXT NOT NULL DEFAULT '';
`,
	// 004 — Task 8 (B1b): AI-tolkade artikelkrav (RowRequirements) persisterade
	// på order_rows + en cache så oförändrade artiklar aldrig omparsas. Fälten
	// skrivs ENDAST via App.SetRowRequirements och röres ALDRIG av sync-upserten
	// (samma undantag som delivered/first_seen). req_parsed_at följer first_seen:
	// TEXT (tom = aldrig parsad). Cachen speglar ai_match_cache-mönstret men i
	// egen tabell — värdet är kraven som JSON, nyckeln en hash av artikel + alla
	// kravtexter som skickas till AI:n.
	`
ALTER TABLE order_rows ADD COLUMN req_material TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_en_norm TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_cert_type TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_english INTEGER NOT NULL DEFAULT 0;
ALTER TABLE order_rows ADD COLUMN req_product_form TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_dimensions TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_impact TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE order_rows ADD COLUMN req_parsed_at TEXT NOT NULL DEFAULT '';

-- Krav-tolkningscache: nyckel = hash(artikel + alla kravtexter), värde = kraven
-- som JSON. Ändras någon kravtext ändras nyckeln → färsk tolkning.
CREATE TABLE ai_requirements_cache (
    cache_key    TEXT PRIMARY KEY,
    requirements TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
`,
	// 005 — product_code: cert-typens förkortning (RS, PL, HEB, 4R, ÖS …) som
	// AI:n fyller vid extraktion enligt kodtabellen i extractV2SystemPrompt.
	// Används i filnamnets formsegment i stället för den råa product_form-texten
	// (faller tillbaka på product_form när koden är tom). Redigerbar direkt via
	// ApplyCorrection — medvetet ingen corrected_-tvilling (jfr name_override).
	`ALTER TABLE certs ADD COLUMN product_code TEXT NOT NULL DEFAULT '';`,

	// 006 — emails.file_hash: intagets fel-skip nycklas på filnamn +
	// innehålls-hash i stället för bara filnamn — ett NYTT mejl som råkar heta
	// som ett gammalt felmejl ska inte skippas för alltid. Gamla rader får ''
	// och matchar därmed aldrig en riktig hash.
	`ALTER TABLE emails ADD COLUMN file_hash TEXT NOT NULL DEFAULT '';`,

	// 007 — emails.error_acked: kvitterade intagsfel. UI-bannern visar bara
	// okvitterade fel; ✕ på bannern sätter error_acked=1 på dagens felrader så
	// bannern försvinner när Rob läst den. status rörs INTE — intagets fel-skip
	// läser status='error' och ska fortsätta skippa filen. Nya fel blir nya
	// rader (error_acked=0) och syns alltid.
	`ALTER TABLE emails ADD COLUMN error_acked INTEGER NOT NULL DEFAULT 0;`,

	// 008 — följesedlar (delivery notes): fotad papperssedel som mejlats in,
	// vision-extraherad och surfad i Att göra. Matchning mot Monitor-order bärs
	// av de NULLbara datafälten matched_po_id/matched_row_id (0 = ej matchad) —
	// inget eget livscykelskede. image_hash är dedupe-nyckeln (omsänt foto ska
	// inte skapa dubblett). Livscykel: mottagen → inlevererad | avfardad.
	`
CREATE TABLE delivery_notes (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    image_hash           TEXT NOT NULL UNIQUE,
    image_filename       TEXT NOT NULL,
    supplier             TEXT NOT NULL DEFAULT '',
    delivery_date        TEXT NOT NULL DEFAULT '',
    order_number         TEXT NOT NULL DEFAULT '',
    charge               TEXT NOT NULL DEFAULT '',
    material             TEXT NOT NULL DEFAULT '',
    quantity             REAL NOT NULL DEFAULT 0,
    unit                 TEXT NOT NULL DEFAULT '',
    delivery_note_number TEXT NOT NULL DEFAULT '',
    waybill_number       TEXT NOT NULL DEFAULT '',
    b_numbers            TEXT NOT NULL DEFAULT '[]',
    confidence           TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL DEFAULT 'mottagen',
    matched_po_id        INTEGER NOT NULL DEFAULT 0,
    matched_row_id       INTEGER NOT NULL DEFAULT 0,
    proposed_quantity    REAL NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL
);
CREATE INDEX idx_delivery_notes_status ON delivery_notes(status);
`,
}

// openDB försöker öppna databasen i WAL-läge (bäst för samtidiga läsningar under
// en lång sync) och faller tillbaka på ett vanligt läge om WAL inte går att slå
// på. WAL kräver en anständig lokal filesystem och exklusiv åtkomst vid själva
// omställningen — det MISSLYCKAS typiskt på nätverks-/molnsynkade mappar (t.ex.
// OneDrive under Windows APPDATA) eller om en kvarhängande tidigare instans
// fortfarande håller filen låst. Ett WAL-fel FÅR ALDRIG hindra appen från att
// starta (modernc returnerar pragma-fel som fatala vid open), så vi provar WAL
// och backar tyst till rå öppning om det inte tar.
//
// Returnerar (db, walPå, err). walPå styr connection-poolens storlek i Open.
func openDB(dbPath string) (*sql.DB, bool, error) {
	// Pragmorna splittas av drivrutinen vid första '?'; en filsökväg (även
	// Windows C:\…) innehåller aldrig '?', så det är säkert att lägga till dem.
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if db, err := sql.Open("sqlite", dsn); err == nil {
		// Ping öppnar en anslutning och kör pragmorna; lyckas det OCH journal_mode
		// faktiskt blev "wal" är WAL påslaget för alla framtida anslutningar
		// (journal_mode persisteras i filhuvudet).
		if perr := db.Ping(); perr == nil {
			var jm string
			if qerr := db.QueryRow("PRAGMA journal_mode").Scan(&jm); qerr == nil && strings.EqualFold(jm, "wal") {
				return db, true, nil
			}
		}
		db.Close()
	}
	// Fallback: rå sökväg utan pragmor — alltid-fungerande rollback-journal-läge.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, false, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, false, err
	}
	return db, false, nil
}

// Open öppnar (eller skapar) V2-databasen och applicerar väntande migrationer.
func Open(dbPath string) (*sql.DB, error) {
	db, wal, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	if wal {
		// WAL = en skrivare + flera läsare samtidigt. Skrivare serialiseras av
		// SQLite och väntar upp till busy_timeout (5 s) i stället för att fela med
		// SQLITE_BUSY; läsare blockeras aldrig av skrivaren. Poolen släpps upp så
		// läs-anrop (t.ex. den tunga GET /api/overview) kan få egna anslutningar
		// medan en lång Monitor-sync håller sin skrivanslutning varm.
		db.SetMaxOpenConns(8)
	} else {
		// Fallback (WAL gick inte att slå på — se openDB): klassiskt
		// rollback-journal-läge, en skrivare åt gången. Samma beteende som före
		// WAL-införandet; UI:t kan då köa bakom synken men appen STARTAR alltid.
		db.SetMaxOpenConns(1)
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
	return filepath.Join(filepath.Dir(ConfigPath()), "cert-renamer-v2.db")
}
