package server

// HTTP-endpoints för "Kommande inleveranser". GET listar, POST /run kickar en
// asynkron refresh (202 + SSE), /mark-delivered markerar en rad levererad, och
// /deliver-in kör Monitor-inleveransrutinen bakom en two-gate (som Sickan):
// kräver confirm, blockerar helt vid materialavvikelse.

import (
	"encoding/json"
	"net/http"

	"cert-renamer/internal/store"
)

// handleUpcoming returnerar listan + schema-status som JSON.
func (s *Server) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.upcomingResponse())
}

// handleUpcomingRun startar en refresh asynkront: 202 + koalescerande kick.
// Resultat/progress pushas via SSE ("upcoming"/"log"). Den faktiska körningen
// hanterar saknad Monitor-konfiguration själv (loggar fel), så endpointen är
// säker att anropa även innan Steg 0 är grön.
func (s *Server) handleUpcomingRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.KickUpcoming()
	w.WriteHeader(http.StatusAccepted) // 202
}

// handleUpcomingMarkDelivered markerar en rad levererad (operatörens markering,
// överlever refresh).
func (s *Server) handleUpcomingMarkDelivered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		DeliveryRowID int64 `json:"delivery_row_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.DeliveryRowID == 0 {
		http.Error(w, "delivery_row_id krävs", http.StatusBadRequest)
		return
	}
	if err := s.repo.MarkUpcomingDelivered(body.DeliveryRowID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.BroadcastUpcoming()
	w.WriteHeader(http.StatusNoContent)
}

// handleUpcomingDeliverIn kör Monitor-inleveransrutinen för en rad. Two-gate som
// Sickan: kräver confirm:true (annars förhandsvisning), och blockerar HELT vid
// materialavvikelse (mismatch). Ctrl+S (spara) styrs av MonitorUIAutoSave inne i
// DriveMonitorRoutine — default av, så operatören bekräftar raden i klienten.
// Svaret bär radens purchase_order_row_id/part_id/planned_qty eftersom
// UI-automationen per ordernr inte själv kan peka ut raden vid fleradersorder.
func (s *Server) handleUpcomingDeliverIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		DeliveryRowID int64 `json:"delivery_row_id"`
		Confirm       bool  `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, err := s.repo.GetUpcomingByRowID(body.DeliveryRowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil {
		http.Error(w, "raden finns inte", http.StatusNotFound)
		return
	}
	rowInfo := map[string]any{
		"delivery_row_id":       row.DeliveryRowID,
		"order_number":          row.OrderNumber,
		"purchase_order_row_id": row.PurchaseOrderRowID,
		"part_id":               row.PartID,
		"part_number":           row.PartNumber,
		"planned_qty":           row.PlannedQty,
	}

	if row.OrderNumber == "" {
		writeJSONStatus(w, http.StatusBadRequest, mergeMap(rowInfo, map[string]any{
			"blocked": true,
			"reason":  "raden saknar ordernummer — kan inte styra Monitor-rutinen",
		}))
		return
	}

	// Hård gate: blockera helt vid materialavvikelse.
	if blocked, reason := deliverInBlocked(row); blocked {
		writeJSONStatus(w, http.StatusConflict, mergeMap(rowInfo, map[string]any{
			"blocked": true,
			"reason":  reason,
		}))
		return
	}

	// Kräver confirm — utan den: förhandsvisning, ingen körning.
	if !body.Confirm {
		writeJSONStatus(w, http.StatusOK, mergeMap(rowInfo, map[string]any{
			"preview":       true,
			"needs_confirm": true,
		}))
		return
	}

	// save skickas bara om auto-spara är på — annars öppnas rutinen med listan
	// hämtad och operatören bekräftar raden (Ctrl+S) själv i klienten.
	// DriveMonitorRoutine dubbelgrindar dessutom mot MonitorUIAutoSave.
	s.mu.Lock()
	autoSave := s.cfg.MonitorUIAutoSave
	s.mu.Unlock()
	if err := s.driveRoutine("report_arrival", row.OrderNumber, autoSave); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Logf("📦 Leverera in: öppnade Monitor-rutin för order %s (rad %d)", row.OrderNumber, row.PurchaseOrderRowID)
	writeJSONStatus(w, http.StatusOK, mergeMap(rowInfo, map[string]any{"started": true}))
}

// deliverInBlocked avgör om raden ska blockeras helt från inleverans.
// Materialavvikelse (mismatch) är en hård spärr. Saknat cert är en MJUK varning
// (cert kommer ofta dagen efter godset) och blockerar inte.
func deliverInBlocked(row *store.UpcomingDelivery) (bool, string) {
	if row.MaterialOK == store.MaterialMismatch {
		return true, "materialavvikelse mot certet (mismatch) — red ut innan inleverans"
	}
	return false, ""
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func mergeMap(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
