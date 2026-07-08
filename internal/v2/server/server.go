// Package server är V2:s HTTP-yta: inbäddat UI, tunna handlers mot
// app-servicen (den enda skrivvägen) och en SSE-buss. Ingen affärslogik här.
package server

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/intake"
	"cert-renamer/internal/v2/monitor"
	"cert-renamer/internal/v2/monitorsync"
	"cert-renamer/internal/v2/store"
)

//go:embed ui
var uiFS embed.FS

// Server bär delat tillstånd. Den implementerar både app.Notifier (Logf +
// OverviewChanged → SSE) och ai.Logger (Logf + RecordUsage → costs.json),
// så alla lager loggar och räknar kostnad genom samma nål.
type Server struct {
	DB   *sql.DB
	Slog *slog.Logger
	Repo *store.Repository
	App  *app.App

	mu         sync.Mutex
	cfg        store.Config
	running    bool
	stopWorker context.CancelFunc
	intakeInst *intake.Intake // satt medan workern kör (PDF-upload behöver den)
	mon        *monitor.Client

	monConnectMu sync.Mutex

	subsMu sync.Mutex
	subs   map[chan ssEvent]struct{}

	logMu  sync.Mutex
	logBuf []string

	ovMu      sync.Mutex
	ovTimer   *time.Timer
	ovPending bool

	costsMu sync.Mutex
	costs   store.Costs

	intakeKick chan struct{}
	syncKick   chan struct{}

	sickanSess sickanSessions
}

func New(cfg store.Config, db *sql.DB, logger *slog.Logger) *Server {
	s := &Server{
		DB:         db,
		Slog:       logger,
		Repo:       store.NewRepository(db),
		cfg:        cfg,
		subs:       map[chan ssEvent]struct{}{},
		costs:      store.LoadCosts(),
		intakeKick: make(chan struct{}, 1),
		syncKick:   make(chan struct{}, 1),
	}
	s.App = app.New(s.Repo, s.Config, time.Now, s)
	return s
}

// Config returnerar en kopia av aktuell konfig.
func (s *Server) Config() store.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Server) setConfig(cfg store.Config) {
	s.mu.Lock()
	// Släpp Monitor-klienten BARA när uppgifterna faktiskt ändrats: varje ny
	// login loggar ut Robs interaktiva Monitor-session, så ett orelaterat
	// config-spar (temabyte, inbox) får inte tvinga fram en om-login.
	if cfg.MonitorURL != s.cfg.MonitorURL ||
		cfg.MonitorUser != s.cfg.MonitorUser ||
		cfg.MonitorPassword != s.cfg.MonitorPassword {
		s.mon = nil
	}
	s.cfg = cfg
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// app.Notifier + ai.Logger
// ---------------------------------------------------------------------------

// Logf skriver till loggen, ring-bufferten och alla SSE-klienter.
func (s *Server) Logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	s.Slog.Info(text)
	s.recordAndBroadcastLog(text)
}

// overviewThrottle är fönstret för att koalescera overview-pingar.
const overviewThrottle = 750 * time.Millisecond

// OverviewChanged pingar klienterna att refetcha /api/overview, koalescerat
// (leading + trailing): FÖRSTA ändringen sänds direkt så interaktiva klick
// (bekräfta, spara) känns omedelbara; följande ändringar inom fönstret slås
// ihop till EN eftersläpande ping. Det hindrar en lång Monitor-sync från att
// trigga hundratals fulla /api/overview-refetchar (en per rad) som förr dränkte
// den enda DB-anslutningen och frös UI:t.
func (s *Server) OverviewChanged() {
	s.ovMu.Lock()
	defer s.ovMu.Unlock()
	if s.ovTimer == nil {
		// Inget öppet fönster — sänd direkt (leading edge) och öppna ett.
		s.broadcast(ssEvent{Event: "overview", Data: "{}"})
		s.ovTimer = time.AfterFunc(overviewThrottle, s.flushOverview)
		return
	}
	// Fönster redan öppet — markera bara att en eftersläpande ping behövs.
	s.ovPending = true
}

