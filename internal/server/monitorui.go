package server

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Monitor UI-automation: när skriv-API:t inte är licensierat styr vi
// Monitor-skrivbordsklienten direkt. Mekanismen är samma som dialog.go —
// PowerShell + .NET, ingen CGO. Flödet i klienten:
//   1. öppna rutinen via monitor://-hyperlänk
//   2. ordernummer → Tab → ordernummer (intervallets från/till) → Ctrl+L (hämta)
//   3. Ctrl+S (spara/registrera) — bara om save=true
//
// Detta är inneboende bräckligt (fokus, timing, fönstertitel, tangentlayout) och
// fungerar bara på Windows med Monitor installerat och monitor://-protokollet
// registrerat.

// monitorOpenDelayMs är pausen efter att rutinen öppnats innan vi aktiverar
// fönstret och börjar skicka tangenter. monitorSaveDelayMs är pausen efter
// Ctrl+L (listan ska hinna hämtas) innan ett ev. Ctrl+S.
const (
	monitorOpenDelayMs = 1500
	monitorStepDelayMs = 300
	monitorSaveDelayMs = 1200
)

// sendKeysEscape escapar SendKeys-metatecken så ett ordernummer skickas
// ordagrant. Metatecknen + ^ % ~ ( ) { } [ ] omsluts av {}.
func sendKeysEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '+', '^', '%', '~', '(', ')', '{', '}', '[', ']':
			b.WriteByte('{')
			b.WriteRune(r)
			b.WriteByte('}')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// psSingleQuote escapar en sträng för en PowerShell single-quoted literal
// (enkel-citat dubbleras).
func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// buildReceivingScript bygger PowerShell-skriptet som öppnar rutinen, fyller i
// ordernumret i båda fälten, hämtar listan (Ctrl+L) och — om save — sparar
// (Ctrl+S). Ren funktion (ingen exec) så den kan enhetstestas på alla OS.
func buildReceivingScript(link, windowTitle, orderNumber string, save bool) string {
	keys := sendKeysEscape(orderNumber)
	var b strings.Builder
	b.WriteString("Add-Type -AssemblyName Microsoft.VisualBasic;\n")
	b.WriteString("Add-Type -AssemblyName System.Windows.Forms;\n")
	fmt.Fprintf(&b, "Start-Process %s;\n", psSingleQuote(link))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorOpenDelayMs)
	fmt.Fprintf(&b, "[Microsoft.VisualBasic.Interaction]::AppActivate(%s);\n", psSingleQuote(windowTitle))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorStepDelayMs)
	// ordernummer → Tab → ordernummer → Ctrl+L
	fmt.Fprintf(&b, "[System.Windows.Forms.SendKeys]::SendWait(%s);\n", psSingleQuote(keys+"{TAB}"+keys))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorStepDelayMs)
	b.WriteString("[System.Windows.Forms.SendKeys]::SendWait('^l');\n")
	if save {
		fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorSaveDelayMs)
		b.WriteString("[System.Windows.Forms.SendKeys]::SendWait('^s');\n")
	}
	return b.String()
}

// runMonitorRoutine kör skriptet via PowerShell på Windows. På andra OS
// returneras ett tydligt fel (mekanismen finns bara där Monitor-klienten kör).
func runMonitorRoutine(link, windowTitle, orderNumber string, save bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("UI-styrning av Monitor stöds bara på Windows (denna app kör på %s)", runtime.GOOS)
	}
	if link == "" {
		return fmt.Errorf("ingen monitor://-länk konfigurerad för rutinen — fyll i den under ⚙️ Inställningar")
	}
	script := buildReceivingScript(link, windowTitle, orderNumber, save)
	return exec.Command("powershell", "-NoProfile", "-Command", script).Run()
}

// DriveMonitorRoutine slår upp rätt länk/fönstertitel ur konfigurationen och
// styr Monitor-klienten. routine är "report_arrival" eller "inspection".
// save=true skickar även Ctrl+S — men bara om MonitorUIAutoSave är på (annars
// är save en no-op-spärr: tvinga aldrig fram en skrivning utan att det är
// uttryckligen tillåtet i inställningarna). Del av sickan.Notifier.
func (s *Server) DriveMonitorRoutine(routine, orderNumber string, save bool) error {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()

	windowTitle := c.MonitorWindowTitle
	if windowTitle == "" {
		windowTitle = "Monitor"
	}
	var link string
	switch routine {
	case "inspection":
		link = c.MonitorLinkInspection
	default: // "report_arrival"
		link = c.MonitorLinkReportArrival
	}

	// Säkerhetsspärr: skicka aldrig Ctrl+S om inte auto-spara är påslaget i
	// inställningarna, även om anroparen bett om det.
	doSave := save && c.MonitorUIAutoSave
	return runMonitorRoutine(link, windowTitle, orderNumber, doSave)
}
