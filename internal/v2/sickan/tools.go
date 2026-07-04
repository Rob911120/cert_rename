package sickan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/monitor"
	"cert-renamer/internal/v2/store"
)

// Toolbox knyter ihop app-servicen (den enda skrivvägen — verktygen är tunna
// adaptrar över den, så guards, rättelselogg och SSE-ping följer med gratis),
// konfig och lazy Monitor-klient för en chat-session.
type Toolbox struct {
	App            *app.App
	Cfg            func() store.Config
	Monitor        *monitor.Client
	MonitorConnect func() (*monitor.Client, error)

	Rules    []string
	ReadOnly bool // proaktiva körningar: muterande verktyg spärras
}

// mutatingTools spärras i läs-läge. propose_name är läsande utom när
// override-argumentet används (guardas i verktyget).
var mutatingTools = map[string]bool{
	"update_cert":    true,
	"link_cert":      true,
	"unlink_cert":    true,
	"mark_delivered": true,
	"complete_task":  true,
}

// DispatchResult är resultatet av en tool-körning.
type DispatchResult struct {
	Content []anthropic.ToolResultBlockParamContentUnion
	Summary string
}

func textResult(s string) DispatchResult {
	return DispatchResult{
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfText: &anthropic.TextBlockParam{Text: s}},
		},
		Summary: s,
	}
}

func wrapText(s string, err error) (DispatchResult, error) {
	if err != nil {
		return DispatchResult{}, err
	}
	return textResult(s), nil
}

// Dispatch kör en namngiven tool med JSON-input.
func (tb *Toolbox) Dispatch(name string, input json.RawMessage) (DispatchResult, error) {
	if tb.ReadOnly && mutatingTools[name] {
		return DispatchResult{}, fmt.Errorf("verktyget %s är spärrat i läs-läge — föreslå åtgärden i din sammanfattning istället", name)
	}
	switch name {
	case "list_overview":
		return wrapText(tb.listOverview())
	case "get_cert":
		return wrapText(tb.getCert(input))
	case "update_cert":
		return wrapText(tb.updateCert(input))
	case "link_cert":
		return wrapText(tb.linkCert(input))
	case "unlink_cert":
		return wrapText(tb.unlinkCert(input))
	case "propose_name":
		return wrapText(tb.proposeName(input))
	case "read_pdf":
		return tb.readPdf(input)
	case "monitor_find_purchase_order":
		return wrapText(tb.monitorFindPurchaseOrder(input))
	case "monitor_find_supplier":
		return wrapText(tb.monitorFindSupplier(input))
	case "monitor_lookup_charge":
		return wrapText(tb.monitorLookupCharge(input))
	case "add_note":
		return wrapText(tb.addNote(input))
	case "get_notes":
		return wrapText(tb.getNotes(input))
	case "mark_delivered":
		return wrapText(tb.markDelivered(input))
	case "compose_deviation_mail":
		return wrapText(tb.composeDeviationMail(input))
	case "remember_rule":
		return wrapText(tb.rememberRule(input))
	case "list_rules":
		return wrapText(tb.listRules())
	case "add_task":
		return wrapText(tb.addTask(input))
	case "list_tasks":
		return wrapText(tb.listTasks())
	case "complete_task":
		return wrapText(tb.completeTask(input))
	default:
		return DispatchResult{}, fmt.Errorf("okänt verktyg: %s", name)
	}
}

func bg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// ---------------------------------------------------------------------------
// Läsverktyg
// ---------------------------------------------------------------------------

