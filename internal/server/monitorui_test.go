package server

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildReceivingScript_FetchOnly(t *testing.T) {
	s := buildReceivingScript("monitor://report", "Monitor", "B128756", false)
	if strings.Count(s, "B128756") < 2 {
		t.Errorf("ordernumret ska skrivas i båda fälten (>=2 ggr): %s", s)
	}
	if !strings.Contains(s, "{TAB}") {
		t.Errorf("saknar Tab mellan fälten: %s", s)
	}
	if !strings.Contains(s, "^l") {
		t.Errorf("saknar Ctrl+L (hämta): %s", s)
	}
	if strings.Contains(s, "^s") {
		t.Errorf("Ctrl+S ska INTE skickas utan save: %s", s)
	}
	if !strings.Contains(s, "monitor://report") {
		t.Errorf("saknar länken: %s", s)
	}
}

func TestBuildReceivingScript_AppActivateGatedOnTitle(t *testing.T) {
	// Tom titel → ingen AppActivate (skriv direkt i fönstret länken öppnade).
	withoutTitle := buildReceivingScript("monitor://report", "", "B1", false)
	if strings.Contains(withoutTitle, "AppActivate") {
		t.Errorf("tom titel ska inte ge AppActivate: %s", withoutTitle)
	}
	// Satt titel → AppActivate (omslutet av try/catch så det inte avbryter).
	withTitle := buildReceivingScript("monitor://report", "Monitor", "B1", false)
	if !strings.Contains(withTitle, "AppActivate") {
		t.Errorf("satt titel ska ge AppActivate: %s", withTitle)
	}
	if !strings.Contains(withTitle, "try {") {
		t.Errorf("AppActivate ska vara omsluten av try/catch: %s", withTitle)
	}
	// Ordernumret ska skickas oavsett.
	if strings.Count(withoutTitle, "B1") < 2 {
		t.Errorf("ordernumret ska skrivas även utan titel: %s", withoutTitle)
	}
}

func TestBuildReceivingScript_Save(t *testing.T) {
	s := buildReceivingScript("monitor://report", "Monitor", "B1", true)
	if !strings.Contains(s, "^s") {
		t.Errorf("save=true ska skicka Ctrl+S: %s", s)
	}
}

func TestSendKeysEscape(t *testing.T) {
	if got := sendKeysEscape("A(B)+C"); got != "A{(}B{)}{+}C" {
		t.Errorf("sendKeysEscape = %q", got)
	}
	if got := sendKeysEscape("B128756"); got != "B128756" {
		t.Errorf("alfanumeriskt ska vara oförändrat: %q", got)
	}
}

func TestPSSingleQuote(t *testing.T) {
	if got := psSingleQuote("a'b"); got != "'a''b'" {
		t.Errorf("psSingleQuote = %q", got)
	}
}

func TestRunMonitorRoutine_NonWindowsErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("testet gäller icke-Windows")
	}
	if err := runMonitorRoutine("monitor://x", "Monitor", "B1", false); err == nil {
		t.Error("UI-styrning ska ge fel på icke-Windows")
	}
}
