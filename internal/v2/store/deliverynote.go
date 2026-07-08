package store

import (
	"context"
	"database/sql"
	"errors"

	"cert-renamer/internal/v2/domain"
)

// Dum CRUD för följesedlar (delivery_notes). Ingen affärslogik/guard här — den
// bor i internal/v2/app. Följer certs/order_rows-mönstret (cols-const + scan +
// Insert/Get/List/Update), mot både DB och pågående transaktion.

const deliveryNoteCols = `id, image_hash, image_filename, supplier, delivery_date,
 order_number, charge, material, quantity, unit, delivery_note_number,
 waybill_number, b_numbers, confidence, status, matched_po_id, matched_row_id,
 proposed_quantity, created_at`

func scanDeliveryNote(sc rowScanner) (*domain.DeliveryNote, error) {
	var d domain.DeliveryNote
	var bNums, status string
	err := sc.Scan(&d.ID, &d.ImageHash, &d.ImageFilename, &d.Supplier, &d.DeliveryDate,
		&d.OrderNumber, &d.Charge, &d.Material, &d.Quantity, &d.Unit, &d.DeliveryNoteNumber,
		&d.WaybillNumber, &bNums, &d.Confidence, &status, &d.MatchedPOID, &d.MatchedRowID,
		&d.ProposedQuantity, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.BNumbers = unmarshalList(bNums)
	d.Status = domain.DeliveryNoteStatus(status)
	return &d, nil
}

func (q *Q) InsertDeliveryNote(ctx context.Context, d *domain.DeliveryNote) (int64, error) {
	status := d.Status
	if status == "" {
		status = domain.DNMottagen
	}
	res, err := q.db.ExecContext(ctx, `INSERT INTO delivery_notes
		(image_hash, image_filename, supplier, delivery_date, order_number, charge,
		 material, quantity, unit, delivery_note_number, waybill_number, b_numbers,
		 confidence, status, matched_po_id, matched_row_id, proposed_quantity, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ImageHash, d.ImageFilename, d.Supplier, d.DeliveryDate, d.OrderNumber, d.Charge,
		d.Material, d.Quantity, d.Unit, d.DeliveryNoteNumber, d.WaybillNumber, marshalList(d.BNumbers),
		d.Confidence, string(status), d.MatchedPOID, d.MatchedRowID, d.ProposedQuantity, d.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	d.ID = id
	return id, err
}

func (q *Q) GetDeliveryNote(ctx context.Context, id int64) (*domain.DeliveryNote, error) {
	return scanDeliveryNote(q.db.QueryRowContext(ctx,
		`SELECT `+deliveryNoteCols+` FROM delivery_notes WHERE id = ?`, id))
}

func (q *Q) GetDeliveryNoteByHash(ctx context.Context, hash string) (*domain.DeliveryNote, error) {
	return scanDeliveryNote(q.db.QueryRowContext(ctx,
		`SELECT `+deliveryNoteCols+` FROM delivery_notes WHERE image_hash = ?`, hash))
}

// ListDeliveryNotes returnerar följesedlar med given status, eller alla om
// status är tom. Nyaste först.
func (q *Q) ListDeliveryNotes(ctx context.Context, status domain.DeliveryNoteStatus) ([]*domain.DeliveryNote, error) {
	query := `SELECT ` + deliveryNoteCols + ` FROM delivery_notes`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.DeliveryNote
	for rows.Next() {
		d, err := scanDeliveryNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateDeliveryNoteMatch skriver de nullbara matchnings-DATAFÄLTEN (ingen
// statusändring — matchning är inte ett livscykelskede).
func (q *Q) UpdateDeliveryNoteMatch(ctx context.Context, id, poID, rowID int64, proposedQty float64) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE delivery_notes SET matched_po_id=?, matched_row_id=?, proposed_quantity=? WHERE id = ?`,
		poID, rowID, proposedQty, id)
	return oneRow(res, err)
}

// UpdateDeliveryNoteStatus byter livscykelstatus (övergången ska redan vara
// validerad av app-lagret via domain.DeliveryNoteCanTransition).
func (q *Q) UpdateDeliveryNoteStatus(ctx context.Context, id int64, status domain.DeliveryNoteStatus) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE delivery_notes SET status=? WHERE id = ?`, string(status), id)
	return oneRow(res, err)
}