// flushOverview sänder en koalescerad ping om ändringar samlats under fönstret
// och håller då fönstret öppet ytterligare en period (så en pågående sync ger
// högst ~1 ping per fönster). Utan väntande ändringar stängs fönstret.
func (s *Server) flushOverview() {
	s.ovMu.Lock()
	defer s.ovMu.Unlock()
	if s.ovPending {
		s.broadcast(ssEvent{Event: "overview", Data: "{}"})
		s.ovPending = false
		s.ovTimer = time.AfterFunc(overviewThrottle, s.flushOverview)
		return
	}
	s.ovTimer = nil
}

// RecordUsage ackumulerar tokenkostnad (delar costs.json med V1) och
// broadcastar summan. SaveCosts körs under låset: två samtidiga anrop skriver
// annars samma costs.json.tmp om varandra (interleavad tmp → korrupt fil).
func (s *Server) RecordUsage(model string, in, out, cacheCreate, cacheRead int64) {
	s.costsMu.Lock()
	s.costs.Add(model, in, out, cacheCreate, cacheRead)
	if err := store.SaveCosts(s.costs); err != nil {
		s.Slog.Warn("SaveCosts", "err", err)
	}
	s.costsMu.Unlock()
	s.broadcastCosts()
}

// ---------------------------------------------------------------------------
// Worker (intag) — start/stopp
// ---------------------------------------------------------------------------

func (s *Server) StartWorker() error {
	// OBS: släpp s.mu INNAN broadcastState()/go in.Run() — båda tar s.mu igen
	// (workerRunning resp. in.Config), och Go-mutex är inte återinträdbar. Med
	// defer-lås skulle broadcastState självlåsa och hänga hela servern (samma
	// mönster som StopWorker, som låser upp före broadcast).
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	if s.cfg.ApiKey == "" {
		s.mu.Unlock()
		return errors.New("ingen API-nyckel konfigurerad — öppna ⚙️ Inställningar")
	}
	if s.cfg.InboxDir == "" {
		s.mu.Unlock()
		return errors.New("ingen inkorgsmapp konfigurerad")
	}
	if err := store.EnsureDirs(s.cfg); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("skapa V2-mappar: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	in := &intake.Intake{App: s.App, AI: intake.NewClaude(s.cfg.ApiKey, s), Config: s.Config}
	s.intakeInst, s.stopWorker, s.running = in, cancel, true
	s.mu.Unlock()

	go in.Run(ctx, s.intakeKick)
	s.broadcastState()
	return nil
}

func (s *Server) StopWorker() {
	s.mu.Lock()
	if s.stopWorker != nil {
		s.stopWorker()
	}
	s.running, s.stopWorker, s.intakeInst = false, nil, nil
	s.mu.Unlock()
	s.broadcastState()
}

func (s *Server) workerRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Server) currentIntake() *intake.Intake {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intakeInst
}

// Autostart startar workern vid app-launch om flaggan är satt.
func (s *Server) Autostart() {
	if !s.Config().Autostart {
		return
	}
	if err := s.StartWorker(); err != nil {
		s.Logf("⚠️  autostart hoppades över: %v", err)
		return
	}
	s.Logf("▶ autostart triggat")
}

// ---------------------------------------------------------------------------
// Monitor-sync — lazy klient + schemaloop
// ---------------------------------------------------------------------------

// ensureMonitor loggar in lazy vid första behov (varje login loggar ut den
// interaktiva Monitor-sessionen, så vi loggar aldrig in vid app-start).
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
		return nil, errors.New("Monitor-anslutning saknas — fyll i URL, användare och lösenord i ⚙️ Inställningar")
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

// RunSyncScheduler är sync-loopen: 5-min-poller mot ShouldCatchUp (gated på
// UpcomingEnabled) + manuell kick via /api/refresh (kör alltid — knappen är
// explicit). Körs tills ctx avbryts.
func (s *Server) RunSyncScheduler(ctx context.Context) {
	ticker := time.NewTicker(monitorsync.SchedulePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.syncKick:
			s.runSync(ctx, "manuell")
		case <-ticker.C:
			cfg := s.Config()
			if !cfg.UpcomingEnabled {
				continue
			}
			lastRun, _ := time.Parse(time.RFC3339, s.App.State(ctx, "last_sync"))
			if monitorsync.ShouldCatchUp(lastRun, time.Now(), cfg.UpcomingTime) {
				s.runSync(ctx, "schemalagd")
			}
		}
	}
}

