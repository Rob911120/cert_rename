package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/v2/ai"
	"cert-renamer/internal/v2/sickan"
	"cert-renamer/internal/v2/store"
)

// Sessionshantering + stream-protokoll är porterade 1:1 från V1
// (internal/server/sickan.go) så chat-klienten fungerar likadant.

type sickanSession struct {
	History []anthropic.MessageParam
	Model   string
}

type sickanSessions struct {
	mu sync.Mutex
	s  map[string]*sickanSession
}

func (ss *sickanSessions) entry(id string) *sickanSession {
	if ss.s == nil {
		ss.s = map[string]*sickanSession{}
	}
	if ss.s[id] == nil {
		ss.s[id] = &sickanSession{Model: ai.ChatDefault}
	}
	return ss.s[id]
}

func (ss *sickanSessions) get(id string) ([]anthropic.MessageParam, string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	e := ss.entry(id)
	return e.History, e.Model
}

func (ss *sickanSessions) set(id string, h []anthropic.MessageParam) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.entry(id).History = h
}

func (ss *sickanSessions) setModel(id, model string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.entry(id).Model = model
}

func (ss *sickanSessions) clear(id string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if e, ok := ss.s[id]; ok {
		e.History = nil
	}
}

func (s *Server) persistSickanModel(session, model string) {
	s.sickanSess.setModel(session, model)
	s.mu.Lock()
	if s.cfg.SickanModel == model {
		s.mu.Unlock()
		return
	}
	s.cfg.SickanModel = model
	cfg := s.cfg
	s.mu.Unlock()
	if err := store.SaveConfig(cfg); err != nil {
		s.Logf("⚠️  kunde inte spara config (sickan-modell): %v", err)
	}
}

// agentRules hämtar aktiva regler för system-blocket.
func (s *Server) agentRules(r *http.Request) []string {
	rules, err := s.Repo.ListRules(r.Context(), true)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.Text)
	}
	return out
}

func (s *Server) handleSickanModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Model   string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Session == "" {
		body.Session = "default"
	}
	if ai.ChatCostKey(body.Model) == "" {
		http.Error(w, "okänd modell", http.StatusBadRequest)
		return
	}
	s.persistSickanModel(body.Session, body.Model)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSickanReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Session == "" {
		body.Session = "default"
	}
	s.sickanSess.clear(body.Session)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSickanStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Text    string `json:"text"`
		Model   string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Session == "" {
		body.Session = "default"
	}
	if body.Text == "" {
		http.Error(w, "tom text", http.StatusBadRequest)
		return
	}
	cfg := s.Config()
	if cfg.ApiKey == "" {
		http.Error(w, "ingen API-nyckel — öppna ⚙️ Inställningar", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming stöds inte", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	emit := func(ev sickan.Event) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, jsonEscape(ev.Data))
		flusher.Flush()
	}

	if body.Model != "" && ai.ChatCostKey(body.Model) != "" {
		s.persistSickanModel(body.Session, body.Model)
	} else if cfg.SickanModel != "" {
		s.sickanSess.setModel(body.Session, cfg.SickanModel)
	}
	history, model := s.sickanSess.get(body.Session)
	history = append(history, anthropic.MessageParam{
		Role: anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{
			{OfText: &anthropic.TextBlockParam{Text: body.Text}},
		},
	})

	client := anthropic.NewClient(option.WithAPIKey(cfg.ApiKey))
	s.mu.Lock()
	mon := s.mon
	s.mu.Unlock()
	tb := &sickan.Toolbox{
		App: s.App, Cfg: s.Config,
		Monitor: mon, MonitorConnect: s.ensureMonitor,
		Rules: s.agentRules(r),
	}

	updated, err := sickan.Run(r.Context(), &client, tb, s, model, history, emit)
	if err != nil {
		emit(sickan.Event{Kind: "error", Data: err.Error()})
	}
	updated = sickan.CompactHistory(updated, 1)
	s.sickanSess.set(body.Session, updated)
}

// jsonEscape: SSE skiljer händelser med "\n\n" — datat skickas därför som
// JSON-sträng (klienten JSON.parse:ar data-fältet).
func jsonEscape(raw string) string {
	b, _ := json.Marshal(raw)
	return string(b)
}
