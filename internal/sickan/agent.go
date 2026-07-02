package sickan

// Sickans "arbetskamrat"-verktyg: kommande inleveranser (läge + noter),
// markera levererad, avvikelsemail-utkast, arbetsregler och att-göra-listan.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/store"
)

var listUpcomingTool = anthropic.ToolParam{
	Name:        "list_upcoming",
	Description: anthropic.String("Listar kommande inleveranser (samma som fliken): order, leverantör, artikel, leveransdatum, leveransstatus, cert-status, materialdom och NOTER. Läs alltid noterna innan du föreslår något — jobba inte om sådant som redan är känt. Filtrera valfritt på order_number."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number": map[string]any{"type": "string", "description": "Valfritt: bara denna order."},
		},
	},
}

var addUpcomingNoteTool = anthropic.ToolParam{
	Name:        "add_upcoming_note",
	Description: anthropic.String("Skriver en not på en inleveransrad — det gemensamma minnet ('ringde 2/7, cert kommer nästa vecka'). Skriv en not när du lärt dig något nytt om en rad, inte för att upprepa vad som redan står."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"delivery_row_id": map[string]any{"type": "string", "description": "Radens delivery_row_id (sträng)."},
			"text":            map[string]any{"type": "string", "description": "Noten."},
		},
		Required: []string{"delivery_row_id", "text"},
	},
}

var getUpcomingNotesTool = anthropic.ToolParam{
	Name:        "get_upcoming_notes",
	Description: anthropic.String("Läser noter på inleveranser. Filtrera valfritt på order_number. Använd för att kolla vad som redan är känt innan du föreslår åtgärder."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number": map[string]any{"type": "string", "description": "Valfritt: bara noter för denna order."},
		},
	},
}

var markDeliveredTool = anthropic.ToolParam{
	Name:        "mark_delivered",
	Description: anthropic.String("Markerar inleveransrader som levererade (samma som ✓-knappen i UI:t). Kräver Robs uttryckliga ja i förra meddelandet. OBS: detta är UI-bokföring — själva inleveransregistreringen i Monitor görs via monitor_ui_report_arrival."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"delivery_row_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Radernas delivery_row_id (strängar)."},
		},
		Required: []string{"delivery_row_ids"},
	},
}

var composeDeviationMailTool = anthropic.ToolParam{
	Name:        "compose_deviation_mail",
	Description: anthropic.String("Bygger ett färdigt mailutkast (mailto) till avvikelseadressen för en order: kind='rest' (positioner som inte levererats in) eller kind='cert_missing' (positioner som saknar cert). Skickar INGET — Rob öppnar och skickar själv. Inkludera mailto-länken i ditt svar."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number": map[string]any{"type": "string", "description": "Ordernumret."},
			"kind":         map[string]any{"type": "string", "enum": []string{"rest", "cert_missing"}, "description": "Utkasttyp."},
		},
		Required: []string{"order_number", "kind"},
	},
}

var rememberRuleTool = anthropic.ToolParam{
	Name:        "remember_rule",
	Description: anthropic.String("Sparar en arbetsregel du ska följa framåt (injiceras i din systemprompt). Använd DIREKT när Rob uttrycker ett arbetssätt ('vi gör aldrig X', 'vänta alltid med Y') — fråga inte först, men nämn i svaret att du sparat den. Reglerna syns och kan raderas i ⚙️."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"text": map[string]any{"type": "string", "description": "Regeln, kort och operativ."},
		},
		Required: []string{"text"},
	},
}

var listRulesTool = anthropic.ToolParam{
	Name:        "list_rules",
	Description: anthropic.String("Listar dina inlärda arbetsregler."),
	InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
}

