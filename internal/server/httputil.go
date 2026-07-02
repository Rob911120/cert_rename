package server

import (
	"encoding/json"
	"net/http"

	"cert-renamer/internal/store"
)

// requirePOST svarar 405 och returnerar false om metoden inte är POST.
// Används av alla muterande endpoints så inga tillståndsändringar sker på GET.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// decodeJSON läser request-kroppen till v; svarar 400 och returnerar false vid fel.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "ingen inbox vald", http.StatusBadRequest)
		return c, false
	}
	return c, true
}
