package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cert-renamer/internal/v2/domain"
)

// DBTX abstraherar *sql.DB och *sql.Tx så att samma CRUD-metoder fungerar i
// och utanför transaktioner.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Q är den dumma CRUD-ytan — ingen affärslogik, ingen guard-logik; det bor i
// internal/v2/app. Alla metoder fungerar mot både DB och pågående transaktion.
type Q struct {
	db DBTX
}

// Repository är Q bunden till databasen, plus transaktionsstart.
type Repository struct {
	Q
	sqldb *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{Q: Q{db: db}, sqldb: db}
}

// Tx kör fn i en transaktion; rollback vid fel, commit annars.
func (r *Repository) Tx(ctx context.Context, fn func(q *Q) error) error {
	tx, err := r.sqldb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Q{db: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// JSON-hjälpare ([]string och rättelselogg lagras som JSON-text)
// ---------------------------------------------------------------------------

func marshalList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalList(s string) []string {
	var v []string
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return []string{}
	}
	return v
}

// corrected_b_numbers: ” = ej rättad (nil), annars JSON-array (även "[]" =
// rättad till inga).
func marshalCorrectedB(v []string) string {
	if v == nil {
		return ""
	}
	return marshalList(v)
}

func unmarshalCorrectedB(s string) []string {
	if s == "" {
		return nil
	}
	return unmarshalList(s)
}

// marshalHyperlinks/unmarshalHyperlinks: order_rows.hyperlinks lagras som
// JSON-TEXT (domain.Hyperlink bär redan json-taggar, se domain.go). Tom/nil
// lista → ” (inte "[]") — enligt uppdraget, se Task 6-briefen.
func marshalHyperlinks(v []domain.Hyperlink) string {
	if len(v) == 0 {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalHyperlinks(s string) []domain.Hyperlink {
	var v []domain.Hyperlink
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return []domain.Hyperlink{}
	}
	return v
}

func marshalLog(v []domain.Correction) string {
	if v == nil {
		v = []domain.Correction{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalLog(s string) []domain.Correction {
	var v []domain.Correction
	if s == "" || json.Unmarshal([]byte(s), &v) != nil {
		return []domain.Correction{}
	}
	return v
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ptrToNullFloat/nullFloatToPtr: nullable-tal (impact_temp_c m.fl.) mappas
// till/från *float64 via sql.NullFloat64 explicit — inget existerande
// prejudikat i V2 för NULL-bara tal.
func ptrToNullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullFloatToPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// ---------------------------------------------------------------------------
// Certs
// ---------------------------------------------------------------------------

const certCols = `id, pdf_hash, original_filename, stored_name,
 email_subject, email_from, email_date,
 cert_type, charge, material, en_standard_present, is_english, product_form,
 product_code, dimensions, country_of_origin, b_numbers, confidence, issues, model_used,
 tokens_input, tokens_output, processing_ms,
 is_legible, is_unaltered, norm_system, impact_temp_c, impact_energy_j,
 norm_edition, ped_directive, cev, carbon_pct, p_pct, s_pct,
 has_bend_test, has_intergranular_test, has_stamp_photo, min_temperature_c,
 delivery_condition,
 corrected_charge, corrected_material, corrected_product_form,
 corrected_dimensions, corrected_cert_type, corrected_b_numbers,
 correction_log, name_override, status, final_filename, output_path,
 saved_at, received_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanCert(sc rowScanner) (*domain.Cert, error) {
	var c domain.Cert
	var enStd, isEng int
	var bNums, issues, corrB, corrLog, status string
	var isLegible, isUnaltered, hasBend, hasIntergranular, hasStampPhoto int
	var impactTempC, cev, carbonPct, pPct, sPct, minTemperatureC sql.NullFloat64
	err := sc.Scan(&c.ID, &c.PdfHash, &c.OriginalFilename, &c.StoredName,
		&c.EmailSubject, &c.EmailFrom, &c.EmailDate,
		&c.CertType, &c.Charge, &c.Material, &enStd, &isEng, &c.ProductForm,
		&c.ProductCode, &c.Dimensions, &c.CountryOfOrigin, &bNums, &c.Confidence, &issues, &c.ModelUsed,
		&c.TokensInput, &c.TokensOutput, &c.ProcessingMS,
		&isLegible, &isUnaltered, &c.NormSystem, &impactTempC, &c.ImpactEnergyJ,
		&c.NormEdition, &c.PedDirective, &cev, &carbonPct, &pPct, &sPct,
		&hasBend, &hasIntergranular, &hasStampPhoto, &minTemperatureC,
		&c.DeliveryCondition,
		&c.CorrectedCharge, &c.CorrectedMaterial, &c.CorrectedProductForm,
		&c.CorrectedDimensions, &c.CorrectedCertType, &corrB,
		&corrLog, &c.NameOverride, &status, &c.FinalFilename, &c.OutputPath,
		&c.SavedAt, &c.ReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.EnStandardPresent = enStd == 1
	c.IsEnglish = isEng == 1
	c.IsLegible = isLegible == 1
	c.IsUnaltered = isUnaltered == 1
	c.HasBendTest = hasBend == 1
	c.HasIntergranularTest = hasIntergranular == 1
	c.HasStampPhoto = hasStampPhoto == 1
	c.ImpactTempC = nullFloatToPtr(impactTempC)
	c.Cev = nullFloatToPtr(cev)
	c.CarbonPct = nullFloatToPtr(carbonPct)
	c.PPct = nullFloatToPtr(pPct)
	c.SPct = nullFloatToPtr(sPct)
	c.MinTemperatureC = nullFloatToPtr(minTemperatureC)
	c.BNumbers = unmarshalList(bNums)
	c.Issues = unmarshalList(issues)
	c.CorrectedBNumbers = unmarshalCorrectedB(corrB)
	c.CorrectionLog = unmarshalLog(corrLog)
	c.Status = domain.CertStatus(status)
	return &c, nil
}

func (q *Q) InsertCert(ctx context.Context, c *domain.Cert) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO certs
		(pdf_hash, original_filename, stored_name,
		 email_subject, email_from, email_date,
		 cert_type, charge, material, en_standard_present, is_english, product_form,
		 product_code, dimensions, country_of_origin, b_numbers, confidence, issues, model_used,
		 tokens_input, tokens_output, processing_ms,
		 is_legible, is_unaltered, norm_system, impact_temp_c, impact_energy_j,
		 norm_edition, ped_directive, cev, carbon_pct, p_pct, s_pct,
		 has_bend_test, has_intergranular_test, has_stamp_photo, min_temperature_c,
		 delivery_condition,
		 corrected_b_numbers, correction_log, name_override, status, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.PdfHash, c.OriginalFilename, c.StoredName,
		c.EmailSubject, c.EmailFrom, c.EmailDate,
		c.CertType, c.Charge, c.Material, b2i(c.EnStandardPresent), b2i(c.IsEnglish), c.ProductForm,
		c.ProductCode, c.Dimensions, c.CountryOfOrigin, marshalList(c.BNumbers), c.Confidence, marshalList(c.Issues), c.ModelUsed,
		c.TokensInput, c.TokensOutput, c.ProcessingMS,
		b2i(c.IsLegible), b2i(c.IsUnaltered), c.NormSystem, ptrToNullFloat(c.ImpactTempC), c.ImpactEnergyJ,
		c.NormEdition, c.PedDirective, ptrToNullFloat(c.Cev), ptrToNullFloat(c.CarbonPct), ptrToNullFloat(c.PPct), ptrToNullFloat(c.SPct),
		b2i(c.HasBendTest), b2i(c.HasIntergranularTest), b2i(c.HasStampPhoto), ptrToNullFloat(c.MinTemperatureC),
		c.DeliveryCondition,
		marshalCorrectedB(c.CorrectedBNumbers), marshalLog(c.CorrectionLog), c.NameOverride,
		string(statusOrDefault(c.Status)), c.ReceivedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	c.ID = id
	return id, err
}

func statusOrDefault(s domain.CertStatus) domain.CertStatus {
	if s == "" {
		return domain.CertMottagen
	}
	return s
}

func (q *Q) GetCert(ctx context.Context, id int64) (*domain.Cert, error) {
	return scanCert(q.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certs WHERE id = ?`, id))
}

func (q *Q) GetCertByHash(ctx context.Context, hash string) (*domain.Cert, error) {
	return scanCert(q.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certs WHERE pdf_hash = ?`, hash))
}

// ListCerts returnerar cert med given status, eller alla om status är tom.
// Nyaste först.
func (q *Q) ListCerts(ctx context.Context, status domain.CertStatus) ([]*domain.Cert, error) {
	query := `SELECT ` + certCols + ` FROM certs`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY received_at DESC, id DESC`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Cert
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCertWork skriver certets ARBETSFÄLT (rättelser, logg, namn-override).
// Råextraktionen och livscykelfälten röres aldrig här — undantag: product_code
// saknar corrected-tvilling och redigeras direkt (jfr name_override).
func (q *Q) UpdateCertWork(ctx context.Context, c *domain.Cert) error {
	res, err := q.db.ExecContext(ctx, `UPDATE certs SET
		corrected_charge=?, corrected_material=?, corrected_product_form=?,
		corrected_dimensions=?, corrected_cert_type=?, corrected_b_numbers=?,
		product_code=?, correction_log=?, name_override=?
		WHERE id = ?`,
		c.CorrectedCharge, c.CorrectedMaterial, c.CorrectedProductForm,
		c.CorrectedDimensions, c.CorrectedCertType, marshalCorrectedB(c.CorrectedBNumbers),
		c.ProductCode, marshalLog(c.CorrectionLog), c.NameOverride, c.ID)
	return oneRow(res, err)
}

// UpdateCertStatus byter livscykelstatus (övergången ska redan vara validerad
// av app-lagret via domain.CertCanTransition).
func (q *Q) UpdateCertStatus(ctx context.Context, id int64, status domain.CertStatus) error {
	res, err := q.db.ExecContext(ctx, `UPDATE certs SET status=? WHERE id = ?`, string(status), id)
	return oneRow(res, err)
}

// UpdateCertSaved stämplar Spara-resultatet och fryser certet.
func (q *Q) UpdateCertSaved(ctx context.Context, id int64, finalFilename, outputPath, savedAt string) error {
	res, err := q.db.ExecContext(ctx, `UPDATE certs SET
		status=?, final_filename=?, output_path=?, saved_at=? WHERE id = ?`,
		string(domain.CertSparad), finalFilename, outputPath, savedAt, id)
	return oneRow(res, err)
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Order rows
// ---------------------------------------------------------------------------

const orderRowCols = `delivery_row_id, purchase_order_id, order_number, supplier_name,
 part_id, part_number, description, extra_description, planned_qty, delivery_date,
 cert_required, delivery_raw, part_raw, delivered, in_monitor, first_seen, last_seen,
 receiving_message, receiving_inspection_instruction, row_goods_label, row_notes,
 supplier_drawing_number, supplier_revision_number, free_text,
 order_goods_label, external_comment, business_contact_order_number,
 alloy_code, alloy_description, part_receiving_instruction, part_purchase_comment,
 part_comment, part_length, part_width, part_height, weight_per_unit,
 goods_type, category_string, extra_fields_raw, hyperlinks, drawing_numbers,
 req_material, req_en_norm, req_cert_type, req_english, req_product_form,
 req_dimensions, req_impact, req_notes, req_parsed_at`

func scanOrderRow(sc rowScanner) (*domain.OrderRow, error) {
	var r domain.OrderRow
	var certReq, delivered, inMonitor, reqEnglish int
	var hyperlinks, reqParsedAt string
	err := sc.Scan(&r.DeliveryRowID, &r.PurchaseOrderID, &r.OrderNumber, &r.SupplierName,
		&r.PartID, &r.PartNumber, &r.Description, &r.ExtraDescription, &r.PlannedQty, &r.DeliveryDate,
		&certReq, &r.DeliveryRaw, &r.PartRaw, &delivered, &inMonitor, &r.FirstSeen, &r.LastSeen,
		&r.ReceivingMessage, &r.ReceivingInspectionInstruction, &r.RowGoodsLabel, &r.RowNotes,
		&r.SupplierDrawingNumber, &r.SupplierRevisionNumber, &r.FreeText,
		&r.OrderGoodsLabel, &r.ExternalComment, &r.BusinessContactOrderNumber,
		&r.AlloyCode, &r.AlloyDescription, &r.PartReceivingInstruction, &r.PartPurchaseComment,
		&r.PartComment, &r.PartLength, &r.PartWidth, &r.PartHeight, &r.WeightPerUnit,
		&r.GoodsType, &r.CategoryString, &r.ExtraFieldsRaw, &hyperlinks, &r.DrawingNumbers,
		&r.Req.Material, &r.Req.EnNorm, &r.Req.CertType, &reqEnglish, &r.Req.ProductForm,
		&r.Req.Dimensions, &r.Req.Impact, &r.Req.Notes, &reqParsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CertRequired = certReq == 1
	r.Delivered = delivered == 1
	r.InMonitor = inMonitor == 1
	r.Hyperlinks = unmarshalHyperlinks(hyperlinks)
	r.Req.English = reqEnglish == 1
	r.ReqParsedAt = parseRowTime(reqParsedAt)
	return &r, nil
}

// parseRowTime/formatRowTime bygger bron mellan order_rows TEXT-tidsstämplar och
// domänens time.Time. Tom sträng ↔ zero time (aldrig parsad). Följer samma
// serialiserings-i-store-lagret-princip som nullFloat/hyperlinks ovan.
func parseRowTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func formatRowTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// UpsertOrderRow skriver/uppdaterar en Monitor-rad. delivered och first_seen
// bevaras medvetet över refresher — det är lokal bokföring. in_monitor sätts
// alltid till 1 (raden sågs i denna sync). De cert-bärande fälten (Task 6) är
// alla syncade Monitor-värden och skrivs om vid varje refresh, precis som
// description/part_raw m.fl. — de rör inte länk-/AI-kolumner (de bor i links).
// De AI-tolkade kravfälten (Task 8, req_*) är med i INSERT (tomma på nya rader)
// men UNDANTAS medvetet ur ON CONFLICT DO UPDATE — precis som delivered/
// first_seen — så en re-sync av en oförändrad rad ALDRIG nollställer kraven.
func (q *Q) UpsertOrderRow(ctx context.Context, r *domain.OrderRow, now string) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO order_rows (`+orderRowCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,
		        ?,?,?,?,
		        ?,?,?,
		        ?,?,?,
		        ?,?,?,?,
		        ?,?,?,?,?,
		        ?,?,?,?,?,
		        ?,?,?,?,?,?,?,?,?)
		ON CONFLICT(delivery_row_id) DO UPDATE SET
			purchase_order_id=excluded.purchase_order_id,
			order_number=excluded.order_number,
			supplier_name=excluded.supplier_name,
			part_id=excluded.part_id,
			part_number=excluded.part_number,
			description=excluded.description,
			extra_description=excluded.extra_description,
			planned_qty=excluded.planned_qty,
			delivery_date=excluded.delivery_date,
			cert_required=excluded.cert_required,
			delivery_raw=excluded.delivery_raw,
			part_raw=excluded.part_raw,
			in_monitor=1,
			last_seen=excluded.last_seen,
			receiving_message=excluded.receiving_message,
			receiving_inspection_instruction=excluded.receiving_inspection_instruction,
			row_goods_label=excluded.row_goods_label,
			row_notes=excluded.row_notes,
			supplier_drawing_number=excluded.supplier_drawing_number,
			supplier_revision_number=excluded.supplier_revision_number,
			free_text=excluded.free_text,
			order_goods_label=excluded.order_goods_label,
			external_comment=excluded.external_comment,
			business_contact_order_number=excluded.business_contact_order_number,
			alloy_code=excluded.alloy_code,
			alloy_description=excluded.alloy_description,
			part_receiving_instruction=excluded.part_receiving_instruction,
			part_purchase_comment=excluded.part_purchase_comment,
			part_comment=excluded.part_comment,
			part_length=excluded.part_length,
			part_width=excluded.part_width,
			part_height=excluded.part_height,
			weight_per_unit=excluded.weight_per_unit,
			goods_type=excluded.goods_type,
			category_string=excluded.category_string,
			extra_fields_raw=excluded.extra_fields_raw,
			hyperlinks=excluded.hyperlinks,
			drawing_numbers=excluded.drawing_numbers`,
		r.DeliveryRowID, r.PurchaseOrderID, r.OrderNumber, r.SupplierName,
		r.PartID, r.PartNumber, r.Description, r.ExtraDescription, r.PlannedQty, r.DeliveryDate,
		b2i(r.CertRequired), r.DeliveryRaw, r.PartRaw, b2i(r.Delivered), now, now,
		r.ReceivingMessage, r.ReceivingInspectionInstruction, r.RowGoodsLabel, r.RowNotes,
		r.SupplierDrawingNumber, r.SupplierRevisionNumber, r.FreeText,
		r.OrderGoodsLabel, r.ExternalComment, r.BusinessContactOrderNumber,
		r.AlloyCode, r.AlloyDescription, r.PartReceivingInstruction, r.PartPurchaseComment,
		r.PartComment, r.PartLength, r.PartWidth, r.PartHeight, r.WeightPerUnit,
		r.GoodsType, r.CategoryString, r.ExtraFieldsRaw, marshalHyperlinks(r.Hyperlinks), r.DrawingNumbers,
		r.Req.Material, r.Req.EnNorm, r.Req.CertType, b2i(r.Req.English), r.Req.ProductForm,
		r.Req.Dimensions, r.Req.Impact, r.Req.Notes, formatRowTime(r.ReqParsedAt))
	return err
}

// SetOrderRowRequirements skriver de AI-tolkade kraven på EN rad (dum CRUD).
// Guard/klocka/notifiering ligger i App.SetRowRequirements — enda skrivvägen.
func (q *Q) SetOrderRowRequirements(ctx context.Context, rowID int64, r domain.RowRequirements, parsedAt string) error {
	res, err := q.db.ExecContext(ctx, `UPDATE order_rows SET
		req_material=?, req_en_norm=?, req_cert_type=?, req_english=?,
		req_product_form=?, req_dimensions=?, req_impact=?, req_notes=?, req_parsed_at=?
		WHERE delivery_row_id = ?`,
		r.Material, r.EnNorm, r.CertType, b2i(r.English),
		r.ProductForm, r.Dimensions, r.Impact, r.Notes, parsedAt, rowID)
	return oneRow(res, err)
}

// MarkAllRowsUnseen nollar in_monitor inför en sync; efterföljande upserts
// sätter tillbaka 1 för rader som fortfarande finns i Monitor-fönstret.
func (q *Q) MarkAllRowsUnseen(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `UPDATE order_rows SET in_monitor = 0`)
	return err
}

func (q *Q) GetOrderRow(ctx context.Context, deliveryRowID int64) (*domain.OrderRow, error) {
	return scanOrderRow(q.db.QueryRowContext(ctx,
		`SELECT `+orderRowCols+` FROM order_rows WHERE delivery_row_id = ?`, deliveryRowID))
}

func (q *Q) ListOrderRows(ctx context.Context) ([]*domain.OrderRow, error) {
	return q.queryOrderRows(ctx, `SELECT `+orderRowCols+` FROM order_rows
		ORDER BY delivery_date, order_number, delivery_row_id`)
}

func (q *Q) RowsByOrderNumber(ctx context.Context, orderNumber string) ([]*domain.OrderRow, error) {
	return q.queryOrderRows(ctx, `SELECT `+orderRowCols+` FROM order_rows
		WHERE order_number = ? ORDER BY delivery_row_id`, orderNumber)
}

func (q *Q) queryOrderRows(ctx context.Context, query string, args ...any) ([]*domain.OrderRow, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.OrderRow
	for rows.Next() {
		r, err := scanOrderRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetDelivered sätter lokal leveransbokföring för en eller flera rader.
func (q *Q) SetDelivered(ctx context.Context, deliveryRowIDs []int64, delivered bool) error {
	if len(deliveryRowIDs) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(deliveryRowIDs)), ",")
	args := make([]any, 0, len(deliveryRowIDs)+1)
	args = append(args, b2i(delivered))
	for _, id := range deliveryRowIDs {
		args = append(args, id)
	}
	_, err := q.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE order_rows SET delivered = ? WHERE delivery_row_id IN (%s)`, ph), args...)
	return err
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

const linkCols = `id, cert_id, delivery_row_id, order_number, status, match_source,
 required_material, required_cert, our_material, material_ok,
 required_product_form, product_form_ok, ai_notes, created_at, updated_at`

func scanLink(sc rowScanner) (*domain.Link, error) {
	var l domain.Link
	var status string
	err := sc.Scan(&l.ID, &l.CertID, &l.DeliveryRowID, &l.OrderNumber, &status, &l.MatchSource,
		&l.RequiredMaterial, &l.RequiredCert, &l.OurMaterial, &l.MaterialOK,
		&l.RequiredProductForm, &l.ProductFormOK, &l.AINotes, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.Status = domain.LinkStatus(status)
	return &l, nil
}

func (q *Q) InsertLink(ctx context.Context, l *domain.Link) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO links
		(cert_id, delivery_row_id, order_number, status, match_source, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		l.CertID, l.DeliveryRowID, l.OrderNumber, string(l.Status), l.MatchSource, l.CreatedAt, l.UpdatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	l.ID = id
	return id, err
}

func (q *Q) GetLink(ctx context.Context, id int64) (*domain.Link, error) {
	return scanLink(q.db.QueryRowContext(ctx, `SELECT `+linkCols+` FROM links WHERE id = ?`, id))
}

// GetLinkByKey slår upp en länk på sin unika nyckel (cert, rad, ordernummer).
func (q *Q) GetLinkByKey(ctx context.Context, certID, deliveryRowID int64, orderNumber string) (*domain.Link, error) {
	return scanLink(q.db.QueryRowContext(ctx,
		`SELECT `+linkCols+` FROM links WHERE cert_id = ? AND delivery_row_id = ? AND order_number = ?`,
		certID, deliveryRowID, orderNumber))
}

func (q *Q) UpdateLinkStatus(ctx context.Context, id int64, status domain.LinkStatus, source, updatedAt string) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE links SET status=?, match_source=?, updated_at=? WHERE id = ?`,
		string(status), source, updatedAt, id)
	return oneRow(res, err)
}

// UpdateLinkDeliveryRow pekar om en länk till en (ny)funnen orderrad — används
// när en fri B-nummerkoppling (delivery_row_id=0) uppgraderas till radnivå.
func (q *Q) UpdateLinkDeliveryRow(ctx context.Context, id, deliveryRowID int64, updatedAt string) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE links SET delivery_row_id=?, updated_at=? WHERE id = ?`,
		deliveryRowID, updatedAt, id)
	return oneRow(res, err)
}

// UpdateLinkVerdict skriver AI-parbedömningen på länken.
func (q *Q) UpdateLinkVerdict(ctx context.Context, l *domain.Link) error {
	res, err := q.db.ExecContext(ctx, `UPDATE links SET
		required_material=?, required_cert=?, our_material=?, material_ok=?,
		required_product_form=?, product_form_ok=?, ai_notes=?, updated_at=?
		WHERE id = ?`,
		l.RequiredMaterial, l.RequiredCert, l.OurMaterial, l.MaterialOK,
		l.RequiredProductForm, l.ProductFormOK, l.AINotes, l.UpdatedAt, l.ID)
	return oneRow(res, err)
}

func (q *Q) ListLinksForCert(ctx context.Context, certID int64) ([]*domain.Link, error) {
	return q.queryLinks(ctx, `SELECT `+linkCols+` FROM links WHERE cert_id = ? ORDER BY id`, certID)
}

func (q *Q) ListLinksForRow(ctx context.Context, deliveryRowID int64) ([]*domain.Link, error) {
	return q.queryLinks(ctx, `SELECT `+linkCols+` FROM links WHERE delivery_row_id = ? ORDER BY id`, deliveryRowID)
}

func (q *Q) ListLinks(ctx context.Context) ([]*domain.Link, error) {
	return q.queryLinks(ctx, `SELECT `+linkCols+` FROM links ORDER BY id`)
}

func (q *Q) queryLinks(ctx context.Context, query string, args ...any) ([]*domain.Link, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ConfirmedOrderNumbers är de bekräftade länkarnas ordernummer för ett cert —
// indata till domain.ProposedFilename.
func (q *Q) ConfirmedOrderNumbers(ctx context.Context, certID int64) ([]string, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT DISTINCT order_number FROM links WHERE cert_id = ? AND status = ? ORDER BY order_number`,
		certID, string(domain.LinkBekraftad))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Emails (intags-audit), AI-cache, app_state, ai_calls
// ---------------------------------------------------------------------------

func (q *Q) InsertEmail(ctx context.Context, filename, subject, from, date, category, status, errMsg, now string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO emails
		(filename, subject, from_addr, date, mail_category, status, error_message, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		filename, subject, from, date, category, status, errMsg, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (q *Q) UpdateEmailStatus(ctx context.Context, id int64, status, errMsg string) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE emails SET status=?, error_message=? WHERE id = ?`, status, errMsg, id)
	return oneRow(res, err)
}

// LatestEmailStatus returnerar senaste status för ett .eml-filnamn (” om
// aldrig sett) — intagets skip-nyckel för filer som slutat i fel.
func (q *Q) LatestEmailStatus(ctx context.Context, filename string) (string, error) {
	var s string
	err := q.db.QueryRowContext(ctx,
		`SELECT status FROM emails WHERE filename = ? ORDER BY id DESC LIMIT 1`, filename).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// ListEmailErrors returnerar intagsfel för UI-bannern.
func (q *Q) ListEmailErrors(ctx context.Context) ([]string, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT filename || ': ' || error_message FROM emails WHERE status = 'error' ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MatchVerdict är den cachade AI-parbedömningen.
type MatchVerdict struct {
	RequiredMaterial    string
	RequiredCert        string
	OurMaterial         string
	MaterialOK          string
	RequiredProductForm string
	ProductFormOK       string
	Notes               string
}

func (q *Q) GetMatchCache(ctx context.Context, key string) (*MatchVerdict, error) {
	var v MatchVerdict
	err := q.db.QueryRowContext(ctx, `SELECT required_material, required_cert, our_material,
		material_ok, required_product_form, product_form_ok, notes
		FROM ai_match_cache WHERE cache_key = ?`, key).
		Scan(&v.RequiredMaterial, &v.RequiredCert, &v.OurMaterial,
			&v.MaterialOK, &v.RequiredProductForm, &v.ProductFormOK, &v.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (q *Q) PutMatchCache(ctx context.Context, key string, v *MatchVerdict, now string) error {
	_, err := q.db.ExecContext(ctx, `INSERT OR REPLACE INTO ai_match_cache
		(cache_key, required_material, required_cert, our_material, material_ok,
		 required_product_form, product_form_ok, notes, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		key, v.RequiredMaterial, v.RequiredCert, v.OurMaterial, v.MaterialOK,
		v.RequiredProductForm, v.ProductFormOK, v.Notes, now)
	return err
}

// GetRequirementsCache/PutRequirementsCache är krav-tolkningscachen (Task 8):
// samma mönster som match-cachen men värdet är RowRequirements som JSON. Nyckeln
// (byggd i monitorsync) täcker artikel + alla kravtexter som skickas till AI:n.
func (q *Q) GetRequirementsCache(ctx context.Context, key string) (*domain.RowRequirements, error) {
	var raw string
	err := q.db.QueryRowContext(ctx,
		`SELECT requirements FROM ai_requirements_cache WHERE cache_key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r domain.RowRequirements
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (q *Q) PutRequirementsCache(ctx context.Context, key string, r *domain.RowRequirements, now string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `INSERT OR REPLACE INTO ai_requirements_cache
		(cache_key, requirements, created_at) VALUES (?,?,?)`, key, string(raw), now)
	return err
}

func (q *Q) GetState(ctx context.Context, key string) (string, error) {
	var v string
	err := q.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (q *Q) SetState(ctx context.Context, key, value string) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO app_state (key, value) VALUES (?,?)`, key, value)
	return err
}

// InsertAICall loggar ett AI-anrop i den enade kostnads-/auditloggen.
func (q *Q) InsertAICall(ctx context.Context, certID int64, step, model string,
	tokensIn, tokensOut, cacheCreate, cacheRead, durationMS int64, success bool, errMsg, now string) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO ai_calls
		(cert_id, step, model, tokens_input, tokens_output, tokens_cache_creation,
		 tokens_cache_read, duration_ms, success, error_message, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		certID, step, model, tokensIn, tokensOut, cacheCreate, cacheRead, durationMS,
		b2i(success), errMsg, now)
	return err
}
