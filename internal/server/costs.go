package server

import (
	"encoding/json"
	"net/http"

	"cert-renamer/internal/store"
)

// costsResponse bygger UI-vänlig payload med token-räknare + USD per modell.
func (s *Server) costsResponse() map[string]any {
	s.costsMu.Lock()
	c := s.costs
	s.costsMu.Unlock()
	sonnetUsd := store.SonnetPricing.Usd(c.Sonnet)
	haikuUsd := store.HaikuPricing.Usd(c.Haiku)
	opusUsd := store.OpusPricing.Usd(c.Opus)
	return map[string]any{
		"sonnet": map[string]any{
			"tokens": c.Sonnet,
			"usd":    sonnetUsd,
		},
		"haiku": map[string]any{
			"tokens": c.Haiku,
			"usd":    haikuUsd,
		},
		"opus": map[string]any{
			"tokens": c.Opus,
			"usd":    opusUsd,
		},
		"total_usd":  sonnetUsd + haikuUsd + opusUsd,
		"updated_at": c.UpdatedAt,
	}
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.costsResponse())
}

// BroadcastCosts pushar uppdaterad cost-summa till alla SSE-prenumeranter.
func (s *Server) BroadcastCosts() {
	payload, _ := json.Marshal(s.costsResponse())
	s.broadcast(ssEvent{Event: "costs", Data: string(payload)})
}
