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
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	Subject     string `json:"subject"`
	FromAddr    string `json:"from_addr"`
	Date        string `json:"date"`
	Body        string `json:"body"`
	Status      string `json:"status"`
	ProcessedAt string `json:"processed_at"`
	CreatedAt   string `json:"created_at"`
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

// --- Certificate operations ---

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
	var c Certificate
	var correctedCharge, correctedMaterial, correctedMaterialShort sql.NullString
	var correctedProductForm, correctedDimensions, correctedBNumbers sql.NullString
	var correctionNotes, correctionLog sql.NullString
	err := r.db.QueryRow(`SELECT * FROM certificates WHERE pdf_hash = ?`, hash).Scan(
		&c.ID, &c.EmailID, &c.PDFHash, &c.Filename, &c.OriginalFilename,
		&c.CertType, &c.Charge, &c.Material, &c.MaterialShort, &c.ProductForm, &c.Dimensions,
		&c.BNumbers, &c.Confidence, &c.Issues, &c.ModelUsed, &c.TokensInput, &c.TokensOutput,
		&c.ProcessingMs, &c.Status, &c.ExtractedAt, &c.HumanCorrected,
		&correctedCharge, &correctedMaterial, &correctedMaterialShort,
		&correctedProductForm, &correctedDimensions, &correctedBNumbers,
		&correctionNotes, &correctionLog, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.CorrectedCharge = correctedCharge.String
	c.CorrectedMaterial = correctedMaterial.String
	c.CorrectedMaterialShort = correctedMaterialShort.String
	c.CorrectedProductForm = correctedProductForm.String
	c.CorrectedDimensions = correctedDimensions.String
	c.CorrectedBNumbers = correctedBNumbers.String
	c.CorrectionNotes = correctionNotes.String
	c.CorrectionLog = correctionLog.String
	return &c, nil
}

// GetCertificateByFilename hämtar ett certifikat via dess filnamn.
func (r *Repository) GetCertificateByFilename(filename string) (*Certificate, error) {
	var c Certificate
	var correctedCharge, correctedMaterial, correctedMaterialShort sql.NullString
	var correctedProductForm, correctedDimensions, correctedBNumbers sql.NullString
	var correctionNotes, correctionLog sql.NullString
	err := r.db.QueryRow(`SELECT * FROM certificates WHERE filename = ?`, filename).Scan(
		&c.ID, &c.EmailID, &c.PDFHash, &c.Filename, &c.OriginalFilename,
		&c.CertType, &c.Charge, &c.Material, &c.MaterialShort, &c.ProductForm, &c.Dimensions,
		&c.BNumbers, &c.Confidence, &c.Issues, &c.ModelUsed, &c.TokensInput, &c.TokensOutput,
		&c.ProcessingMs, &c.Status, &c.ExtractedAt, &c.HumanCorrected,
		&correctedCharge, &correctedMaterial, &correctedMaterialShort,
		&correctedProductForm, &correctedDimensions, &correctedBNumbers,
		&correctionNotes, &correctionLog, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.CorrectedCharge = correctedCharge.String
	c.CorrectedMaterial = correctedMaterial.String
	c.CorrectedMaterialShort = correctedMaterialShort.String
	c.CorrectedProductForm = correctedProductForm.String
	c.CorrectedDimensions = correctedDimensions.String
	c.CorrectedBNumbers = correctedBNumbers.String
	c.CorrectionNotes = correctionNotes.String
	c.CorrectionLog = correctionLog.String
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
		var c Certificate
		var correctedCharge, correctedMaterial, correctedMaterialShort sql.NullString
		var correctedProductForm, correctedDimensions, correctedBNumbers sql.NullString
		var correctionNotes, correctionLog sql.NullString
		if err := rows.Scan(
			&c.ID, &c.EmailID, &c.PDFHash, &c.Filename, &c.OriginalFilename,
			&c.CertType, &c.Charge, &c.Material, &c.MaterialShort, &c.ProductForm, &c.Dimensions,
			&c.BNumbers, &c.Confidence, &c.Issues, &c.ModelUsed, &c.TokensInput, &c.TokensOutput,
			&c.ProcessingMs, &c.Status, &c.ExtractedAt, &c.HumanCorrected,
			&correctedCharge, &correctedMaterial, &correctedMaterialShort,
			&correctedProductForm, &correctedDimensions, &correctedBNumbers,
			&correctionNotes, &correctionLog, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		c.CorrectedCharge = correctedCharge.String
		c.CorrectedMaterial = correctedMaterial.String
		c.CorrectedMaterialShort = correctedMaterialShort.String
		c.CorrectedProductForm = correctedProductForm.String
		c.CorrectedDimensions = correctedDimensions.String
		c.CorrectedBNumbers = correctedBNumbers.String
		c.CorrectionNotes = correctionNotes.String
		c.CorrectionLog = correctionLog.String
		certs = append(certs, c)
	}
	return certs, nil
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