var addTaskTool = anthropic.ToolParam{
	Name:        "add_task",
	Description: anthropic.String("Lägger till en post på att-göra-listan ('glöm inte att X'). Använd när Rob ber dig komma ihåg något, eller när du själv ser något som behöver göras senare. due_date (YYYY-MM-DD) och order_number är valfria. Visas i morgonbriefen."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"text":         map[string]any{"type": "string", "description": "Vad som ska göras."},
			"due_date":     map[string]any{"type": "string", "description": "Valfritt: YYYY-MM-DD."},
			"order_number": map[string]any{"type": "string", "description": "Valfritt: kopplad order."},
		},
		Required: []string{"text"},
	},
}

var listTasksTool = anthropic.ToolParam{
	Name:        "list_tasks",
	Description: anthropic.String("Listar öppna poster på att-göra-listan."),
	InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
}

var completeTaskTool = anthropic.ToolParam{
	Name:        "complete_task",
	Description: anthropic.String("Bockar av en task. Kräver Robs ja om det inte är din egen task."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"id": map[string]any{"type": "integer", "description": "Taskens id."},
		},
		Required: []string{"id"},
	},
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// upcomingCompact är den token-snåla vyn av en rad för list_upcoming.
type upcomingCompact struct {
	DeliveryRowID string               `json:"delivery_row_id"`
	OrderNumber   string               `json:"order_number"`
	Supplier      string               `json:"supplier"`
	PartNumber    string               `json:"part_number"`
	Description   string               `json:"description,omitempty"`
	Qty           float64              `json:"qty,omitempty"`
	DeliveryDate  string               `json:"delivery_date"`
	LocalStatus   string               `json:"local_status"`
	CertStatus    string               `json:"cert_status"`
	MaterialOK    string               `json:"material_ok,omitempty"`
	RequiredCert  string               `json:"required_cert,omitempty"`
	Notes         []store.UpcomingNote `json:"notes,omitempty"`
}

func (tb *Toolbox) listUpcoming(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return `{"rows":[],"count":0}`, nil
	}
	var args struct {
		OrderNumber string `json:"order_number"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", err
		}
	}
	rows, err := tb.Repo.ListUpcoming()
	if err != nil {
		return "", err
	}
	out := make([]upcomingCompact, 0, len(rows))
	for _, r := range rows {
		if args.OrderNumber != "" && r.OrderNumber != args.OrderNumber {
			continue
		}
		out = append(out, upcomingCompact{
			DeliveryRowID: strconv.FormatInt(r.DeliveryRowID, 10),
			OrderNumber:   r.OrderNumber,
			Supplier:      r.SupplierName,
			PartNumber:    r.PartNumber,
			Description:   r.Description,
			Qty:           r.PlannedQty,
			DeliveryDate:  r.DeliveryDate,
			LocalStatus:   r.LocalStatus,
			CertStatus:    r.CertStatus,
			MaterialOK:    r.MaterialOK,
			RequiredCert:  r.RequiredCert,
			Notes:         r.RowNotes,
		})
	}
	b, _ := json.Marshal(map[string]any{"rows": out, "count": len(out)})
	return string(b), nil
}

func (tb *Toolbox) addUpcomingNote(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		DeliveryRowID string `json:"delivery_row_id"`
		Text          string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	id, err := strconv.ParseInt(args.DeliveryRowID, 10, 64)
	if err != nil || id == 0 {
		return "", fmt.Errorf("ogiltigt delivery_row_id: %q", args.DeliveryRowID)
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("tom not")
	}
	noteID, err := tb.Repo.AddUpcomingNote(id, "sickan", strings.TrimSpace(args.Text))
	if err != nil {
		return "", err
	}
	tb.N.BroadcastUpcoming()
	tb.N.Logf("🤖 Sickan skrev not på rad %d: %s", id, args.Text)
	out, _ := json.Marshal(map[string]any{"ok": true, "note_id": noteID})
	return string(out), nil
}

func (tb *Toolbox) getUpcomingNotes(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return `{"notes":[],"count":0}`, nil
	}
	var args struct {
		OrderNumber string `json:"order_number"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", err
		}
	}
	notes, err := tb.Repo.ListUpcomingNotes()
	if err != nil {
		return "", err
	}
	if args.OrderNumber != "" {
		filtered := notes[:0]
		for _, n := range notes {
			if n.OrderNumber == args.OrderNumber {
				filtered = append(filtered, n)
			}
		}
		notes = filtered
	}
	b, _ := json.Marshal(map[string]any{"notes": notes, "count": len(notes)})
	return string(b), nil
}

