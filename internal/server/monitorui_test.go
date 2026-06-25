package server

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildReceivingScript_FetchOnly(t *testing.T) {
	s := buildReceivingScript("mond://report", "B128756", false)
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
	if !strings.Contains(s, "mond://report") {
		t.Errorf("saknar länken: %s", s)
	}
}

func TestBuildReceivingScript_PicksNewWindow(t *testing.T) {
	// Fokusera det nyaste fönstret som dök upp efter länken.
	s := buildReceivingScript("mond://report", "B1", false)
	if !strings.Contains(s, "EnumWindows") {
		t.Errorf("ska enumerera fönster: %s", s)
	}
	if !strings.Contains(s, "$new[$new.Count - 1]") {
		t.Errorf("ska välja nyaste nya fönstret: %s", s)
	}
	if !strings.Contains(s, "WinUtil]::Focus") {
		t.Errorf("ska fokusera målfönstret innan SendKeys: %s", s)
	}
	if strings.Count(s, "B1") < 2 {
		t.Errorf("ordernumret ska skrivas i båda fälten: %s", s)
	}
}

func TestBuildReceivingScript_Save(t *testing.T) {
	s := buildReceivingScript("mond://report", "B1", true)
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
	if err := runMonitorRoutine("mond://x", "B1", false); err == nil {
		t.Error("UI-styrning ska ge fel på icke-Windows")
	}
}
