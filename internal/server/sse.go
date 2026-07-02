package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"cert-renamer/internal/store"
)

type ssEvent struct {
	Event string
	Data  string
}

func (s *Server) broadcast(ev ssEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// statsPayload bygger stats-eventets JSON. "approved" räknas från disk så
// "klara"-räknaren i UI:t överlever sidladdningar.
func (s *Server) statsPayload() string {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	var rev, arc, app int64
	if c.InboxDir != "" {
		rev = store.CountSubdirs(store.ReviewDir(c))
		arc = store.CountSubdirs(store.ArkiveratDir(c))
		app = store.CountFiles(store.ApprovedDir(c))
	}
	payload, _ := json.Marshal(map[string]int64{
		"ok":       s.stats.OK.Load(),
		"review":   rev,
		"archived": arc,
		"approved": app,
	})
	return string(payload)
}

func (s *Server) BroadcastStats() {
	s.broadcast(ssEvent{Event: "stats", Data: s.statsPayload()})
}

func (s *Server) BroadcastQueue()  { s.broadcast(ssEvent{Event: "queue", Data: "{}"}) }
func (s *Server) BroadcastReview() { s.broadcast(ssEvent{Event: "review", Data: "{}"}) }

func (s *Server) broadcastStateInternal() {
	s.mu.Lock()
	r := s.running
	s.mu.Unlock()
	payload, _ := json.Marshal(map[string]bool{"running": r})
	s.broadcast(ssEvent{Event: "state", Data: string(payload)})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan ssEvent, 100)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	// Spela upp buffrad logghistorik så en klient som ansluter efteråt (eller
	// laddar om) ser vad som hänt — inte en tom logg.
	for _, payload := range s.recentLogs() {
		fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
	}
	flusher.Flush()

	// Initial state + stats skickas bara till den nyanslutna klienten —
	// en broadcast här skulle trigga en onödig disk-scan hos alla andra.
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	statePayload, _ := json.Marshal(map[string]bool{"running": running})
	fmt.Fprintf(w, "event: stats\ndata: %s\n\n", s.statsPayload())
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", statePayload)
	flusher.Flush()

	ka := time.NewTicker(15 * time.Second)
	defer ka.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ka.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
			flusher.Flush()
		}
	}
}
