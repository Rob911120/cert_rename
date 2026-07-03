package monitorsync

import (
	"context"
	"time"

	v1store "cert-renamer/internal/store"
)

// Rena schemafunktioner (injicerad klocka, tabelltestbara) + drift-loopen.
// Samma semantik som V1 (worker/upcoming.go NextRun/ShouldCatchUp): en daglig
// väggklockstid, med catch-up om appen var avstängd/sovande över måltiden.

// NextRun är nästa körning: dagens upcomingTime om den inte passerat, annars
// morgondagens.
func NextRun(now time.Time, upcomingTime string) time.Time {
	hh, mm := parseHHMM(upcomingTime)
	target := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// ShouldCatchUp: dagens schemalagda tid har passerat och ingen körning har
// skett sedan dess. lastRun==zero (aldrig körd) → kör om tiden passerat.
func ShouldCatchUp(lastRun, now time.Time, upcomingTime string) bool {
	hh, mm := parseHHMM(upcomingTime)
	todayTarget := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if now.Before(todayTarget) {
		return false
	}
	return lastRun.Before(todayTarget)
}

func parseHHMM(s string) (int, int) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		t, _ = time.Parse("15:04", v1store.DefaultUpcomingTime)
	}
	return t.Hour(), t.Minute()
}

const schedulePoll = 5 * time.Minute

// RunScheduled kör refresh-jobbet på schema tills ctx avbryts: 5-minuters-
// poller mot ShouldCatchUp (gated på UpcomingEnabled), plus kick-kanal för
// manuell "Uppdatera" (kör alltid, oavsett gate — knappen är explicit).
// Senaste körning läses/skrivs i app_state ("last_sync") så catch-up
// överlever omstarter.
func (s *Sync) RunScheduled(ctx context.Context, kick <-chan struct{}) {
	ticker := time.NewTicker(schedulePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			s.runOnce(ctx, "manuell")
		case <-ticker.C:
			cfg := s.Config()
			if !cfg.UpcomingEnabled {
				continue
			}
			lastRun, _ := time.Parse(time.RFC3339, s.App.State(ctx, "last_sync"))
			if ShouldCatchUp(lastRun, time.Now(), cfg.UpcomingTime) {
				s.runOnce(ctx, "schemalagd")
			}
		}
	}
}

func (s *Sync) runOnce(ctx context.Context, why string) {
	n, err := s.App.Notify, error(nil)
	n.Logf("🔄 Monitor-sync (%s) startar…", why)
	_, err = s.Refresh(ctx)
	if err != nil {
		n.Logf("❌ Monitor-sync: %v", err)
	}
}
