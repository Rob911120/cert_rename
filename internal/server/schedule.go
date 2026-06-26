package server

// Schemat för "Kommande inleveranser": en väggklocks-poller (ticker ~5 min) som
// kör RefreshUpcoming vid konfigurerad tid, med catch-up om dagens körning
// missades (desktopen sov/var avstängd). Manuell "Kör nu" delar samma
// koalescerande kick + running-vakt så att de aldrig dubbelkörs.

import (
	"context"
	"encoding/json"
	"time"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
	"cert-renamer/internal/worker"
)

// schedulePollInterval är hur ofta väggklockan jämförs mot måltiden.
const schedulePollInterval = 5 * time.Minute

// MonitorClient uppfyller worker.Notifier — delegerar till den lazy login:en i
// ensureMonitor (en login kan logga ut operatörens skrivbordssession, så den
// sker inte vid app-start).
func (s *Server) MonitorClient() (*monitor.Client, error) {
	return s.ensureMonitor()
}

// StartUpcomingSchedule startar schemat på en ctx Server äger. Anropas från main
// (inte Autostart, som kan returnera tidigt, och inte NewMux). Två goroutines:
// en konsument som kör refresh på kick, och en poller som kickar enligt schemat.
func (s *Server) StartUpcomingSchedule() {
	ctx := context.Background()
	go s.upcomingRefreshLoop(ctx)
	go s.upcomingScheduleLoop(ctx)
}

func (s *Server) upcomingRefreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.refreshKick:
			s.runUpcomingRefresh(ctx)
		}
	}
}

func (s *Server) upcomingScheduleLoop(ctx context.Context) {
	s.maybeScheduledRefresh() // catch-up direkt vid uppstart om dagens körning missades
	t := time.NewTicker(schedulePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maybeScheduledRefresh()
		}
	}
}

// maybeScheduledRefresh kickar en körning om schemat är på och måltiden passerat
// utan körning sedan dess.
func (s *Server) maybeScheduledRefresh() {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if !cfg.UpcomingEnabled {
		return
	}
	last := s.lastUpcomingRun()
	if worker.ShouldCatchUp(last, time.Now(), cfg) {
		s.Logf("⏰ Schemalagd körning av kommande inleveranser (mål %s)", cfg.UpcomingTime)
		s.KickUpcoming()
	}
}

// KickUpcoming begär en refresh. Koalescerande: en redan köad kick räcker.
func (s *Server) KickUpcoming() {
	select {
	case s.refreshKick <- struct{}{}:
	default:
	}
}

// runUpcomingRefresh kör en refresh, skyddat av running-vakten (en i taget).
func (s *Server) runUpcomingRefresh(ctx context.Context) {
	if !s.refreshing.CompareAndSwap(false, true) {
		return // redan igång
	}
	defer s.refreshing.Store(false)

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	s.BroadcastUpcoming() // signalera "running=true" till UI:t

	mc, err := s.ensureMonitor()
	if err != nil {
		s.Logf("⚠️ Kommande inleveranser: %v", err)
		s.BroadcastUpcoming()
		return
	}
	if _, err := worker.RefreshUpcoming(ctx, mc, s.repo, cfg, s); err != nil {
		s.Logf("⚠️ Kommande inleveranser misslyckades: %v", err)
	} else if err := s.repo.SetAppState(store.AppStateLastUpcomingRun, time.Now().Format(time.RFC3339)); err != nil {
		s.Logf("⚠️ kunde inte spara last_run: %v", err)
	}
	s.BroadcastUpcoming()
}

func (s *Server) lastUpcomingRun() time.Time {
	v, err := s.repo.GetAppState(store.AppStateLastUpcomingRun)
	if err != nil || v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// upcomingResponse är UI-payloaden: raderna + schema-status. Delas av
// GET /api/upcoming och BroadcastUpcoming.
func (s *Server) upcomingResponse() map[string]any {
	rows, err := s.repo.ListUpcoming()
	if err != nil {
		s.Logf("⚠️ ListUpcoming: %v", err)
	}
	if rows == nil {
		rows = []store.UpcomingDelivery{}
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return map[string]any{
		"rows":        rows,
		"running":     s.refreshing.Load(),
		"enabled":     cfg.UpcomingEnabled,
		"time":        cfg.UpcomingTime,
		"window_days": cfg.UpcomingWindowDays,
		"back_days":   cfg.UpcomingBackDays,
		"last_run":    s.appStateString(store.AppStateLastUpcomingRun),
	}
}

func (s *Server) appStateString(key string) string {
	v, _ := s.repo.GetAppState(key)
	return v
}

// BroadcastUpcoming pushar raderna + schema-status till alla SSE-prenumeranter.
// Server-only-metod (inte på worker.Notifier-interfacet — bryter inga test-stubbar).
func (s *Server) BroadcastUpcoming() {
	payload, _ := json.Marshal(s.upcomingResponse())
	s.broadcast(ssEvent{Event: "upcoming", Data: string(payload)})
}
