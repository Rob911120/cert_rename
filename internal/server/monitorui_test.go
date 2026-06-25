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

func TestBuildReceivingScript_PicksNewWindow(t *testing.T) {
	// Tom titel → fokusera det nyaste fönstret som dök upp efter länken.
	noTitle := buildReceivingScript("monitor://report", "", "B1", false)
	if !strings.Contains(noTitle, "EnumWindows") {
		t.Errorf("ska enumerera fönster: %s", noTitle)
	}
	if !strings.Contains(noTitle, "$new[$new.Count - 1]") {
		t.Errorf("tom titel ska välja nyaste nya fönstret: %s", noTitle)
	}
	if !strings.Contains(noTitle, "WinUtil]::Focus") {
		t.Errorf("ska fokusera målfönstret innan SendKeys: %s", noTitle)
	}
	if strings.Count(noTitle, "B1") < 2 {
		t.Errorf("ordernumret ska skrivas i båda fälten: %s", noTitle)
	}
	// Satt titel → filtrera nya fönster på titeln.
	withTitle := buildReceivingScript("monitor://report", "Rapportera inleverans", "B1", false)
	if !strings.Contains(withTitle, "*Rapportera inleverans*") {
		t.Errorf("satt titel ska filtrera på titeln: %s", withTitle)
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
