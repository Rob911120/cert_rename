package monitorsync

import (
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

// SchedulePoll är hur ofta drift-loopen (server.RunSyncScheduler) pollar
// ShouldCatchUp. Loopen bor i servern — den äger config och kick-kanalen.
const SchedulePoll = 5 * time.Minute