func (tb *Toolbox) listOverview() (string, error) {
	ctx, cancel := bg()
	defer cancel()
	rows, err := tb.App.Repo.ListOrderRows(ctx)
	if err != nil {
		return "", err
	}
	type linkView struct {
		LinkID           int64  `json:"link_id"`
		CertID           int64  `json:"cert_id"`
		OriginalFilename string `json:"original_filename"`
		Status           string `json:"status"`
		MaterialOK       string `json:"material_ok"`
	}
	type rowView struct {
		DeliveryRowID int64      `json:"delivery_row_id"`
		OrderNumber   string     `json:"order_number"`
		PartNumber    string     `json:"part_number"`
		Description   string     `json:"description"`
		DeliveryDate  string     `json:"delivery_date"`
		CertRequired  bool       `json:"cert_required"`
		Delivered     bool       `json:"delivered"`
		Links         []linkView `json:"links"`
	}
	outRows := make([]rowView, 0, len(rows))
	for _, r := range rows {
		rv := rowView{DeliveryRowID: r.DeliveryRowID, OrderNumber: r.OrderNumber,
			PartNumber: r.PartNumber, Description: r.Description, DeliveryDate: r.DeliveryDate,
			CertRequired: r.CertRequired, Delivered: r.Delivered}
		links, _ := tb.App.Repo.ListLinksForRow(ctx, r.DeliveryRowID)
		for _, l := range links {
			if l.Status == domain.LinkAvfardad {
				continue
			}
			lv := linkView{LinkID: l.ID, CertID: l.CertID, Status: string(l.Status), MaterialOK: l.MaterialOK}
			if c, err := tb.App.Repo.GetCert(ctx, l.CertID); err == nil {
				lv.OriginalFilename = c.OriginalFilename
			}
			rv.Links = append(rv.Links, lv)
		}
		outRows = append(outRows, rv)
	}
	type unlinkedView struct {
		CertID           int64    `json:"cert_id"`
		OriginalFilename string   `json:"original_filename"`
		Charge           string   `json:"charge"`
		Material         string   `json:"material"`
		BNumbers         []string `json:"b_numbers"`
		ProposedFilename string   `json:"proposed_filename"`
	}
	living, err := tb.App.Repo.ListCerts(ctx, domain.CertMottagen)
	if err != nil {
		return "", err
	}
	var unlinked []unlinkedView
	for _, c := range living {
		confirmed, _ := tb.App.Repo.ConfirmedOrderNumbers(ctx, c.ID)
		if len(confirmed) > 0 {
			continue
		}
		unlinked = append(unlinked, unlinkedView{
			CertID: c.ID, OriginalFilename: c.OriginalFilename,
			Charge: c.EffectiveCharge(), Material: c.EffectiveMaterial(),
			BNumbers:         c.EffectiveBNumbers(),
			ProposedFilename: domain.ProposedFilename(c, nil),
		})
	}
	out, _ := json.Marshal(map[string]any{"order_rows": outRows, "unlinked_certs": unlinked})
	return string(out), nil
}

func (tb *Toolbox) getCert(input json.RawMessage) (string, error) {
	var args struct {
		CertID int64 `json:"cert_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	view, err := tb.App.GetCertView(ctx, args.CertID)
	if err != nil {
		return "", err
	}
	c := view.Cert
	out, _ := json.Marshal(map[string]any{
		"cert_id": c.ID, "status": string(c.Status),
		"original_filename": c.OriginalFilename,
		"raw": map[string]any{"charge": c.Charge, "material": c.Material, "cert_type": c.CertType,
			"product_form": c.ProductForm, "dimensions": c.Dimensions, "b_numbers": c.BNumbers,
			"confidence": c.Confidence, "issues": c.Issues},
		"corrected": map[string]any{"charge": c.CorrectedCharge, "material": c.CorrectedMaterial,
			"cert_type": c.CorrectedCertType, "product_form": c.CorrectedProductForm,
			"dimensions": c.CorrectedDimensions, "b_numbers": c.CorrectedBNumbers},
		"effective": map[string]any{"charge": c.EffectiveCharge(), "material": c.EffectiveMaterial(),
			"cert_type": c.EffectiveCertType(), "product_form": c.EffectiveProductForm(),
			"dimensions": c.EffectiveDimensions(), "b_numbers": c.EffectiveBNumbers()},
		"name_override": c.NameOverride, "proposed_filename": view.ProposedFilename,
		"final_filename": c.FinalFilename, "links": view.Links,
		"correction_log": c.CorrectionLog,
	})
	return string(out), nil
}

// ---------------------------------------------------------------------------
// Muterande verktyg (via app — guards + logg + SSE gratis)
// ---------------------------------------------------------------------------

func (tb *Toolbox) updateCert(input json.RawMessage) (string, error) {
	var args struct {
		CertID int64  `json:"cert_id"`
		Field  string `json:"field"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	view, err := tb.App.UpdateCertField(ctx, args.CertID, args.Field, args.Value, "sickan")
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"ok": true, "field": args.Field, "value": args.Value,
		"proposed_filename": view.ProposedFilename,
	})
	return string(out), nil
}

