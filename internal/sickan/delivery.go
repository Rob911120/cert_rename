package sickan

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// ---------------------------------------------------------------------------
// Inleverans-trial (Fas 4): följesedel-bild → matcha PO → föreslå → registrera.
// Flödet: read_delivery_note_image (visuell koll) · list_delivery_notes ·
// match_delivery_note_to_po · propose_receiving (förhandsvisning, skriver inget) ·
// monitor_register_arrival (WRITE — bara efter status=receiving_proposed OCH
// confirm=true, en orderrad i taget).
// ---------------------------------------------------------------------------

var listDeliveryNotesTool = anthropic.ToolParam{
	Name:        "list_delivery_notes",
	Description: anthropic.String("Listar uppladdade följesedlar med vision-avlästa fält + status. Default: bara 'unmatched'. Ange status (unmatched/matched_po/receiving_proposed/receiving_confirmed) eller \"all\" för alla."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"status": map[string]any{"type": "string", "description": "Statusfilter; tomt = unmatched, \"all\" = alla."},
		},
	},
}

var readDeliveryNoteImageTool = anthropic.ToolParam{
	Name:        "read_delivery_note_image",
	Description: anthropic.String("Bifogar en uppladdad följesedel-BILD som bild-block så du kan läsa den visuellt (dubbelkolla ordernummer, charge, antal innan matchning/registrering)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"id": map[string]any{"type": "integer", "description": "Följesedelns id."},
		},
		Required: []string{"id"},
	},
}

var matchDeliveryNoteToPOTool = anthropic.ToolParam{
	Name:        "match_delivery_note_to_po",
	Description: anthropic.String("Matchar en följesedel mot en Monitor-inköpsorder och orderrad via ordernummer + charge (charge→ProductRecord→PartId→orderrad). Vid exakt en träff sätts matchningen (status matched_po). Vid flera/ingen träff registreras INGET — du får kandidater att välja bland manuellt."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"id": map[string]any{"type": "integer", "description": "Följesedelns id."},
		},
		Required: []string{"id"},
	},
}

var proposeReceivingTool = anthropic.ToolParam{
	Name:        "propose_receiving",
	Description: anthropic.String("Bygger en FÖRHANDSVISNING av inleverans-payloaden (ReportArrivals) för en matchad följesedel och sätter status receiving_proposed. KÖR INGET mot Monitor. Visa förhandsvisningen för användaren och vänta på ja."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"id":       map[string]any{"type": "integer", "description": "Följesedelns id (måste vara matchad)."},
			"quantity": map[string]any{"type": "number", "description": "Valfri override av kvantitet; annars används den avlästa."},
		},
		Required: []string{"id"},
	},
}

var monitorRegisterArrivalTool = anthropic.ToolParam{
	Name:        "monitor_register_arrival",
	Description: anthropic.String("SKRIVER en inleverans till Monitor (ReportArrivals), en orderrad i taget. Körs BARA när följesedeln har status receiving_proposed OCH confirm=true. Anropa ENDAST efter att användaren uttryckligen sagt ja i förra meddelandet."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"id":       map[string]any{"type": "integer", "description": "Följesedelns id (måste vara receiving_proposed)."},
			"confirm":  map[string]any{"type": "boolean", "description": "Måste vara true — sätts bara efter användarens uttryckliga ja."},
			"quantity": map[string]any{"type": "number", "description": "Valfri override; annars den föreslagna kvantiteten."},
		},
		Required: []string{"id", "confirm"},
	},
}

