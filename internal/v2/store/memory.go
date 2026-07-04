package store

import (
	"context"
	"database/sql"
	"errors"

	"cert-renamer/internal/v2/domain"
)

// Sickans och Robs delade minne: noteringar, lärda regler och att-göra.
// Samma dumma CRUD-stil som resten av Q.

// Note är en notering på en orderrad eller ett cert.
type Note struct {
	ID          int64  `json:"id,string"`
	Kind        string `json:"kind"` // "order_row" | "cert"
	RefID       int64  `json:"ref_id,string"`
	OrderNumber string `json:"order_number"`
	PartNumber  string `json:"part_number"`
	Author      string `json:"author"`
	Text        string `json:"text"`
	CreatedAt   string `json:"created_at"`
}

func (q *Q) AddNote(ctx context.Context, n *Note) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO notes
		(kind, ref_id, order_number, part_number, author, text, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		n.Kind, n.RefID, n.OrderNumber, n.PartNumber, n.Author, n.Text, n.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	n.ID = id
	return id, err
}

func (q *Q) ListNotes(ctx context.Context, kind string, refID int64) ([]*Note, error) {
	return q.queryNotes(ctx, `SELECT id, kind, ref_id, order_number, part_number, author, text, created_at
		FROM notes WHERE kind = ? AND ref_id = ? ORDER BY id`, kind, refID)
}

func (q *Q) ListAllNotes(ctx context.Context) ([]*Note, error) {
	return q.queryNotes(ctx, `SELECT id, kind, ref_id, order_number, part_number, author, text, created_at
		FROM notes ORDER BY id`)
}

func (q *Q) queryNotes(ctx context.Context, query string, args ...any) ([]*Note, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Kind, &n.RefID, &n.OrderNumber, &n.PartNumber,
			&n.Author, &n.Text, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (q *Q) DeleteNote(ctx context.Context, id int64) error {
	res, err := q.db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id)
	return oneRow(res, err)
}

// Rule är en lärd regel för Sickan.
type Rule struct {
	ID        int64  `json:"id,string"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

func (q *Q) AddRule(ctx context.Context, text, source, now string) (int64, error) {
	res, err := q.db.ExecContext(ctx,
		`INSERT INTO agent_rules (text, source, created_at) VALUES (?,?,?)`, text, source, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (q *Q) ListRules(ctx context.Context, activeOnly bool) ([]*Rule, error) {
	query := `SELECT id, text, source, active, created_at FROM agent_rules`
	if activeOnly {
		query += ` WHERE active = 1`
	}
	query += ` ORDER BY id`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		var r Rule
		var active int
		if err := rows.Scan(&r.ID, &r.Text, &r.Source, &active, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (q *Q) RemoveRule(ctx context.Context, id int64) error {
	res, err := q.db.ExecContext(ctx, `DELETE FROM agent_rules WHERE id = ?`, id)
	return oneRow(res, err)
}

// Task är en att-göra-post.
type Task struct {
	ID          int64  `json:"id,string"`
	Text        string `json:"text"`
	Status      string `json:"status"` // open | done
	DueDate     string `json:"due_date"`
	OrderNumber string `json:"order_number"`
	Source      string `json:"source"`
	CreatedAt   string `json:"created_at"`
	DoneAt      string `json:"done_at"`
}

func (q *Q) AddTask(ctx context.Context, t *Task) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO tasks
		(text, status, due_date, order_number, source, created_at)
		VALUES (?, 'open', ?, ?, ?, ?)`,
		t.Text, t.DueDate, t.OrderNumber, t.Source, t.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	t.ID = id
	return id, err
}

func (q *Q) ListTasks(ctx context.Context, status string) ([]*Task, error) {
	query := `SELECT id, text, status, due_date, order_number, source, created_at, done_at FROM tasks`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Status, &t.DueDate, &t.OrderNumber,
			&t.Source, &t.CreatedAt, &t.DoneAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (q *Q) CompleteTask(ctx context.Context, id int64, doneAt string) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE tasks SET status='done', done_at=? WHERE id = ?`, doneAt, id)
	return oneRow(res, err)
}

func (q *Q) DeleteTask(ctx context.Context, id int64) error {
	res, err := q.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	return oneRow(res, err)
}

// GetTask finns för symmetri/verktyg.
func (q *Q) GetTask(ctx context.Context, id int64) (*Task, error) {
	var t Task
	err := q.db.QueryRowContext(ctx,
		`SELECT id, text, status, due_date, order_number, source, created_at, done_at
		 FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Text, &t.Status, &t.DueDate, &t.OrderNumber, &t.Source, &t.CreatedAt, &t.DoneAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
