package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repository hanterar alla databasoperationer.
type Repository struct {
	db *sql.DB
}

// NewRepository skapar en ny Repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// DB returnerar den underliggande databasen.
func (r *Repository) DB() *sql.DB {
	return r.db
}

// Email representerar en bearbetad email.
type Email struct {
	ID           int64  `json:"id"`
	Filename     string `json:"filename"`
	Subject      string `json:"subject"`
	FromAddr     string `json:"from_addr"`
	Date         string `json:"date"`
	Body         string `json:"body"`
	Status       string `json:"status"`
	MailCategory string `json:"mail_category"`
	ProcessedAt  string `json:"processed_at"`
	CreatedAt    string `json:"created_at"`
}

// Certificate representerar ett extraherat certifikat.
type Certificate struct {
	ID               int64   `json:"id"`
	EmailID          int64   `json:"email_id"`
	PDFHash          string  `json:"pdf_hash"`
	Filename         string  `json:"filename"`
	OriginalFilename string  `json:"original_filename"`
	CertType         string  `json:"cert_type"`
	Charge           string  `json:"charge"`
	Material         string  `json:"material"`
	MaterialShort    string  `json:"material_short"`
	ProductForm      string  `json:"product_form"`
	Dimensions       string  `json:"dimensions"`
	BNumbers         string  `json:"b_numbers"`
	Confidence       string  `json:"confidence"`
	Issues           string  `json:"issues"`
	ModelUsed        string  `json:"model_used"`
	TokensInput      int64   `json:"tokens_input"`
	TokensOutput     int64   `json:"tokens_output"`
	ProcessingMs     int64   `json:"processing_ms"`
	Status           string  `json:"status"`
	ExtractedAt      string  `json:"extracted_at"`
	HumanCorrected   bool    `json:"human_corrected"`
	CorrectedCharge  string  `json:"corrected_charge"`
	CorrectedMaterial string `json:"corrected_material"`
	CorrectedMaterialShort string `json:"corrected_material_short"`
	CorrectedProductForm string `json:"corrected_product_form"`
	CorrectedDimensions string `json:"corrected_dimensions"`
	CorrectedBNumbers string `json:"corrected_b_numbers"`
	CorrectionNotes string  `json:"correction_notes"`
	CorrectionLog   string  `json:"correction_log"`
	CreatedAt       string  `json:"created_at"`
}

// AIDecision representerar ett AI-beslut.
type AIDecision struct {
	ID                 int64  `json:"id"`
	EmailID            *int64 `json:"email_id"`
	CertificateID      *int64 `json:"certificate_id"`
	Step               string `json:"step"`
	Model              string `json:"model"`
	TokensInput        int64  `json:"tokens_input"`
	TokensOutput       int64  `json:"tokens_output"`
	TokensCacheCreation int64 `json:"tokens_cache_creation"`
	TokensCacheRead    int64  `json:"tokens_cache_read"`
	DurationMs         int64  `json:"duration_ms"`
	Success            bool   `json:"success"`
	ErrorMessage       string `json:"error_message"`
	CreatedAt          string `json:"created_at"`
}

// CostEntry representerar en kostnadspost.
type CostEntry struct {
	ID                 int64   `json:"id"`
	CertificateID      *int64  `json:"certificate_id"`
	Model              string  `json:"model"`
	TokensInput        int64   `json:"tokens_input"`
	TokensOutput       int64   `json:"tokens_output"`
	TokensCacheCreation int64  `json:"tokens_cache_creation"`
	TokensCacheRead    int64   `json:"tokens_cache_read"`
	USD                float64 `json:"usd"`
	Context            string  `json:"context"`
	CreatedAt          string  `json:"created_at"`
}

// --- Email operations ---

