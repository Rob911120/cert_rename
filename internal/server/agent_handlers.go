package server

// HTTP-endpoints för Sickans minne: noter på inleveranser, inlärda arbetsregler
// och att-göra-listan. Muterande anrop broadcastar så alla klienter uppdateras.

import (
	"net/http"
	"strings"

	"cert-renamer/internal/store"
)

// handleUpcomingNote lägger till en not på en inleveransrad (author=rob).
func (s *Server) handleUpcomingNote(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		DeliveryRowID int64  `json:"delivery_row_id,string"`
		Text          string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.DeliveryRowID == 0 {
		httpError(w, "delivery_row_id krävs", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		httpError(w, "tom not", http.StatusBadRequest)
		return
	}
	if _, err := s.repo.AddUpcomingNote(body.DeliveryRowID, "rob", text); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.BroadcastUpcoming()
	w.WriteHeader(http.StatusNoContent)
}

// handleUpcomingNoteDelete tar bort en not.
func (s *Server) handleUpcomingNoteDelete(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.repo.DeleteUpcomingNote(body.ID); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.BroadcastUpcoming()
	w.WriteHeader(http.StatusNoContent)
}

// handleSickanRules listar aktiva arbetsregler (för ⚙️-panelen).
func (s *Server) handleSickanRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.repo.ListAgentRules()
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []store.AgentRule{}
	}
	writeJSON(w, map[string]any{"rules": rules})
}

// handleSickanRuleRemove inaktiverar en regel.
func (s *Server) handleSickanRuleRemove(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.repo.DisableAgentRule(body.ID); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSickanRuleAdd lägger till en regel manuellt från ⚙️-panelen.
func (s *Server) handleSickanRuleAdd(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		httpError(w, "tom regel", http.StatusBadRequest)
		return
	}
	if _, err := s.repo.AddAgentRule(text, "rob"); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTasks: GET = lista öppna tasks; POST = lägg till.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tasks, err := s.repo.ListTasks(true)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tasks == nil {
			tasks = []store.Task{}
		}
		writeJSON(w, map[string]any{"tasks": tasks})
		return
	}
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Text        string `json:"text"`
		DueDate     string `json:"due_date"`
		OrderNumber string `json:"order_number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		httpError(w, "tom task", http.StatusBadRequest)
		return
	}
	if _, err := s.repo.AddTask(store.Task{Text: text, DueDate: body.DueDate, OrderNumber: body.OrderNumber, Source: "rob"}); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.BroadcastUpcoming()
	w.WriteHeader(http.StatusNoContent)
}

// handleTaskDone bockar av en task; handleTaskDelete tar bort den.
func (s *Server) handleTaskDone(w http.ResponseWriter, r *http.Request) {
	s.taskMutation(w, r, s.repo.CompleteTask)
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	s.taskMutation(w, r, s.repo.DeleteTask)
}

func (s *Server) taskMutation(w http.ResponseWriter, r *http.Request, fn func(int64) error) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := fn(body.ID); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.BroadcastUpcoming()
	w.WriteHeader(http.StatusNoContent)
}

// agentRules läser aktiva regler som strängar (för Toolbox.Rules); fel loggas
// och ger tom lista — chatten ska inte stoppas av en regelläsning.
func (s *Server) agentRules() []string {
	rules, err := s.repo.ListAgentRules()
	if err != nil {
		s.Logf("⚠️  kunde inte läsa arbetsregler: %v", err)
		return nil
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Text)
	}
	return out
}
