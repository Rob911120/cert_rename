package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// Logf ska spara raden i ring-bufferten (för uppspelning till nya SSE-klienter)
// utöver att skriva till filloggen/broadcasta. Payloaden ska vara giltig JSON med
// ts + text.
func TestLogf_RecordsToBuffer(t *testing.T) {
	s := &Server{}
	s.Logf("hej %d", 1)
	s.Logf("då %s", "två")

	got := s.recentLogs()
	if len(got) != 2 {
		t.Fatalf("vill ha 2 buffrade rader, fick %d", len(got))
	}
	var first map[string]string
	if err := json.Unmarshal([]byte(got[0]), &first); err != nil {
		t.Fatalf("payload[0] inte JSON: %v", err)
	}
	if first["text"] != "hej 1" || first["ts"] == "" {
		t.Errorf("payload[0] = %v, vill ha text=\"hej 1\" + ts", first)
	}
	if !strings.Contains(got[1], "då två") {
		t.Errorf("payload[1] = %q, saknar \"då två\"", got[1])
	}
}

// Bufferten ska kapas till logBufMax och behålla de SENASTE raderna (äldst först).
func TestLogBuffer_CapsAtMaxKeepingNewest(t *testing.T) {
	s := &Server{}
	total := logBufMax + 50
	for i := 0; i < total; i++ {
		s.Logf("rad %d", i)
	}

	got := s.recentLogs()
	if len(got) != logBufMax {
		t.Fatalf("vill ha %d rader (kapat), fick %d", logBufMax, len(got))
	}
	// Äldsta kvarvarande raden ska vara nr (total-logBufMax), nyaste (total-1).
	wantFirst := "rad " + strconv.Itoa(total-logBufMax)
	wantLast := "rad " + strconv.Itoa(total-1)
	if !strings.Contains(got[0], wantFirst) {
		t.Errorf("första buffrade raden = %q, vill innehålla %q", got[0], wantFirst)
	}
	if !strings.Contains(got[len(got)-1], wantLast) {
		t.Errorf("sista buffrade raden = %q, vill innehålla %q", got[len(got)-1], wantLast)
	}
}
