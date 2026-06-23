package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/sickan"
	"cert-renamer/internal/store"
)

type sickanSession struct {
	History []anthropic.MessageParam
	Model   string
}

// sickanSessions håller konversationshistorik + valt modell-ID per session.
// Klienten skickar med samma session-ID i varje request. Reset rensar history
// men behåller modellvalet (via setModel).
type sickanSessions struct {
	mu sync.Mutex
	s  map[string]*sickanSession
}

func newSickanSessions() *sickanSessions {
	return &sickanSessions{s: map[string]*sickanSession{}}
}

func (ss *sickanSessions) entry(id string) *sickanSession {
	if ss.s[id] == nil {
		ss.s[id] = &sickanSession{Model: ai.ChatDefault}
	}
	return ss.s[id]
}

func (ss *sickanSessions) get(id string) (history []anthropic.MessageParam, model string) {
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

func (s *Server) handleSickanModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Session string `json:"session"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.Session == "" {
		body.Session = "default"
	}
	if ai.ChatCostKey(body.Model) == "" {
		http.Error(w, "okänd modell", 400)
		return
	}
	s.sickanSess.setModel(body.Session, body.Model)
	s.mu.Lock()
	if s.cfg.SickanModel != body.Model {
		s.cfg.SickanModel = body.Model
		cfg := s.cfg
		s.mu.Unlock()
		_ = store.SaveConfig(cfg)
	} else {
		s.mu.Unlock()
	}
	w.WriteHeader(204)
}

func (s *Server) handleSickanReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Session string `json:"session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Session == "" {
		body.Session = "default"
	}
	s.sickanSess.clear(body.Session)
	w.WriteHeader(204)
}

func (s *Server) handleSickanStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Session string `json:"session"`
		Text    string `json:"text"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.Session == "" {
		body.Session = "default"
	}
	if body.Text == "" {
		http.Error(w, "tom text", 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.ApiKey == "" {
		http.Error(w, "ingen API-nyckel", 400)
		return
	}
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
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
		s.sickanSess.setModel(body.Session, body.Model)
		s.mu.Lock()
		if s.cfg.SickanModel != body.Model {
			s.cfg.SickanModel = body.Model
			cfg := s.cfg
			s.mu.Unlock()
			_ = store.SaveConfig(cfg)
		} else {
			s.mu.Unlock()
		}
	} else if c.SickanModel != "" {
		// Första request i sessionen utan explicit modell — använd
		// senast sparade från config.
		s.sickanSess.setModel(body.Session, c.SickanModel)
	}
	history, model := s.sickanSess.get(body.Session)
	history = append(history, anthropic.MessageParam{
		Role: anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{
			{OfText: &anthropic.TextBlockParam{Text: body.Text}},
		},
	})

	client := anthropic.NewClient(option.WithAPIKey(c.ApiKey))
	tb := &sickan.Toolbox{Cfg: c, N: s, Repo: s.repo}

	updated, err := sickan.Run(r.Context(), &client, tb, s, model, history, emit)
	if err != nil {
		emit(sickan.Event{Kind: "error", Data: err.Error()})
	}
	updated = sickan.CompactHistory(updated, 1)
	s.sickanSess.set(body.Session, updated)
}

// jsonEscape returnerar en SSE-säker version av en datasträng. SSE skiljer
// händelser med "\n\n", så råa newlines i datat måste eskaperas. Vi skickar
// alla events som JSON-strängar (klienten kör JSON.parse på data:-fältet).
func jsonEscape(raw string) string {
	b, _ := json.Marshal(raw)
	return string(b)
}
