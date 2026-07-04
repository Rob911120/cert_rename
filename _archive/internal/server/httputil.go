package server

import (
	"encoding/json"
	"net/http"

	"cert-renamer/internal/store"
)

// httpError svarar med enhetligt JSON-felformat {"error": "..."} och status.
// Alla API-fel går genom den här så UI:t kan läsa fel på ett ställe (apiError).
func httpError(w http.ResponseWriter, msg string, status int) {
	writeJSONStatus(w, status, map[string]string{"error": msg})
}

// writeJSON svarar 200 med JSON-kropp.
func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus svarar med given status och JSON-kropp.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// mergeMap slår ihop två maps (b vinner vid nyckelkrock).
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

// requirePOST svarar 405 och returnerar false om metoden inte är POST.
// Används av alla muterande endpoints så inga tillståndsändringar sker på GET.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// decodeJSON läser request-kroppen till v; svarar 400 och returnerar false vid fel.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// snapshotCfg returnerar en kopia av konfigurationen tagen under lås.
func (s *Server) snapshotCfg() store.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// requireInbox returnerar cfg-snapshot; svarar 400 och returnerar false om
// ingen inbox-mapp är vald.
func (s *Server) requireInbox(w http.ResponseWriter) (store.Config, bool) {
	c := s.snapshotCfg()
	if c.InboxDir == "" {
		httpError(w, "ingen inbox vald", http.StatusBadRequest)
		return c, false
	}
	return c, true
}