func (tb *Toolbox) linkCert(input json.RawMessage) (string, error) {
	var args struct {
		CertID        int64  `json:"cert_id"`
		DeliveryRowID int64  `json:"delivery_row_id"`
		OrderNumber   string `json:"order_number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	link, err := tb.App.ConfirmLink(ctx, args.CertID, args.DeliveryRowID, args.OrderNumber, "sickan")
	if err != nil {
		return "", err
	}
	view, _ := tb.App.GetCertView(ctx, args.CertID)
	proposed := ""
	if view != nil {
		proposed = view.ProposedFilename
	}
	out, _ := json.Marshal(map[string]any{
		"ok": true, "link_id": link.ID, "order_number": link.OrderNumber,
		"status": string(link.Status), "proposed_filename": proposed,
	})
	return string(out), nil
}

func (tb *Toolbox) unlinkCert(input json.RawMessage) (string, error) {
	var args struct {
		LinkID int64 `json:"link_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	if err := tb.App.RejectLink(ctx, args.LinkID); err != nil {
		return "", err
	}
	return `{"ok":true}`, nil
}

func (tb *Toolbox) proposeName(input json.RawMessage) (string, error) {
	var args struct {
		CertID   int64   `json:"cert_id"`
		Override *string `json:"override"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	if args.Override != nil {
		if tb.ReadOnly {
			return "", fmt.Errorf("override är spärrad i läs-läge")
		}
		view, err := tb.App.SetNameOverride(ctx, args.CertID, *args.Override)
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]any{"proposed_filename": view.ProposedFilename, "override_set": *args.Override != ""})
		return string(out), nil
	}
	view, err := tb.App.GetCertView(ctx, args.CertID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"proposed_filename": view.ProposedFilename,
		"confirmed_orders":  view.ConfirmedOrders,
		"name_override":     view.Cert.NameOverride,
	})
	return string(out), nil
}

func (tb *Toolbox) readPdf(input json.RawMessage) (DispatchResult, error) {
	var args struct {
		CertID int64 `json:"cert_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return DispatchResult{}, err
	}
	ctx, cancel := bg()
	defer cancel()
	c, err := tb.App.Repo.GetCert(ctx, args.CertID)
	if err != nil {
		return DispatchResult{}, err
	}
	data, err := os.ReadFile(store.StorePath(tb.Cfg(), c.StoredName))
	if err != nil {
		return DispatchResult{}, fmt.Errorf("läsa lagerfil: %w", err)
	}
	if len(data) > 20<<20 {
		return DispatchResult{}, fmt.Errorf("PDF:en är för stor (%d MB)", len(data)>>20)
	}
	return DispatchResult{
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfText: &anthropic.TextBlockParam{Text: "PDF: " + c.OriginalFilename}},
			{OfDocument: &anthropic.DocumentBlockParam{
				Source: anthropic.DocumentBlockParamSourceUnion{
					OfBase64: &anthropic.Base64PDFSourceParam{
						Data: base64.StdEncoding.EncodeToString(data),
					},
				},
			}},
		},
		Summary: "läser " + c.OriginalFilename,
	}, nil
}

// ---------------------------------------------------------------------------
// Monitor (read-only, lazy login)
// ---------------------------------------------------------------------------

func (tb *Toolbox) monitorReady() error {
	if tb.Monitor != nil {
		return nil
	}
	if tb.MonitorConnect != nil {
		mc, err := tb.MonitorConnect()
		if err != nil {
			return err
		}
		tb.Monitor = mc
	}
	if tb.Monitor == nil {
		return fmt.Errorf("Monitor är inte konfigurerad — öppna ⚙️ Inställningar")
	}
	return nil
}

