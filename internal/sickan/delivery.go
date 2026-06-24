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
// Inleverans (följesedlar): följesedel-bild → matcha PO. Själva inleverans-
// registreringen sker via monitor_ui_report_arrival (styr Monitor-klienten),
// eftersom Monitors skriv-API inte är licensierat. Flödet här:
// read_delivery_note_image (visuell koll) · list_delivery_notes ·
// match_delivery_note_to_po (status matched_po).
// ---------------------------------------------------------------------------

var listDeliveryNotesTool = anthropic.ToolParam{
	Name:        "list_delivery_notes",
	Description: anthropic.String("Listar uppladdade följesedlar med vision-avlästa fält + status. Default: bara 'unmatched'. Ange status (unmatched/matched_po) eller \"all\" för alla."),
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
	var partIDs map[monitor.ID]bool
	if dn.Charge != "" {
		if recs, rerr := tb.Monitor.FindProductRecords(ctx, dn.Charge); rerr == nil {
			partIDs = map[monitor.ID]bool{}
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
		if err := tb.Repo.UpdateDeliveryNoteMatch(dn.ID, int64(po.ID), int64(row.ID), store.DNMatchedPO); err != nil {
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