func (tb *Toolbox) listDeliveryNotes(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return `{"notes":[],"count":0}`, nil
	}
	var args struct {
		Status string `json:"status"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", err
		}
	}
	filter := args.Status
	switch filter {
	case "":
		filter = store.DNUnmatched
	case "all":
		filter = ""
	}
	notes, err := tb.Repo.ListDeliveryNotes(filter)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"notes": notes, "count": len(notes)})
	return string(out), nil
}

func (tb *Toolbox) readDeliveryNoteImage(input json.RawMessage) (DispatchResult, error) {
	if tb.Repo == nil {
		return DispatchResult{}, fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return DispatchResult{}, err
	}
	dn, err := tb.Repo.GetDeliveryNote(args.ID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("följesedel %d finns inte: %w", args.ID, err)
	}
	if !safeName(dn.ImageFilename) {
		return DispatchResult{}, fmt.Errorf("ogiltigt bildfilnamn")
	}
	full := filepath.Join(store.DeliveryNotesDir(tb.Cfg), dn.ImageFilename)
	data, err := os.ReadFile(full)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("kunde inte läsa bild: %w", err)
	}
	if len(data) > 32*1024*1024 {
		return DispatchResult{}, fmt.Errorf("bild för stor (%d MB)", len(data)/(1024*1024))
	}
	mediaType := imageMediaType(dn.ImageFilename)
	b64 := base64.StdEncoding.EncodeToString(data)
	intro := fmt.Sprintf("Följesedel #%d (%s, %d KB) — bild i nästa block.", dn.ID, dn.ImageFilename, len(data)/1024)
	tb.N.Logf("🤖 Sickan läser följesedel-bild #%d (%d KB)", dn.ID, len(data)/1024)
	return DispatchResult{
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfText: &anthropic.TextBlockParam{Text: intro}},
			{OfImage: &anthropic.ImageBlockParam{
				Source: anthropic.ImageBlockParamSourceUnion{
					OfBase64: &anthropic.Base64ImageSourceParam{
						Data:      b64,
						MediaType: anthropic.Base64ImageSourceMediaType(mediaType),
					},
				},
			}},
		},
		Summary: intro,
	}, nil
}

func (tb *Toolbox) matchDeliveryNoteToPO(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	dn, err := tb.Repo.GetDeliveryNote(args.ID)
	if err != nil {
		return "", fmt.Errorf("följesedel %d finns inte: %w", args.ID, err)
	}
	ctx, cancel := monitorCtx()
	defer cancel()

	// 1. Hitta inköpsordern (via ordernummer, annars via charge→ProductRecord).
	var po *monitor.PurchaseOrder
	if dn.OrderNumber != "" {
		if po, err = tb.Monitor.FindPurchaseOrderByNumber(ctx, dn.OrderNumber); err != nil {
			return "", err
		}
	}
	if po == nil && dn.Charge != "" {
		recs, rerr := tb.Monitor.FindProductRecords(ctx, dn.Charge)
		if rerr != nil {
			return "", rerr
		}
		for _, r := range recs {
			if r.PurchaseOrderId != 0 {
				if po, _ = tb.Monitor.GetPurchaseOrder(ctx, r.PurchaseOrderId); po != nil {
					break
				}
			}
		}
	}
	if po == nil {
		out, _ := json.Marshal(map[string]any{"matched": false, "reason": "hittade ingen inköpsorder för ordernummer/charge"})
		return string(out), nil
	}

	// 2. Hämta orderrader och bestäm vilken rad som matchar.
	rows, err := tb.Monitor.GetPurchaseOrderRows(ctx, po.ID)
	if err != nil {
		return "", err
	}
	var partIDs map[int64]bool
	if dn.Charge != "" {
		if recs, rerr := tb.Monitor.FindProductRecords(ctx, dn.Charge); rerr == nil {
			partIDs = map[int64]bool{}
			for _, r := range recs {
				partIDs[r.PartId] = true
			}
		}
	}
	var candidates []monitor.PurchaseOrderRow
	if len(partIDs) > 0 {
		for _, row := range rows {
			if partIDs[row.PartId] {
				candidates = append(candidates, row)
			}
		}
	} else {
		candidates = rows
	}

	if len(candidates) == 1 {
		row := candidates[0]
		if err := tb.Repo.UpdateDeliveryNoteMatch(dn.ID, po.ID, row.ID, store.DNMatchedPO); err != nil {
			return "", err
		}
		tb.N.BroadcastStats()
		tb.N.Logf("🤖 Sickan: följesedel #%d matchad mot order %s rad %d", dn.ID, po.OrderNumber, row.ID)
		out, _ := json.Marshal(map[string]any{
			"matched":               true,
			"order_number":          po.OrderNumber,
			"purchase_order_id":     po.ID,
			"purchase_order_row_id": row.ID,
			"part_id":               row.PartId,
			"rest_quantity":         row.RestQuantity,
		})
		return string(out), nil
	}

	// Tvetydigt eller inga rader → registrera inget, returnera kandidater.
	out, _ := json.Marshal(map[string]any{
		"matched":           false,
		"order_number":      po.OrderNumber,
		"purchase_order_id": po.ID,
		"candidate_rows":    candidates,
		"reason":            fmt.Sprintf("%d möjliga rader — välj manuellt, inget registrerat", len(candidates)),
	})
	return string(out), nil
}

func (tb *Toolbox) proposeReceiving(input json.RawMessage) (string, error) {
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		ID       int64    `json:"id"`
		Quantity *float64 `json:"quantity"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	dn, err := tb.Repo.GetDeliveryNote(args.ID)
	if err != nil {
		return "", fmt.Errorf("följesedel %d finns inte: %w", args.ID, err)
	}
	if dn.MatchedRowID == 0 {
		return "", fmt.Errorf("följesedel %d är inte matchad mot en orderrad — kör match_delivery_note_to_po först", args.ID)
	}
	qty := dn.Quantity
	if args.Quantity != nil {
		qty = *args.Quantity
	}
	if qty <= 0 {
		return "", fmt.Errorf("kvantitet saknas/ogiltig (%g) — ange quantity explicit", qty)
	}
	payload := monitor.ReportArrivalsRequest{
		DeliveryNoteNumber: dn.DeliveryNoteNumber,
		WaybillNumber:      dn.WaybillNumber,
		Rows:               []monitor.ArrivalRow{{PurchaseOrderRowId: dn.MatchedRowID, Quantity: qty}},
	}
	if err := tb.Repo.UpdateDeliveryNoteProposal(dn.ID, qty, store.DNReceivingProposed); err != nil {
		return "", err
	}
	tb.N.BroadcastStats()
	out, _ := json.Marshal(map[string]any{
		"preview": payload,
		"note":    "FÖRSLAG — inget registrerat. Vid ja: anropa monitor_register_arrival med confirm=true.",
	})
	return string(out), nil
}