func (tb *Toolbox) monitorFindPurchaseOrder(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	var args struct {
		OrderNumber string `json:"order_number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if args.OrderNumber == "" {
		return "", fmt.Errorf("order_number krävs")
	}
	ctx, cancel := bg()
	defer cancel()
	po, err := tb.Monitor.FindPurchaseOrderByNumber(ctx, args.OrderNumber)
	if err != nil {
		return "", err
	}
	if po == nil {
		out, _ := json.Marshal(map[string]any{"found": false, "order_number": args.OrderNumber})
		return string(out), nil
	}
	rows, err := tb.Monitor.GetPurchaseOrderRows(ctx, po.ID)
	if err != nil {
		return "", err
	}
	supplierName := ""
	if sup, _ := tb.Monitor.GetSupplier(ctx, po.BusinessContactId); sup != nil {
		supplierName = sup.Name
	}
	out, _ := json.Marshal(map[string]any{
		"found": true, "order": po, "supplier_name": supplierName, "rows": rows,
	})
	return string(out), nil
}

func (tb *Toolbox) monitorFindSupplier(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	var args struct {
		Term string `json:"term"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if args.Term == "" {
		return "", fmt.Errorf("term krävs")
	}
	ctx, cancel := bg()
	defer cancel()
	sups, err := tb.Monitor.FindSupplier(ctx, args.Term)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"suppliers": sups, "count": len(sups)})
	return string(out), nil
}

// monitorLookupCharge: charge → ProductRecords → kandidatorder/artikel.
// FÖRESLÅR koppling; tillämpas via link_cert efter Robs ja.
func (tb *Toolbox) monitorLookupCharge(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	var args struct {
		CertID int64  `json:"cert_id"`
		Charge string `json:"charge"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	charge := strings.TrimSpace(args.Charge)
	if charge == "" && args.CertID != 0 {
		c, err := tb.App.Repo.GetCert(ctx, args.CertID)
		if err != nil {
			return "", err
		}
		charge = c.EffectiveCharge()
	}
	if charge == "" {
		return "", fmt.Errorf("ingen charge angiven och certet saknar charge")
	}
	recs, err := tb.Monitor.FindProductRecords(ctx, charge)
	if err != nil {
		return "", err
	}
	type match struct {
		OrderNumber  string `json:"order_number"`
		SupplierName string `json:"supplier_name"`
		PartId       int64  `json:"part_id"`
		SerialNumber string `json:"serial_number"`
	}
	matches := make([]match, 0, len(recs))
	for i, r := range recs {
		if i >= 10 {
			break
		}
		m := match{PartId: int64(r.PartId), SerialNumber: r.SerialNumber}
		if r.PurchaseOrderId != 0 {
			if po, _ := tb.Monitor.GetPurchaseOrder(ctx, r.PurchaseOrderId); po != nil {
				m.OrderNumber = po.OrderNumber
				if sup, _ := tb.Monitor.GetSupplier(ctx, po.BusinessContactId); sup != nil {
					m.SupplierName = sup.Name
				}
			}
		}
		matches = append(matches, m)
	}
	out, _ := json.Marshal(map[string]any{
		"charge": charge, "monitor_matches": matches,
		"note": "Förslag — koppla INGET utan Robs ja. Tillämpa via link_cert.",
	})
	return string(out), nil
}

// ---------------------------------------------------------------------------
// Minne: noteringar, regler, tasks + bokföring
// ---------------------------------------------------------------------------

func (tb *Toolbox) addNote(input json.RawMessage) (string, error) {
	var args struct {
		Kind        string `json:"kind"`
		RefID       int64  `json:"ref_id"`
		OrderNumber string `json:"order_number"`
		Text        string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	if _, err := tb.App.AddNote(ctx, args.Kind, args.RefID, args.OrderNumber, "", "sickan", args.Text); err != nil {
		return "", err
	}
	return `{"ok":true}`, nil
}

func (tb *Toolbox) getNotes(input json.RawMessage) (string, error) {
	var args struct {
		Kind  string `json:"kind"`
		RefID int64  `json:"ref_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	notes, err := tb.App.Repo.ListNotes(ctx, args.Kind, args.RefID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"notes": notes, "count": len(notes)})
	return string(out), nil
}

func (tb *Toolbox) markDelivered(input json.RawMessage) (string, error) {
	var args struct {
		DeliveryRowIDs []int64 `json:"delivery_row_ids"`
		Delivered      *bool   `json:"delivered"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if len(args.DeliveryRowIDs) == 0 {
		return "", fmt.Errorf("delivery_row_ids krävs")
	}
	delivered := true
	if args.Delivered != nil {
		delivered = *args.Delivered
	}
	ctx, cancel := bg()
	defer cancel()
	if err := tb.App.MarkDelivered(ctx, args.DeliveryRowIDs, delivered); err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "count": len(args.DeliveryRowIDs), "delivered": delivered})
	return string(out), nil
}

// composeDeviationMail bygger ett mailto-utkast för en order: 'rest' (ej
// levererade positioner) eller 'cert_missing' (cert-krävande rader utan
// bekräftat cert). Skickar inget.
func (tb *Toolbox) composeDeviationMail(input json.RawMessage) (string, error) {
	var args struct {
		OrderNumber string `json:"order_number"`
		Kind        string `json:"kind"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	to := tb.Cfg().ReportEmail
	if to == "" {
		return "", fmt.Errorf("ingen avvikelseadress ifylld — sätt den i ⚙️ Inställningar")
	}
	ctx, cancel := bg()
	defer cancel()
	rows, err := tb.App.Repo.RowsByOrderNumber(ctx, strings.ToUpper(strings.TrimSpace(args.OrderNumber)))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("okänd order %q", args.OrderNumber)
	}
	var picked []*domain.OrderRow
	var suffix, intro string
	switch args.Kind {
	case "rest":
		suffix, intro = "ej inlevererade positioner", "levererades inte in"
		for _, r := range rows {
			if !r.Delivered {
				picked = append(picked, r)
			}
		}
	case "cert_missing":
		suffix, intro = "cert saknas", "saknar materialcert"
		for _, r := range rows {
			if !r.CertRequired {
				continue
			}
			links, _ := tb.App.Repo.ListLinksForRow(ctx, r.DeliveryRowID)
			has := false
			for _, l := range links {
				if l.Status == domain.LinkBekraftad {
					has = true
					break
				}
			}
			if !has {
				picked = append(picked, r)
			}
		}
	default:
		return "", fmt.Errorf("okänd mailtyp %q (rest eller cert_missing)", args.Kind)
	}
	if len(picked) == 0 {
		return "", fmt.Errorf("inga positioner matchar %q på ordern", args.Kind)
	}
	label := picked[0].OrderNumber
	if s := picked[0].SupplierName; s != "" {
		label += " (" + s + ")"
	}
	var lines []string
	for _, r := range picked {
		lines = append(lines, fmt.Sprintf("- %s %s (%g st, leveransdatum %s)",
			r.PartNumber, r.Description, r.PlannedQty, r.DeliveryDate))
	}
	subject := fmt.Sprintf("Avvikelse %s — %s", label, suffix)
	body := fmt.Sprintf("Hej,\n\nFöljande positioner på %s %s:\n\n%s\n\nMvh\nRob",
		label, intro, strings.Join(lines, "\n"))
	mailto := "mailto:" + to + "?subject=" + url.QueryEscape(subject) + "&body=" + url.QueryEscape(body)
	out, _ := json.Marshal(map[string]any{
		"to": to, "subject": subject, "body": body,
		"mailto_url": mailto, "positions": lines,
	})
	return string(out), nil
}

func (tb *Toolbox) rememberRule(input json.RawMessage) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("tom regel")
	}
	ctx, cancel := bg()
	defer cancel()
	if _, err := tb.App.Repo.AddRule(ctx, args.Text, "sickan", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	return `{"ok":true,"note":"regeln är sparad och aktiv från nästa meddelande"}`, nil
}

