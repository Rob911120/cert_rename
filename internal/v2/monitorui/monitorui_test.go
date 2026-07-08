package monitorui

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildReceivingScript(t *testing.T) {
	s := BuildReceivingScript(LinkReportArrival, "B127562", false)
	if !strings.Contains(s, LinkReportArrival) {
		t.Error("skriptet ska öppna rutinlänken")
	}
	if !strings.Contains(s, "B127562{TAB}B127562") {
		t.Error("ordernumret ska fyllas i båda fälten via Tab")
	}
	if !strings.Contains(s, "SendWait('^l')") {
		t.Error("skriptet ska hämta listan med Ctrl+L")
	}
	if strings.Contains(s, "SendWait('^s')") {
		t.Error("utan save ska INGET Ctrl+S skickas")
	}

	withSave := BuildReceivingScript(LinkReportArrival, "B1", true)
	if !strings.Contains(withSave, "SendWait('^s')") {
		t.Error("med save ska Ctrl+S skickas")
	}
}

func TestSendKeysEscape(t *testing.T) {
	if got := sendKeysEscape("A+B(C)"); got != "A{+}B{(}C{)}" {
		t.Errorf("metatecken ej escapade: %q", got)
	}
}

func TestDriveNonWindowsErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kör bara på icke-Windows")
	}
	if err := Drive("report_arrival", "B1", false); err == nil {
		t.Error("Drive ska returnera fel på icke-Windows")
	}
}
