package store

// Sickans minne: noter per inleveransrad, inlärda arbetsregler och att-göra-listan.
// Allt är vanliga tabeller — synligt och raderbart i UI:t, inget dolt tillstånd.

import "database/sql"

// --- Noter på kommande inleveranser ---

// UpcomingNote är en not på en inleveransrad ("ringde 2/7, cert kommer med
// nästa sändning"). Author är "rob" (UI) eller "sickan" (verktyg).
type UpcomingNote struct {
	ID            int64  `json:"id"`
	DeliveryRowID int64  `json:"delivery_row_id,string"`
	OrderNumber   string `json:"order_number"`
	PartNumber    string `json:"part_number"`
	Author        string `json:"author"`
	Text          string `json:"text"`
	CreatedAt     string `json:"created_at"`
}

// AddUpcomingNote skapar en not. Order-/artikelnummer läses från raden om den
// finns (så noten är läsbar även om raden senare försvinner ur fönstret).
func (r *Repository) AddUpcomingNote(deliveryRowID int64, author, text string) (int64, error) {
	var orderNo, partNo string
	if row, err := r.GetUpcomingByRowID(deliveryRowID); err == nil && row != nil {
		orderNo, partNo = row.OrderNumber, row.PartNumber
	}
	res, err := r.db.Exec(`
		INSERT INTO upcoming_notes (delivery_row_id, order_number, part_number, author, text)
		VALUES (?, ?, ?, ?, ?)`, deliveryRowID, orderNo, partNo, author, text)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteUpcomingNote tar bort en not.
func (r *Repository) DeleteUpcomingNote(id int64) error {
	_, err := r.db.Exec(`DELETE FROM upcoming_notes WHERE id = ?`, id)
	return err
}

// ListUpcomingNotes returnerar alla noter, äldst först (kronologisk läsning).
func (r *Repository) ListUpcomingNotes() ([]UpcomingNote, error) {
	rows, err := r.db.Query(`
		SELECT id, delivery_row_id, order_number, part_number, author, text, created_at
		FROM upcoming_notes ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingNote
	for rows.Next() {
		var n UpcomingNote
		if err := rows.Scan(&n.ID, &n.DeliveryRowID, &n.OrderNumber, &n.PartNumber, &n.Author, &n.Text, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// attachUpcomingNotes fyller RowNotes-fältet på raderna (en grupperad query).
func (r *Repository) attachUpcomingNotes(deliveries []UpcomingDelivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	notes, err := r.ListUpcomingNotes()
	if err != nil {
		return err
	}
	byRow := map[int64][]UpcomingNote{}
	for _, n := range notes {
		byRow[n.DeliveryRowID] = append(byRow[n.DeliveryRowID], n)
	}
	for i := range deliveries {
		deliveries[i].RowNotes = byRow[deliveries[i].DeliveryRowID]
	}
	return nil
}

// --- Inlärda arbetsregler ---

// AgentRule är en arbetsregel Sickan följer (injiceras i systemprompten).
type AgentRule struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
}

// AddAgentRule sparar en regel. source: "rob" (uttryckt i chatten/UI) eller
// "sickan" (egen lärdom) eller "seed".
func (r *Repository) AddAgentRule(text, source string) (int64, error) {
	res, err := r.db.Exec(`INSERT INTO agent_rules (text, source) VALUES (?, ?)`, text, source)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAgentRules returnerar aktiva regler, äldst först.
func (r *Repository) ListAgentRules() ([]AgentRule, error) {
	rows, err := r.db.Query(`SELECT id, text, source, created_at FROM agent_rules WHERE active = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRule
	for rows.Next() {
		var a AgentRule
		if err := rows.Scan(&a.ID, &a.Text, &a.Source, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DisableAgentRule inaktiverar en regel (mjuk radering — historiken består).
func (r *Repository) DisableAgentRule(id int64) error {
	_, err := r.db.Exec(`UPDATE agent_rules SET active = 0 WHERE id = ?`, id)
	return err
}

// --- Att-göra-listan ---

// Task-status.
const (
	TaskOpen = "open"
	TaskDone = "done"
)

// Task är en att-göra-post ("glöm inte att X"), med valfritt datum och
// orderkoppling. Visas i morgonbriefen.
type Task struct {
	ID            int64  `json:"id"`
	Text          string `json:"text"`
	Status        string `json:"status"`
	DueDate       string `json:"due_date"`
	OrderNumber   string `json:"order_number"`
	DeliveryRowID int64  `json:"delivery_row_id,string"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
	DoneAt        string `json:"done_at"`
}

// AddTask skapar en task.
func (r *Repository) AddTask(t Task) (int64, error) {
	if t.Status == "" {
		t.Status = TaskOpen
	}
	res, err := r.db.Exec(`
		INSERT INTO tasks (text, status, due_date, order_number, delivery_row_id, source)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.Text, t.Status, t.DueDate, t.OrderNumber, t.DeliveryRowID, t.Source)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListTasks returnerar tasks; openOnly filtrerar bort avbockade. Sortering:
// förfallodatum först (utan datum sist), sedan skapelseordning.
func (r *Repository) ListTasks(openOnly bool) ([]Task, error) {
	q := `SELECT id, text, status, due_date, order_number, delivery_row_id, source, created_at, done_at FROM tasks`
	if openOnly {
		q += ` WHERE status = 'open'`
	}
	q += ` ORDER BY CASE WHEN due_date = '' THEN 1 ELSE 0 END, due_date ASC, id ASC`
	rows, err := r.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Status, &t.DueDate, &t.OrderNumber, &t.DeliveryRowID, &t.Source, &t.CreatedAt, &t.DoneAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask hämtar en task; nil utan fel om den saknas.
func (r *Repository) GetTask(id int64) (*Task, error) {
	var t Task
	err := r.db.QueryRow(`SELECT id, text, status, due_date, order_number, delivery_row_id, source, created_at, done_at FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Text, &t.Status, &t.DueDate, &t.OrderNumber, &t.DeliveryRowID, &t.Source, &t.CreatedAt, &t.DoneAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CompleteTask bockar av en task.
func (r *Repository) CompleteTask(id int64) error {
	_, err := r.db.Exec(`UPDATE tasks SET status = 'done', done_at = datetime('now') WHERE id = ?`, id)
	return err
}

// DeleteTask tar bort en task.
func (r *Repository) DeleteTask(id int64) error {
	_, err := r.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}
