package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// En SSE-buss, fyra eventtyper: log (rad), state (worker på/av), overview
// (ping → klienten refetchar /api/overview), costs (summa). Inga tunga
// payloads över SSE — datat hämtas alltid via GET /api/overview.

type ssEvent struct {
	Event string
	Data  string
}

const logBufMax = 300

func (s *Server) broadcast(ev ssEvent) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // långsam klient får inte blockera bussen
		}
	}
}

func (s *Server) recordAndBroadcastLog(text string) {
	payload, _ := json.Marshal(map[string]string{
		"ts":   time.Now().Format("15:04:05"),
		"text": text,
	})
	s.logMu.Lock()
	s.logBuf = append(s.logBuf, string(payload))
	if len(s.logBuf) > logBufMax {
		s.logBuf = append([]string(nil), s.logBuf[len(s.logBuf)-logBufMax:]...)
	}
	s.logMu.Unlock()
	s.broadcast(ssEvent{Event: "log", Data: string(payload)})
}

func (s *Server) recentLogs() []string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return append([]string(nil), s.logBuf...)
}

func (s *Server) broadcastState() {
	payload, _ := json.Marshal(map[string]bool{"running": s.workerRunning()})
	s.broadcast(ssEvent{Event: "state", Data: payload2string(payload)})
}

func (s *Server) broadcastCosts() {
	s.costsMu.Lock()
	payload, _ := json.Marshal(s.costs)
	s.costsMu.Unlock()
	s.broadcast(ssEvent{Event: "costs", Data: string(payload)})
}

func payload2string(b []byte) string { return string(b) }

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming stöds inte", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan ssEvent, 100)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subs, ch)
		s.subsMu.Unlock()
	}()

	// Spela upp loggbufferten så en omladdad klient ser historiken.
	for _, payload := range s.recentLogs() {
		fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
	}
	// Initialt tillstånd bara till den nya klienten.
	statePayload, _ := json.Marshal(map[string]bool{"running": s.workerRunning()})
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", statePayload)
	s.costsMu.Lock()
	costsPayload, _ := json.Marshal(s.costs)
	s.costsMu.Unlock()
	fmt.Fprintf(w, "event: costs\ndata: %s\n\n", costsPayload)
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