func (tb *Toolbox) listRules() (string, error) {
	ctx, cancel := bg()
	defer cancel()
	rules, err := tb.App.Repo.ListRules(ctx, false)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"rules": rules})
	return string(out), nil
}

func (tb *Toolbox) addTask(input json.RawMessage) (string, error) {
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
	ctx, cancel := bg()
	defer cancel()
	t := &store.Task{Text: args.Text, DueDate: args.DueDate, OrderNumber: args.OrderNumber,
		Source: "sickan", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if _, err := tb.App.Repo.AddTask(ctx, t); err != nil {
		return "", err
	}
	tb.App.Notify.OverviewChanged()
	out, _ := json.Marshal(map[string]any{"ok": true, "id": t.ID})
	return string(out), nil
}

func (tb *Toolbox) listTasks() (string, error) {
	ctx, cancel := bg()
	defer cancel()
	tasks, err := tb.App.Repo.ListTasks(ctx, "")
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"tasks": tasks})
	return string(out), nil
}

func (tb *Toolbox) completeTask(input json.RawMessage) (string, error) {
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	ctx, cancel := bg()
	defer cancel()
	if err := tb.App.Repo.CompleteTask(ctx, args.ID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	tb.App.Notify.OverviewChanged()
	return `{"ok":true}`, nil
}
