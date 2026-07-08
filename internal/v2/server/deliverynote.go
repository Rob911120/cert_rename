package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/monitorui"
	"cert-renamer/internal/v2/store"
)

// ---------------------------------------------------------------------------
// Följesedlar (delivery notes): surfas i "Att göra", matchas lokalt mot
// order_rows och (Fas 2) registreras via Monitor-klienten. All mutation går
// genom App (enda skrivvägen); overviewn läser read-only och beräknar kandidater
// färskt (inget live-Monitor-anrop).
// ---------------------------------------------------------------------------

type dnCandidateJSON struct {
	DeliveryRowID string `json:"delivery_row_id"`
	OrderNumber   string `json:"order_number"`
	PartNumber    string `json:"part_number"`
	Description   string `json:"description"`
	DeliveryDate  string `json:"delivery_date"`
}

type deliveryNoteJSON struct {
	ID                 string   `json:"id"`
	ImageFilename      string   `json:"image_filename"`
	Supplier           string   `json:"supplier"`
	DeliveryDate       string   `json:"delivery_date"`
	OrderNumber        string   `json:"order_number"`
	Charge             string   `json:"charge"`
	Material           string   `json:"material"`
	Quantity           float64  `json:"quantity"`
	Unit               string   `json:"unit"`
	DeliveryNoteNumber string   `json:"delivery_note_number"`
	BNumbers           []string `json:"b_numbers"`
	Confidence         string   `json:"confidence"`
	Status             string   `json:"status"`

	// Matchning (nullbart datafält). MatchedRowID "0" = ej matchad → Candidates
	// bär kandidatrader för manuellt val.
	MatchedRowID       string            `json:"matched_row_id"`
	MatchedOrderNumber string            `json:"matched_order_number"`
	MatchedPartNumber  string            `json:"matched_part_number"`
	MatchedDescription string            `json:"matched_description"`
	Candidates         []dnCandidateJSON `json:"candidates"`

	CreatedAt string `json:"created_at"`
}

// deliveryNotesJSON bygger UI-vyn av väntande följesedlar. Read-only: matchade
// visar sin orderrad, omatchade får kandidatrader (lokalt uppslag, inget
// live-Monitor-anrop).
func (s *Server) deliveryNotesJSON(ctx context.Context) []deliveryNoteJSON {
	notes, err := s.App.ListDeliveryNotes(ctx, domain.DNMottagen)
	if err != nil {
		return []deliveryNoteJSON{}
	}
	out := make([]deliveryNoteJSON, 0, len(notes))
	for _, d := range notes {
		dj := deliveryNoteJSON{
			ID: idStr(d.ID), ImageFilename: d.ImageFilename, Supplier: d.Supplier,
			DeliveryDate: d.DeliveryDate, OrderNumber: d.OrderNumber, Charge: d.Charge,
			Material: d.Material, Quantity: d.Quantity, Unit: d.Unit,
			DeliveryNoteNumber: d.DeliveryNoteNumber, BNumbers: orEmpty(d.BNumbers),
			Confidence: d.Confidence, Status: string(d.Status),
			MatchedRowID: idStr(d.MatchedRowID), CreatedAt: d.CreatedAt,
			Candidates: []dnCandidateJSON{},
		}
		if d.Matched() {
			if row, err := s.Repo.GetOrderRow(ctx, d.MatchedRowID); err == nil {
				dj.MatchedOrderNumber = row.OrderNumber
				dj.MatchedPartNumber = row.PartNumber
				dj.MatchedDescription = row.Description
			}
		} else {
			seen := map[int64]bool{}
			for _, num := range dnOrderCandidateNumbers(d) {
				rows, _ := s.Repo.RowsByOrderNumber(ctx, num)
				for _, r := range rows {
					if seen[r.DeliveryRowID] {
						continue
					}
					seen[r.DeliveryRowID] = true
					dj.Candidates = append(dj.Candidates, dnCandidateJSON{
						DeliveryRowID: idStr(r.DeliveryRowID), OrderNumber: r.OrderNumber,
						PartNumber: r.PartNumber, Description: r.Description, DeliveryDate: r.DeliveryDate,
					})
				}
			}
		}
		out = append(out, dj)
	}
	return out
}