func (s *Server) runSync(ctx context.Context, why string) {
	mc, err := s.ensureMonitor()
	if err != nil {
		s.Logf("❌ Monitor-sync: %v", err)
		return
	}
	sync := &monitorsync.Sync{App: s.App, ERP: mc, Judge: s.judge(), Config: s.Config}
	s.Logf("🔄 Monitor-sync (%s)…", why)
	if n, err := sync.Refresh(ctx); err != nil {
		s.Logf("❌ Monitor-sync: %v", err)
	} else {
		s.Logf("✅ Monitor-sync klar: %d rader", n)
	}
}

// KickSync triggar en refresh utan att blockera (coalescing: full kanal = en
// körning är redan på väg).
func (s *Server) KickSync() {
	select {
	case s.syncKick <- struct{}{}:
	default:
	}
}

// KickIntake triggar en omedelbar inbox-scan (efter upload).
func (s *Server) KickIntake() {
	select {
	case s.intakeKick <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------
// Mux
// ---------------------------------------------------------------------------

func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()

	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // omöjligt: katalogen är inbäddad vid kompilering
	}
	mux.Handle("GET /", staticHandler(ui))

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config", s.handleConfigPost)
	mux.HandleFunc("GET /api/costs", s.handleCosts)

	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("GET /api/pdf", s.handlePDF)

	mux.HandleFunc("POST /api/cert/update", s.handleCertUpdate)
	mux.HandleFunc("POST /api/cert/name", s.handleCertName)
	mux.HandleFunc("POST /api/cert/save", s.handleCertSave)
	mux.HandleFunc("POST /api/cert/archive", s.handleCertArchive)
	mux.HandleFunc("POST /api/cert/unarchive", s.handleCertUnarchive)

	mux.HandleFunc("POST /api/link", s.handleLink)
	mux.HandleFunc("POST /api/link/reject", s.handleLinkReject)

	mux.HandleFunc("POST /api/row/delivered", s.handleRowDelivered)

	mux.HandleFunc("POST /api/note", s.handleNoteAdd)
	mux.HandleFunc("POST /api/note/delete", s.handleNoteDelete)

	mux.HandleFunc("GET /api/tasks", s.handleTasksList)
	mux.HandleFunc("POST /api/tasks", s.handleTaskAdd)
	mux.HandleFunc("POST /api/tasks/done", s.handleTaskDone)
	mux.HandleFunc("POST /api/tasks/delete", s.handleTaskDelete)

	mux.HandleFunc("POST /api/upload", s.handleUpload)
	mux.HandleFunc("POST /api/start", s.handleStart)
	mux.HandleFunc("POST /api/stop", s.handleStop)

	mux.HandleFunc("POST /api/sickan/stream", s.handleSickanStream)
	mux.HandleFunc("POST /api/sickan/reset", s.handleSickanReset)
	mux.HandleFunc("POST /api/sickan/model", s.handleSickanModel)

	return mux
}

// staticHandler serverar det inbäddade UI:t men TVINGAR rätt Content-Type per
// filändelse innan http.FileServer får sätta sin. På Windows läser FileServer
// MIME-typen ur registret (HKEY_CLASSES_ROOT\.js), där .js ofta är felregistrerad
// som text/plain av annan installerad mjukvara. Webbläsaren vägrar då köra
// <script type="module"> (strikt MIME-koll) och HELA gränssnittet dör tyst —
// ingen SSE-logg, inga rader, inställningsrutan går inte att öppna. Genom att
// sätta headern explicit kringgår vi registret helt (ServeContent respekterar en
// redan satt Content-Type och skriver inte över den).
func staticHandler(ui fs.FS) http.Handler {
	fileServer := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := contentTypeByExt(r.URL.Path); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		fileServer.ServeHTTP(w, r)
	})
}

// contentTypeByExt ger en stabil, register-oberoende MIME-typ för de filtyper UI:t
// levererar. Tom sträng = lämna åt FileServer att gissa (t.ex. för "/" → index.html,
// där html-detektionen är ofarlig).
func contentTypeByExt(p string) string {
	p = strings.ToLower(p)
	switch {
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".json"):
		return "application/json"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml; charset=utf-8"
	}
	return ""
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.Ping(); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