func (tb *Toolbox) monitorRegisterArrival(input json.RawMessage) (string, error) {
	if err := tb.monitorReady(); err != nil {
		return "", err
	}
	if tb.Repo == nil {
		return "", fmt.Errorf("DB inte tillgänglig")
	}
	var args struct {
		ID       int64    `json:"id"`
		Confirm  bool     `json:"confirm"`
		Quantity *float64 `json:"quantity"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	dn, err := tb.Repo.GetDeliveryNote(args.ID)
	if err != nil {
		return "", fmt.Errorf("följesedel %d finns inte: %w", args.ID, err)
	}
	// GATE 1: måste vara föreslaget (propose_receiving har körts).
	if dn.Status != store.DNReceivingProposed {
		return "", fmt.Errorf("registrerar inte: följesedel %d har status %q — kör propose_receiving först", args.ID, dn.Status)
	}
	// GATE 2: explicit bekräftelse (sätts bara efter användarens ja).
	if !args.Confirm {
		return "", fmt.Errorf("registrerar inte utan uttrycklig bekräftelse (confirm=true) — be användaren bekräfta först")
	}
	if dn.MatchedRowID == 0 {
		return "", fmt.Errorf("ingen matchad orderrad att registrera mot")
	}
	qty := dn.ProposedQuantity
	if args.Quantity != nil {
		qty = *args.Quantity
	}
	if qty <= 0 {
		return "", fmt.Errorf("ogiltig kvantitet (%g)", qty)
	}
	ctx, cancel := monitorCtx()
	defer cancel()
	res, err := tb.Monitor.ReportArrivals(ctx, monitor.ReportArrivalsRequest{
		DeliveryNoteNumber: dn.DeliveryNoteNumber,
		WaybillNumber:      dn.WaybillNumber,
		Rows:               []monitor.ArrivalRow{{PurchaseOrderRowId: dn.MatchedRowID, Quantity: qty}},
	})
	if err != nil {
		return "", fmt.Errorf("ReportArrivals misslyckades: %w", err)
	}
	if err := tb.Repo.UpdateDeliveryNoteStatus(dn.ID, store.DNReceivingConfirmed); err != nil {
		return "", err
	}
	tb.N.Logf("🤖 Sickan: inleverans REGISTRERAD — följesedel #%d, orderrad %d, antal %g", dn.ID, dn.MatchedRowID, qty)
	tb.N.BroadcastStats()
	out, _ := json.Marshal(map[string]any{
		"ok":               true,
		"delivery_note_id": dn.ID,
		"monitor_response": json.RawMessage(res),
	})
	return string(out), nil
}

func imageMediaType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