// InsertEmail infogar en ny email och returnerar dess ID.
func (r *Repository) InsertEmail(e *Email) (int64, error) {
	result, err := r.db.Exec(`
		INSERT INTO emails (filename, subject, from_addr, date, body, status, processed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Filename, e.Subject, e.FromAddr, e.Date, e.Body, e.Status, e.ProcessedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateEmailStatus uppdaterar status för en email.
func (r *Repository) UpdateEmailStatus(id int64, status string) error {
	_, err := r.db.Exec(`UPDATE emails SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateEmailCategory sätter mail_category för en email (Fas 2-klassificering).
func (r *Repository) UpdateEmailCategory(id int64, category string) error {
	_, err := r.db.Exec(`UPDATE emails SET mail_category = ? WHERE id = ?`, category, id)
	return err
}

// ListEmailsByCategory listar emails filtrerade på mail_category. Tom category
// ("") returnerar alla. Använder explicit kolumnlista (inte SELECT *) så att
// framtida kolumntillägg inte bryter scanningen.
func (r *Repository) ListEmailsByCategory(category string) ([]Email, error) {
	query := `SELECT id, filename, subject, from_addr, date, body, status, mail_category, processed_at, created_at FROM emails`
	var args []interface{}
	if category != "" {
		query += ` WHERE mail_category = ?`
		args = append(args, category)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []Email
	for rows.Next() {
		var e Email
		var subject, fromAddr, date, body sql.NullString
		if err := rows.Scan(
			&e.ID, &e.Filename, &subject, &fromAddr, &date, &body,
			&e.Status, &e.MailCategory, &e.ProcessedAt, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Subject = subject.String
		e.FromAddr = fromAddr.String
		e.Date = date.String
		e.Body = body.String
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// --- Certificate operations ---

// scanCertificate avkodar en certifikatrad från ett SELECT * (positionell ordning
// enligt certificates-schemat). En delad scanner så att alla cert-läsare (hash/
// filnamn/lista/ordermatch) har EN scan-plats — lägger man en kolumn på
// certificates uppdaterar man bara här. product_form/dimensions/b_numbers/issues
// skrivs alltid (om än "") av appen och scannas direkt; corrected_* är nullbara.
func scanCertificate(s interface {
	Scan(dest ...any) error
}) (Certificate, error) {
	var c Certificate
	var correctedCharge, correctedMaterial, correctedMaterialShort sql.NullString
	var correctedProductForm, correctedDimensions, correctedBNumbers sql.NullString
	var correctionNotes, correctionLog sql.NullString
	err := s.Scan(
		&c.ID, &c.EmailID, &c.PDFHash, &c.Filename, &c.OriginalFilename,
		&c.CertType, &c.Charge, &c.Material, &c.MaterialShort, &c.ProductForm, &c.Dimensions,
		&c.BNumbers, &c.Confidence, &c.Issues, &c.ModelUsed, &c.TokensInput, &c.TokensOutput,
		&c.ProcessingMs, &c.Status, &c.ExtractedAt, &c.HumanCorrected,
		&correctedCharge, &correctedMaterial, &correctedMaterialShort,
		&correctedProductForm, &correctedDimensions, &correctedBNumbers,
		&correctionNotes, &correctionLog, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	c.CorrectedCharge = correctedCharge.String
	c.CorrectedMaterial = correctedMaterial.String
	c.CorrectedMaterialShort = correctedMaterialShort.String
	c.CorrectedProductForm = correctedProductForm.String
	c.CorrectedDimensions = correctedDimensions.String
	c.CorrectedBNumbers = correctedBNumbers.String
	c.CorrectionNotes = correctionNotes.String
	c.CorrectionLog = correctionLog.String
	return c, nil
}

// InsertCertificate infogar ett nytt certifikat.
func (r *Repository) InsertCertificate(c *Certificate) (int64, error) {
	result, err := r.db.Exec(`
		INSERT INTO certificates (
			email_id, pdf_hash, filename, original_filename,
			cert_type, charge, material, material_short, product_form, dimensions,
			b_numbers, confidence, issues, model_used, tokens_input, tokens_output,
			processing_ms, status, extracted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.EmailID, c.PDFHash, c.Filename, c.OriginalFilename,
		c.CertType, c.Charge, c.Material, c.MaterialShort, c.ProductForm, c.Dimensions,
		c.BNumbers, c.Confidence, c.Issues, c.ModelUsed, c.TokensInput, c.TokensOutput,
		c.ProcessingMs, c.Status, c.ExtractedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateCertificateStatus uppdaterar status för ett certifikat.
func (r *Repository) UpdateCertificateStatus(id int64, status string) error {
	_, err := r.db.Exec(`UPDATE certificates SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateCertificateFilename uppdaterar filnamn för ett certifikat.
func (r *Repository) UpdateCertificateFilename(id int64, filename string) error {
	_, err := r.db.Exec(`UPDATE certificates SET filename = ? WHERE id = ?`, filename, id)
	return err
}

// UpdateCertificateCorrection uppdaterar korrigerade fält och loggar ändringen.
func (r *Repository) UpdateCertificateCorrection(id int64, field, oldValue, newValue string) error {
	// Hämta nuvarande log
	var currentLog sql.NullString
	err := r.db.QueryRow(`SELECT correction_log FROM certificates WHERE id = ?`, id).Scan(&currentLog)
	if err != nil {
		return err
	}

	var logEntries []map[string]string
	if currentLog.Valid {
		_ = json.Unmarshal([]byte(currentLog.String), &logEntries)
	}

	logEntries = append(logEntries, map[string]string{
		"timestamp": time.Now().Format(time.RFC3339),
		"field":     field,
		"old_value": oldValue,
		"new_value": newValue,
	})

	logJSON, _ := json.Marshal(logEntries)

	// Uppdatera fältet + loggen
	query := `UPDATE certificates SET ` + field + ` = ?, human_corrected = TRUE, correction_log = ? WHERE id = ?`
	_, err = r.db.Exec(query, newValue, string(logJSON), id)
	return err
}

// GetCertificateByHash hämtar ett certifikat via dess PDF-hash.
func (r *Repository) GetCertificateByHash(hash string) (*Certificate, error) {
	c, err := scanCertificate(r.db.QueryRow(`SELECT * FROM certificates WHERE pdf_hash = ?`, hash))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCertificateByFilename hämtar ett certifikat via dess filnamn.
func (r *Repository) GetCertificateByFilename(filename string) (*Certificate, error) {
	c, err := scanCertificate(r.db.QueryRow(`SELECT * FROM certificates WHERE filename = ?`, filename))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCertificates listar certifikat med valfritt filter.
func (r *Repository) ListCertificates(status string) ([]Certificate, error) {
	query := `SELECT * FROM certificates`
	var args []interface{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var certs []Certificate
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	return certs, rows.Err()
}

// ListCertificatesMatchingOrder returnerar certifikat vars B-nummer (b_numbers
// eller corrected_b_numbers) innehåller ordernumret. PurchaseOrder.OrderNumber ÄR
// ett B-nummer (t.ex. "B127575") och certets BNumbers bär ordernumret — det här är
// den primära cert↔order-matchningen. LIKE räcker som grovfilter; raden förfinas
// sedan via charge→ProductRecord→PartId i worker-lagret. Återanvänder scanCertificate.
func (r *Repository) ListCertificatesMatchingOrder(orderNumber string) ([]Certificate, error) {
	if strings.TrimSpace(orderNumber) == "" {
		return nil, nil
	}
	like := "%" + orderNumber + "%"
	rows, err := r.db.Query(
		`SELECT * FROM certificates WHERE b_numbers LIKE ? OR corrected_b_numbers LIKE ? ORDER BY created_at DESC`,
		like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCertificates räknar certifikat per status.
func (r *Repository) CountCertificates() (queue, approved, review, archived int64, err error) {
	rows, err := r.db.Query(`SELECT status, COUNT(*) FROM certificates GROUP BY status`)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, 0, 0, err
		}
		switch status {
		case "queue":
			queue = count
		case "approved":
			approved = count
		case "review":
			review = count
		case "archived":
			archived = count
		}
	}
	return queue, approved, review, archived, nil
}

// --- AI Decision operations ---

// InsertAIDecision infogar ett AI-beslut.
func (r *Repository) InsertAIDecision(d *AIDecision) (int64, error) {
	result, err := r.db.Exec(`
		INSERT INTO ai_decisions (
			email_id, certificate_id, step, model,
			tokens_input, tokens_output, tokens_cache_creation, tokens_cache_read,
			duration_ms, success, error_message
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.EmailID, d.CertificateID, d.Step, d.Model,
		d.TokensInput, d.TokensOutput, d.TokensCacheCreation, d.TokensCacheRead,
		d.DurationMs, d.Success, d.ErrorMessage)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// --- Cost operations ---

// InsertCostEntry infogar en kostnadspost.
func (r *Repository) InsertCostEntry(c *CostEntry) (int64, error) {
	result, err := r.db.Exec(`
		INSERT INTO cost_entries (
			certificate_id, model, tokens_input, tokens_output,
			tokens_cache_creation, tokens_cache_read, usd, context
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CertificateID, c.Model, c.TokensInput, c.TokensOutput,
		c.TokensCacheCreation, c.TokensCacheRead, c.USD, c.Context)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetTotalCosts returnerar totala kostnader per modell.
func (r *Repository) GetTotalCosts() (map[string]CostEntry, error) {
	rows, err := r.db.Query(`
		SELECT model,
			COALESCE(SUM(tokens_input), 0),
			COALESCE(SUM(tokens_output), 0),
			COALESCE(SUM(tokens_cache_creation), 0),
			COALESCE(SUM(tokens_cache_read), 0),
			COALESCE(SUM(usd), 0)
		FROM cost_entries
		GROUP BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	costs := make(map[string]CostEntry)
	for rows.Next() {
		var c CostEntry
		if err := rows.Scan(&c.Model, &c.TokensInput, &c.TokensOutput,
			&c.TokensCacheCreation, &c.TokensCacheRead, &c.USD); err != nil {
			return nil, err
		}
		costs[c.Model] = c
	}
	return costs, nil
}

// --- Delivery note operations (Fas 4: inleverans-trial) ---

// Status-flöde för en följesedel. Livscykeln slutar vid matched_po — själva
// inleveransregistreringen sker via UI-styrning av Monitor-klienten (utanför DB).
const (
	DNUnmatched = "unmatched"
	DNMatchedPO = "matched_po"
)

// DeliveryNote är en uppladdad följesedel (bild) med vision-extraherade fält +
// matchning mot Monitor-inköpsorder. status går unmatched → matched_po →
// receiving_proposed → receiving_confirmed.
type DeliveryNote struct {
	ID                 int64   `json:"id"`
	ImageFilename      string  `json:"image_filename"`
	Supplier           string  `json:"supplier"`
	DeliveryDate       string  `json:"delivery_date"`
	OrderNumber        string  `json:"order_number"`
	Charge             string  `json:"charge"`
	Material           string  `json:"material"`
	Quantity           float64 `json:"quantity"`
	Unit               string  `json:"unit"`
	DeliveryNoteNumber string  `json:"delivery_note_number"`
	WaybillNumber      string  `json:"waybill_number"`
	BNumbers           string  `json:"b_numbers"`
	Confidence         string  `json:"confidence"`
	Status             string  `json:"status"`
	MatchedPOID        int64   `json:"matched_po_id"`
	MatchedRowID       int64   `json:"matched_row_id"`
	ProposedQuantity   float64 `json:"proposed_quantity"`
	CreatedAt          string  `json:"created_at"`
}

const dnColumns = `id, image_filename, supplier, delivery_date, order_number, charge, material, quantity, unit, delivery_note_number, waybill_number, b_numbers, confidence, status, matched_po_id, matched_row_id, proposed_quantity, created_at`

func scanDeliveryNote(s interface {
	Scan(dest ...any) error
}) (DeliveryNote, error) {
	var d DeliveryNote
	var supplier, date, orderNo, charge, material, unit, dnNo, waybill, bnums, conf sql.NullString
	var qty, proposedQty sql.NullFloat64
	var poID, rowID sql.NullInt64
	err := s.Scan(
		&d.ID, &d.ImageFilename, &supplier, &date, &orderNo, &charge, &material,
		&qty, &unit, &dnNo, &waybill, &bnums, &conf, &d.Status,
		&poID, &rowID, &proposedQty, &d.CreatedAt,
	)
	if err != nil {
		return d, err
	}
	d.Supplier, d.DeliveryDate, d.OrderNumber = supplier.String, date.String, orderNo.String
	d.Charge, d.Material, d.Unit = charge.String, material.String, unit.String
	d.DeliveryNoteNumber, d.WaybillNumber, d.BNumbers, d.Confidence = dnNo.String, waybill.String, bnums.String, conf.String
	d.Quantity, d.ProposedQuantity = qty.Float64, proposedQty.Float64
	d.MatchedPOID, d.MatchedRowID = poID.Int64, rowID.Int64
	return d, nil
}

// InsertDeliveryNote infogar en ny följesedel (status default 'unmatched').
func (r *Repository) InsertDeliveryNote(d *DeliveryNote) (int64, error) {
	if d.Status == "" {
		d.Status = DNUnmatched
	}
	result, err := r.db.Exec(`
		INSERT INTO delivery_notes (
			image_filename, supplier, delivery_date, order_number, charge, material,
			quantity, unit, delivery_note_number, waybill_number, b_numbers, confidence, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ImageFilename, d.Supplier, d.DeliveryDate, d.OrderNumber, d.Charge, d.Material,
		d.Quantity, d.Unit, d.DeliveryNoteNumber, d.WaybillNumber, d.BNumbers, d.Confidence, d.Status)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetDeliveryNote hämtar en följesedel via id.
func (r *Repository) GetDeliveryNote(id int64) (*DeliveryNote, error) {
	row := r.db.QueryRow(`SELECT `+dnColumns+` FROM delivery_notes WHERE id = ?`, id)
	d, err := scanDeliveryNote(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDeliveryNotes listar följesedlar; tom status returnerar alla.
func (r *Repository) ListDeliveryNotes(status string) ([]DeliveryNote, error) {
	query := `SELECT ` + dnColumns + ` FROM delivery_notes`
	var args []interface{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryNote
	for rows.Next() {
		d, err := scanDeliveryNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateDeliveryNoteMatch sätter matchad PO/orderrad + status (matched_po).
func (r *Repository) UpdateDeliveryNoteMatch(id, poID, rowID int64, status string) error {
	_, err := r.db.Exec(
		`UPDATE delivery_notes SET matched_po_id = ?, matched_row_id = ?, status = ? WHERE id = ?`,
		poID, rowID, status, id)
	return err
}

// --- Chat Session operations ---

// SaveChatSession sparar eller uppdaterar en chat-session.
func (r *Repository) SaveChatSession(id, model, historyJSON string) error {
	_, err := r.db.Exec(`
		INSERT INTO chat_sessions (id, model, history_json, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET model = ?, history_json = ?, updated_at = datetime('now')`,
		id, model, historyJSON, model, historyJSON)
	return err
}

// LoadChatSession laddar en chat-session.
func (r *Repository) LoadChatSession(id string) (model, historyJSON string, err error) {
	err = r.db.QueryRow(`SELECT model, history_json FROM chat_sessions WHERE id = ?`, id).Scan(&model, &historyJSON)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return model, historyJSON, err
}

// DeleteChatSession tar bort en chat-session.
func (r *Repository) DeleteChatSession(id string) error {
	_, err := r.db.Exec(`DELETE FROM chat_sessions WHERE id = ?`, id)
	return err
}

// DeleteCertificate tar bort ett certifikat.
func (r *Repository) DeleteCertificate(id int64) error {
	_, err := r.db.Exec(`DELETE FROM certificates WHERE id = ?`, id)
	return err
}

// ReconcileQueue jämför queue/ på disk med DB och:
// - Infogar DB-poster för filer som saknas
// - Tar bort DB-poster vars filer saknas på disk
func (r *Repository) ReconcileQueue(queueDir string) (added, removed int, err error) {
	// 1. Hämta alla certificates med status="queue"
	dbCerts, err := r.ListCertificates("queue")
	if err != nil {
		return 0, 0, err
	}
	dbMap := map[string]int64{}
	for _, c := range dbCerts {
		dbMap[c.Filename] = c.ID
	}

	// 2. Skanna queue/ på disk
	entries, err := os.ReadDir(queueDir)
	if err != nil {
		return 0, 0, err
	}
	diskFiles := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			diskFiles[e.Name()] = true
		}
	}

	// 3. Infoga saknade DB-poster
	for filename := range diskFiles {
		if _, exists := dbMap[filename]; !exists {
			pdfPath := filepath.Join(queueDir, filename)
			cert := &Certificate{
				Filename:      filename,
				CertType:      "3.1",
				Status:        "queue",
				ExtractedAt:   time.Now().Format(time.RFC3339),
				ModelUsed:     "reconcile",
			}
			if m, ok := ReadMetadata(pdfPath); ok {
				cert.PDFHash = m.Hash
				cert.OriginalFilename = m.OriginalFilename
				cert.Charge = m.Charge
				cert.Material = m.Material
				cert.MaterialShort = m.MaterialShort
				cert.ProductForm = m.ProductForm
				cert.Dimensions = m.Dimensions
				cert.BNumbers = marshalStringSlice(m.BNumbers)
				cert.Confidence = m.Confidence
				cert.Issues = marshalStringSlice(m.Issues)
				cert.ModelUsed = m.ModelUsed
			} else {
				// Fallback: använd filnamn som hash om metadata saknas
				cert.PDFHash = "reconcile-" + filename
				cert.OriginalFilename = filename
				cert.Charge = "unknown"
				cert.Material = "unknown"
				cert.MaterialShort = "unknown"
				cert.Confidence = "low"
			}
			if _, insertErr := r.InsertCertificate(cert); insertErr == nil {
				added++
			}
		}
	}

	// 4. Ta bort DB-poster utan fil
	for filename, id := range dbMap {
		if !diskFiles[filename] {
			if delErr := r.DeleteCertificate(id); delErr == nil {
				removed++
			}
		}
	}

	return added, removed, nil
}

func marshalStringSlice(s []string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// --- Upcoming delivery operations (Kommande inleveranser) ---

// cert_status-värden.
const (
	CertNoneRequired = "none_required"
	CertMissing      = "missing"
	CertMatched      = "matched"
)

// match_by-värden (hur certet kopplades till raden).
const (
	MatchByBNumber    = "b_number"
	MatchByChargePart = "charge_part"
	MatchByNone       = "none"
)

// material_ok-värden (AI-domen).
const (
	MaterialOK       = "ok"
	MaterialMismatch = "mismatch"
	MaterialUnknown  = "unknown"
)

// local_status-värden (operatörens markering, rörs aldrig av refresh).
const (
	UpcomingPending   = "pending"
	UpcomingDelivered = "delivered"
)

// AppStateLastUpcomingRun är nyckeln för senaste schemalagda upcoming-körning.
const AppStateLastUpcomingRun = "last_upcoming_run"

// UpcomingDelivery är en kommande inleveransrad (Monitor) matchad mot cert.
// delivery_row_id (Monitor-radens stabila Id) är naturlig nyckel.
type UpcomingDelivery struct {
	DeliveryRowID      int64   `json:"delivery_row_id"`
	PurchaseOrderID    int64   `json:"purchase_order_id"`
	PurchaseOrderRowID int64   `json:"purchase_order_row_id"`
	OrderNumber        string  `json:"order_number"`
	SupplierName       string  `json:"supplier_name"`
	PartID             int64   `json:"part_id"`
	PartNumber         string  `json:"part_number"`
	Dimensions         string  `json:"dimensions"`
	PlannedQty         float64 `json:"planned_qty"`
	DeliveryDate       string  `json:"delivery_date"`
	CertRequired       bool    `json:"cert_required"`
	CertStatus         string  `json:"cert_status"`
	CertFilename       string  `json:"cert_filename"`
	MatchBy            string  `json:"match_by"`
	RequiredMaterial   string  `json:"required_material"`
	RequiredCert       string  `json:"required_cert"`
	OurMaterial        string  `json:"our_material"`
	MaterialOK         string  `json:"material_ok"`
	Notes              string  `json:"notes"`
	EvidenceJSON       string  `json:"evidence_json"`
	DeliveryRaw        string  `json:"delivery_raw"`
	PartRaw            string  `json:"part_raw"`
	LocalStatus        string  `json:"local_status"`
	FirstSeen          string  `json:"first_seen"`
	LastSeen           string  `json:"last_seen"`
}

const upcomingColumns = `delivery_row_id, purchase_order_id, purchase_order_row_id, order_number, supplier_name, part_id, part_number, dimensions, planned_qty, delivery_date, cert_required, cert_status, cert_filename, match_by, required_material, required_cert, our_material, material_ok, notes, evidence_json, delivery_raw, part_raw, local_status, first_seen, last_seen`

func scanUpcoming(s interface {
	Scan(dest ...any) error
}) (UpcomingDelivery, error) {
	var u UpcomingDelivery
	err := s.Scan(
		&u.DeliveryRowID, &u.PurchaseOrderID, &u.PurchaseOrderRowID, &u.OrderNumber,
		&u.SupplierName, &u.PartID, &u.PartNumber, &u.Dimensions, &u.PlannedQty,
		&u.DeliveryDate, &u.CertRequired, &u.CertStatus, &u.CertFilename, &u.MatchBy,
		&u.RequiredMaterial, &u.RequiredCert, &u.OurMaterial, &u.MaterialOK, &u.Notes,
		&u.EvidenceJSON, &u.DeliveryRaw, &u.PartRaw, &u.LocalStatus, &u.FirstSeen, &u.LastSeen,
	)
	return u, err
}

// MergeUpcomingDeliveries gör en upsert per rad på delivery_row_id och rör ALDRIG
// local_status eller first_seen (så operatörens "delivered"-markering överlever en
// refresh). Rader som inte sågs denna körning OCH inte är delivered raderas
// (stale-delete). Allt i en transaktion. Mönster: ReconcileQueue.
func (r *Repository) MergeUpcomingDeliveries(rows []UpcomingDelivery) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op efter Commit

	now := time.Now().Format(time.RFC3339)
	seen := make([]int64, 0, len(rows))
	for _, u := range rows {
		ls := u.LocalStatus
		if ls == "" {
			ls = UpcomingPending
		}
		_, err := tx.Exec(`
			INSERT INTO upcoming_deliveries (`+upcomingColumns+`)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(delivery_row_id) DO UPDATE SET
				purchase_order_id=excluded.purchase_order_id,
				purchase_order_row_id=excluded.purchase_order_row_id,
				order_number=excluded.order_number,
				supplier_name=excluded.supplier_name,
				part_id=excluded.part_id,
				part_number=excluded.part_number,
				dimensions=excluded.dimensions,
				planned_qty=excluded.planned_qty,
				delivery_date=excluded.delivery_date,
				cert_required=excluded.cert_required,
				cert_status=excluded.cert_status,
				cert_filename=excluded.cert_filename,
				match_by=excluded.match_by,
				required_material=excluded.required_material,
				required_cert=excluded.required_cert,
				our_material=excluded.our_material,
				material_ok=excluded.material_ok,
				notes=excluded.notes,
				evidence_json=excluded.evidence_json,
				delivery_raw=excluded.delivery_raw,
				part_raw=excluded.part_raw,
				last_seen=excluded.last_seen`,
			// local_status och first_seen UPPDATERAS MEDVETET INTE ovan.
			u.DeliveryRowID, u.PurchaseOrderID, u.PurchaseOrderRowID, u.OrderNumber,
			u.SupplierName, u.PartID, u.PartNumber, u.Dimensions, u.PlannedQty,
			u.DeliveryDate, u.CertRequired, u.CertStatus, u.CertFilename, u.MatchBy,
			u.RequiredMaterial, u.RequiredCert, u.OurMaterial, u.MaterialOK, u.Notes,
			u.EvidenceJSON, u.DeliveryRaw, u.PartRaw, ls, now, now,
		)
		if err != nil {
			return err
		}
		seen = append(seen, u.DeliveryRowID)
	}

	if err := deleteUnseenUpcoming(tx, seen); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteUnseenUpcoming raderar pending-rader som inte fanns med i denna körning.
// Delivered-rader bevaras alltid. Tom seen → alla pending raderas (Monitor gav
// inget). Använder en explicit ID-lista (deterministiskt, ingen tidsstämpel-
// jämförelse). Listans storlek är i praktiken liten (ett par hundra rader / fönster).
func deleteUnseenUpcoming(tx *sql.Tx, seen []int64) error {
	if len(seen) == 0 {
		_, err := tx.Exec(`DELETE FROM upcoming_deliveries WHERE local_status != ?`, UpcomingDelivered)
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(seen)), ",")
	args := make([]any, 0, len(seen)+1)
	for _, id := range seen {
		args = append(args, id)
	}
	args = append(args, UpcomingDelivered)
	q := `DELETE FROM upcoming_deliveries WHERE delivery_row_id NOT IN (` + placeholders + `) AND local_status != ?`
	_, err := tx.Exec(q, args...)
	return err
}

// ListUpcoming returnerar alla kommande inleveranser, sorterade på leveransdatum.
func (r *Repository) ListUpcoming() ([]UpcomingDelivery, error) {
	rows, err := r.db.Query(`SELECT ` + upcomingColumns + ` FROM upcoming_deliveries ORDER BY delivery_date ASC, order_number ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingDelivery
	for rows.Next() {
		u, err := scanUpcoming(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUpcomingByRowID hämtar en rad via Monitor-radens Id. nil utan fel om saknas.
func (r *Repository) GetUpcomingByRowID(id int64) (*UpcomingDelivery, error) {
	u, err := scanUpcoming(r.db.QueryRow(`SELECT `+upcomingColumns+` FROM upcoming_deliveries WHERE delivery_row_id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// MarkUpcomingDelivered sätter local_status=delivered på en rad (nyckel = stabilt
// Monitor-rad-Id, inte order+part).
func (r *Repository) MarkUpcomingDelivered(deliveryRowID int64) error {
	_, err := r.db.Exec(`UPDATE upcoming_deliveries SET local_status = ? WHERE delivery_row_id = ?`, UpcomingDelivered, deliveryRowID)
	return err
}

// --- App state (nyckel/värde) ---

// GetAppState hämtar ett värde; tom sträng (utan fel) om nyckeln saknas.
func (r *Repository) GetAppState(key string) (string, error) {
	var v string
	err := r.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetAppState sätter (upsert) ett nyckel/värde-par.
func (r *Repository) SetAppState(key, value string) error {
	_, err := r.db.Exec(`
		INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = ?`, key, value, value)
	return err
}

// --- AI-materialdom-cache (Kommande inleveranser) ---

// UpcomingClassificationCache är en cachead AI-materialdom (utan ai-import i store).
type UpcomingClassificationCache struct {
	RequiredMaterial string `json:"required_material"`
	RequiredCert     string `json:"required_cert"`
	OurMaterial      string `json:"our_material"`
	MaterialOK       string `json:"material_ok"`
	Notes            string `json:"notes"`
}

// GetUpcomingClassification slår upp en cachead dom på innehålls-nyckel. nil utan
// fel om cache-miss.
func (r *Repository) GetUpcomingClassification(cacheKey string) (*UpcomingClassificationCache, error) {
	var c UpcomingClassificationCache
	err := r.db.QueryRow(
		`SELECT required_material, required_cert, our_material, material_ok, notes
		 FROM upcoming_classifications WHERE cache_key = ?`, cacheKey).
		Scan(&c.RequiredMaterial, &c.RequiredCert, &c.OurMaterial, &c.MaterialOK, &c.Notes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveUpcomingClassification cachar en AI-dom på innehålls-nyckel (upsert).
func (r *Repository) SaveUpcomingClassification(cacheKey string, c UpcomingClassificationCache) error {
	_, err := r.db.Exec(`
		INSERT INTO upcoming_classifications (cache_key, required_material, required_cert, our_material, material_ok, notes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			required_material=excluded.required_material,
			required_cert=excluded.required_cert,
			our_material=excluded.our_material,
			material_ok=excluded.material_ok,
			notes=excluded.notes`,
		cacheKey, c.RequiredMaterial, c.RequiredCert, c.OurMaterial, c.MaterialOK, c.Notes)
	return err
}