// dnOrderCandidateNumbers är de normaliserade ordernummer en följesedel kan
// matchas på (ordernummer + B-nummer), för read-only kandidatuppslag i overviewn.
func dnOrderCandidateNumbers(d *domain.DeliveryNote) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(d.OrderNumber)
	for _, bn := range d.BNumbers {
		add(bn)
	}
	return out
}

// handleDeliveryNoteMatch: med delivery_row_id → manuellt val; utan → kör om den
// lokala auto-matchningen.
func (s *Server) handleDeliveryNoteMatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID            id64 `json:"id"`
		DeliveryRowID id64 `json:"delivery_row_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	var err error
	if req.DeliveryRowID != 0 {
		err = s.App.SetDeliveryNoteMatch(r.Context(), int64(req.ID), int64(req.DeliveryRowID))
	} else {
		_, err = s.App.MatchDeliveryNoteLocal(r.Context(), int64(req.ID))
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleDeliveryNoteReject avfärdar en följesedel (irrelevant).
func (s *Server) handleDeliveryNoteReject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID id64 `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.App.RejectDeliveryNote(r.Context(), int64(req.ID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleDeliveryNoteRegister driver Monitor-klienten för att registrera
// inleveransen. confirm=false → preview (driver inget). confirm=true → öppnar
// Rapportera inleverans, fyller i ordernumret, Ctrl+L, och Ctrl+S BARA om
// save=true OCH MonitorUIAutoSave är på. Följesedeln flyttas till 'inlevererad'
// enbart när Ctrl+S faktiskt skickades (annars sparar Rob själv och bekräftar).
// Windows-only — på andra OS returnerar monitorui ett tydligt fel.
func (s *Server) handleDeliveryNoteRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      id64 `json:"id"`
		Confirm bool `json:"confirm"`
		Save    bool `json:"save"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	d, err := s.App.GetDeliveryNote(ctx, int64(req.ID))
	if err != nil {
		writeError(w, err)
		return
	}
	order := d.OrderNumber
	if d.Matched() {
		if row, err := s.Repo.GetOrderRow(ctx, d.MatchedRowID); err == nil && row.OrderNumber != "" {
			order = row.OrderNumber
		}
	}
	if order == "" {
		http.Error(w, "ingen order att leverera in på — matcha följesedeln först", http.StatusBadRequest)
		return
	}
	autoSave := s.Config().MonitorUIAutoSave
	willSave := req.Save && autoSave
	if !req.Confirm {
		writeJSON(w, map[string]any{
			"preview":      true,
			"order_number": order,
			"routine":      "report_arrival",
			"will_save":    willSave,
			"note":         "FÖRSLAG — öppnar Rapportera inleverans och fyller i ordernumret. Ctrl+S skickas bara om auto-save är på och du bekräftar med save.",
		})
		return
	}
	if err := monitorui.Drive("report_arrival", order, willSave); err != nil {
		writeError(w, err)
		return
	}
	// Bara faktiskt sparad inleverans (Ctrl+S skickat) flyttar följesedeln till
	// terminal 'inlevererad'. Utan auto-save granskar/sparar Rob själv i Monitor.
	if willSave {
		if err := s.App.MarkDeliveryNoteRegistered(ctx, int64(req.ID)); err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "saved": willSave, "order_number": order})
}

// handleDeliveryNoteImage serverar det lagrade följesedel-fotot.
func (s *Server) handleDeliveryNoteImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "id saknas", http.StatusBadRequest)
		return
	}
	d, err := s.App.GetDeliveryNote(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if mt := store.ImageMediaType(d.ImageFilename); mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	http.ServeFile(w, r, store.DeliveryNoteImagePath(s.Config(), d.ImageFilename))
}
