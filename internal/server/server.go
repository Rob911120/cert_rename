// Package server implementerar HTTP-API + SSE för cert-renamer-UI:t.
// Paketet exporterar en *Server som även implementerar worker.Notifier
// och ai.Logger så att underliggande paket kan logga och broadcasta.
package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"cert-renamer/internal/monitor"
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

	db   *sql.DB
	repo *store.Repository

	// mon är Monitor-ERP-klienten (nil tills lazy inloggad vid första
	// API-användningen — se ensureMonitor). Skyddas av mu. monConnectMu
	// serialiserar själva inloggningen.
	mon          *monitor.Client
	monConnectMu sync.Mutex

	costsMu sync.Mutex
	costs   store.Costs

	sickanSess *sickanSessions

	uploadMu   sync.Mutex
	workerKick chan struct{}
}

// New bygger en ny Server med Config + Costs från disk och initierar databasen.
func New() *Server {
	dbPath := store.DBPath()
	db, err := store.InitDB(dbPath)
	if err != nil {
		log.Fatalf("❌ Kunde inte initiera databas: %v", err)
	}

	srv := &Server{
		cfg:        store.LoadConfig(),
		db:         db,
		repo:       store.NewRepository(db),
		costs:      store.LoadCosts(),
		subs:       map[chan ssEvent]struct{}{},
		sickanSess: newSickanSessions(),
		workerKick: make(chan struct{}, 1),
	}

	// Kör reconciliation om inbox är satt
	if srv.cfg.InboxDir != "" {
		queueDir := store.QueueDir(srv.cfg)
		if _, statErr := os.Stat(queueDir); statErr == nil {
			added, removed, recErr := srv.repo.ReconcileQueue(queueDir)
			if recErr != nil {
				log.Printf("⚠️  Reconciliation misslyckades: %v", recErr)
			} else if added > 0 || removed > 0 {
				log.Printf("🗄️  Reconciliation: lade till %d, tog bort %d", added, removed)
			}
		}
	}

	// Ingen Monitor-inloggning vid start: varje login skickar ForceRelogin:true
	// och loggar ut den interaktiva Monitor-sessionen. Vi loggar in lazy först
	// när ett verktyg faktiskt behöver läsa via API:t (ensureMonitor).

	return srv
}

// ensureMonitor returnerar en inloggad Monitor-klient och loggar in lazy vid
// första anropet. Serialiserad så samtidiga förstaanvändningar inte loggar in
// i kapp. Anropas av sickan-verktygen (via Toolbox.MonitorConnect) precis när
// de behöver API:t — inte vid app-start.
func (s *Server) ensureMonitor() (*monitor.Client, error) {
	s.monConnectMu.Lock()
	defer s.monConnectMu.Unlock()

	s.mu.Lock()
	mon := s.mon
	url, user, pass := s.cfg.MonitorURL, s.cfg.MonitorUser, s.cfg.MonitorPassword
	s.mu.Unlock()
	if mon != nil {
		return mon, nil
	}
	if url == "" || user == "" || pass == "" {
		return nil, fmt.Errorf("Monitor-anslutning saknas — fyll i URL, användarnamn och lösenord i ⚙️ Inställningar")
	}
	mc := monitor.New(url)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mc.Login(ctx, user, pass); err != nil {
		return nil, fmt.Errorf("Monitor-login misslyckades: %w", err)
	}
	s.mu.Lock()
	s.mon = mc
	s.mu.Unlock()
	s.Logf("🔌 Monitor inloggad mot %s", url)
	return mc, nil
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

// Repo returnerar repositoryn — del av worker.Notifier.
func (s *Server) Repo() *store.Repository {
	return s.repo
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
	mux.HandleFunc("/api/upload-delivery-note", s.handleUploadDeliveryNote)
	return mux
}