func (tb *Toolbox) markDelivered(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		DeliveryRowIDs []string `json:"delivery_row_ids"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ids := make([]int64, 0, len(args.DeliveryRowIDs))
	for _, raw := range args.DeliveryRowIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id == 0 {
			return "", fmt.Errorf("ogiltigt delivery_row_id: %q", raw)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("inga delivery_row_ids angivna")
	}
	if err := tb.Repo.MarkUpcomingDeliveredMany(ids); err != nil {
		return "", err
	}
	tb.N.BroadcastUpcoming()
	tb.N.Logf("🤖 Sickan markerade %d rad(er) som levererade", len(ids))
	out, _ := json.Marshal(map[string]any{"ok": true, "marked": len(ids)})
	return string(out), nil
}

func (tb *Toolbox) composeDeviationMail(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		OrderNumber string `json:"order_number"`
		Kind        string `json:"kind"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	all, err := tb.Repo.ListUpcoming()
	if err != nil {
		return "", err
	}
	var rows []store.UpcomingDelivery
	for _, r := range all {
		if r.OrderNumber == args.OrderNumber {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("ordern %q finns inte bland kommande inleveranser", args.OrderNumber)
	}
	mail, err := store.BuildDeviationMail(rows, args.Kind, tb.Cfg.ReportEmail)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(mail)
	return string(b), nil
}

func (tb *Toolbox) rememberRule(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("tom regel")
	}
	id, err := tb.Repo.AddAgentRule(strings.TrimSpace(args.Text), "sickan")
	if err != nil {
		return "", err
	}
	tb.N.Logf("🤖 Sickan lärde sig en regel: %s", args.Text)
	out, _ := json.Marshal(map[string]any{"ok": true, "rule_id": id})
	return string(out), nil
}

func (tb *Toolbox) listRules() (string, error) {
	if tb.Repo == nil {
		return `{"rules":[],"count":0}`, nil
	}
	rules, err := tb.Repo.ListAgentRules()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"rules": rules, "count": len(rules)})
	return string(b), nil
}

func (tb *Toolbox) addTask(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		Text        string `json:"text"`
		DueDate     string `json:"due_date"`
		OrderNumber string `json:"order_number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("tom task")
	}
	id, err := tb.Repo.AddTask(store.Task{
		Text:        strings.TrimSpace(args.Text),
		DueDate:     args.DueDate,
		OrderNumber: args.OrderNumber,
		Source:      "sickan",
	})
	if err != nil {
		return "", err
	}
	tb.N.BroadcastUpcoming()
	tb.N.Logf("🤖 Sickan la till på att-göra-listan: %s", args.Text)
	out, _ := json.Marshal(map[string]any{"ok": true, "task_id": id})
	return string(out), nil
}

func (tb *Toolbox) listTasks() (string, error) {
	if tb.Repo == nil {
		return `{"tasks":[],"count":0}`, nil
	}
	tasks, err := tb.Repo.ListTasks(true)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"tasks": tasks, "count": len(tasks)})
	return string(b), nil
}

func (tb *Toolbox) completeTask(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	t, err := tb.Repo.GetTask(args.ID)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("task %d finns inte", args.ID)
	}
	if err := tb.Repo.CompleteTask(args.ID); err != nil {
		return "", err
	}
	tb.N.BroadcastUpcoming()
	tb.N.Logf("🤖 Sickan bockade av: %s", t.Text)
	out, _ := json.Marshal(map[string]any{"ok": true, "completed": t.Text})
	return string(out), nil
}
