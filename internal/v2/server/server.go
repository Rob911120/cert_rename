// Package server är V2:s HTTP-yta: inbäddat UI, tunna handlers mot
// app-servicen och en SSE-buss. Handlers innehåller ingen affärslogik.
package server

import (
	"database/sql"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	v1store "cert-renamer/internal/store"
)

//go:embed ui
var uiFS embed.FS

// Server bär V2:s delade tillstånd. Konfig muteras bara via handleConfig och
// läses under lås — samma enkla modell som V1.
type Server struct {
	DB  *sql.DB
	Log *slog.Logger

	mu  sync.RWMutex
	cfg v1store.Config
}

func New(cfg v1store.Config, db *sql.DB, log *slog.Logger) *Server {
	return &Server{DB: db, Log: log, cfg: cfg}
}

// Config returnerar en kopia av aktuell konfig (säker att läsa utan lås utåt).
func (s *Server) Config() v1store.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// NewMux registrerar alla routes. Fler endpoints tillkommer i takt med
// milstolparna; det som finns här är alltid komplett för det som är byggt.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()

	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // omöjligt: katalogen är inbäddad vid kompilering
	}
	mux.Handle("GET /", http.FileServerFS(ui))

	mux.HandleFunc("GET /api/health", s.handleHealth)

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.Ping(); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
