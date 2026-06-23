// Package server implementerar HTTP-API + SSE för cert-renamer-UI:t.
// Paketet exporterar en *Server som även implementerar worker.Notifier
// och ai.Logger så att underliggande paket kan logga och broadcasta.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"cert-renamer/internal/store"
)

//go:embed ui/index.html ui/tailwind.css
var uiFS embed.FS

type Server struct {
	mu       sync.Mutex
	cfg      store.Config
	stats    store.Stats
	subs     map[chan ssEvent]struct{}
	cancelFn context.CancelFunc
	running  bool

	costsMu sync.Mutex
	costs   store.Costs

	sickanSess *sickanSessions

	uploadMu   sync.Mutex
	workerKick chan struct{}
}

// New bygger en ny Server med Config + Costs från disk.
func New() *Server {
	return &Server{
		cfg:        store.LoadConfig(),
		costs:      store.LoadCosts(),
		subs:       map[chan ssEvent]struct{}{},
		sickanSess: newSickanSessions(),
		workerKick: make(chan struct{}, 1),
	}
}

// Logf är en del av ai.Logger och worker.Notifier.
func (s *Server) Logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	log.Println(text)
	payload, _ := json.Marshal(map[string]string{
		"ts":   time.Now().Format("15:04:05"),
		"text": text,
	})
	s.broadcast(ssEvent{Event: "log", Data: string(payload)})
}

// IncrementOK ökar OK-räknaren — del av worker.Notifier.
func (s *Server) IncrementOK() { s.stats.OK.Add(1) }

// RecordUsage ackumulerar token-användning från ett Claude-anrop, sparar
// costs.json och broadcastar uppdaterad summa via SSE. Del av ai.Logger.
func (s *Server) RecordUsage(model string, in, out, cacheCreate, cacheRead int64) {
	s.costsMu.Lock()
	s.costs.Add(model, in, out, cacheCreate, cacheRead)
	snapshot := s.costs
	s.costsMu.Unlock()
	if err := store.SaveCosts(snapshot); err != nil {
		log.Printf("⚠️  SaveCosts: %v", err)
	}
	s.BroadcastCosts()
}

// Autostart startar workern om Autostart-flaggan är satt och preconds (api_key + inbox) uppfyllda.
// Avsedd att kallas en gång vid app-launch, gärna i en goroutine.
func (s *Server) Autostart() {
	s.mu.Lock()
	enabled := s.cfg.Autostart
	s.mu.Unlock()
	if !enabled {
		return
	}
	if err := s.startWorker(); err != nil {
		log.Printf("⚠️  autostart hoppades över: %v", err)
		return
	}
	log.Println("▶ autostart triggat")
}

// NewMux bygger upp http-routern med alla endpoints + UI-statics.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/pick-folder", s.handlePickFolder)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/review", s.handleReview)
	mux.HandleFunc("/api/approve", s.handleApprove)
	mux.HandleFunc("/api/archive", s.handleArchive)
	mux.HandleFunc("/api/promote-review", s.handlePromoteReview)
	mux.HandleFunc("/api/file", s.handleFile)
	mux.HandleFunc("/api/open", s.handleOpen)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/costs", s.handleCosts)
	mux.HandleFunc("/api/sickan/stream", s.handleSickanStream)
	mux.HandleFunc("/api/sickan/reset", s.handleSickanReset)
	mux.HandleFunc("/api/sickan/model", s.handleSickanModel)
	mux.HandleFunc("/api/upload", s.handleUpload)
	return mux
}
