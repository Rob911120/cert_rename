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

// monitorOpenDelayMs är pausen efter att rutinen öppnats så fönstret hinner
// öppnas riktigt innan vi aktiverar det och börjar skriva. monitorStepDelayMs är
// pausen efter AppActivate (fönstret ska hinna ta fokus) och mellan tangentsteg.
// monitorSaveDelayMs är pausen efter Ctrl+L (listan ska hinna hämtas) innan
// ett ev. Ctrl+S.
const (
	monitorOpenDelayMs = 3000
	monitorStepDelayMs = 500
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

// winFocusCSharp är en C#-hjälpklass (kompileras av PowerShell via Add-Type) som
// enumererar synliga topp-fönster och kan fokusera ett givet fönster. Vi
// använder den för att hitta det NYA fönstret som Monitor öppnar efter att vi
// kört monitor://-länken, och flytta förgrundsfokus dit innan vi skriver — så
// tangenterna hamnar i rutinen och inte i appen som hade fokus (t.ex. chatten).
// AttachThreadInput-tricket krävs för att SetForegroundWindow ska få stjäla
// fokus. Skrivet i C# 2-kompatibel stil (anonym delegate, ingen var/out _) så
// den kompilerar även i Windows PowerShell 5.1.
const winFocusCSharp = `using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public class WinUtil {
    [DllImport("user32.dll")] static extern bool EnumWindows(EnumProc cb, IntPtr p);
    delegate bool EnumProc(IntPtr h, IntPtr p);
    [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] static extern int GetWindowTextLength(IntPtr h);
    [DllImport("user32.dll")] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
    [DllImport("user32.dll")] static extern bool SetForegroundWindow(IntPtr h);
    [DllImport("user32.dll")] static extern bool BringWindowToTop(IntPtr h);
    [DllImport("user32.dll")] static extern bool ShowWindow(IntPtr h, int c);
    [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
    [DllImport("kernel32.dll")] static extern uint GetCurrentThreadId();
    [DllImport("user32.dll")] static extern bool AttachThreadInput(uint a, uint b, bool f);
    public static List<IntPtr> List() {
        List<IntPtr> r = new List<IntPtr>();
        EnumWindows(delegate(IntPtr h, IntPtr p) { if (IsWindowVisible(h) && GetWindowTextLength(h) > 0) r.Add(h); return true; }, IntPtr.Zero);
        return r;
    }
    public static string Title(IntPtr h) {
        int n = GetWindowTextLength(h);
        StringBuilder sb = new StringBuilder(n + 1);
        GetWindowText(h, sb, sb.Capacity);
        return sb.ToString();
    }
    public static void Focus(IntPtr h) {
        IntPtr fg = GetForegroundWindow();
        uint pid;
        uint t1 = GetWindowThreadProcessId(fg, out pid);
        uint t2 = GetCurrentThreadId();
        AttachThreadInput(t2, t1, true);
        ShowWindow(h, 9);
        BringWindowToTop(h);
        SetForegroundWindow(h);
        AttachThreadInput(t2, t1, false);
    }
}`

// buildReceivingScript bygger PowerShell-skriptet som öppnar rutinen, hittar det
// NYA fönstret som dök upp, fokuserar det, fyller i ordernumret i båda fälten,
// hämtar listan (Ctrl+L) och — om save — sparar (Ctrl+S). Ren funktion (ingen
// exec) så den kan enhetstestas på alla OS. windowTitle är valfri: är den satt
// föredrar vi ett nytt fönster vars titel matchar; annars tas det nyaste nya
// fönstret.
func buildReceivingScript(link, windowTitle, orderNumber string, save bool) string {
	keys := sendKeysEscape(orderNumber)
	var b strings.Builder
	b.WriteString("Add-Type -AssemblyName System.Windows.Forms;\n")
	b.WriteString("Add-Type @\"\n")
	b.WriteString(winFocusCSharp)
	b.WriteString("\n\"@;\n")
	// 1. Ögonblicksbild av fönstren INNAN vi öppnar rutinen.
	b.WriteString("$before = [WinUtil]::List();\n")
	fmt.Fprintf(&b, "Start-Process %s;\n", psSingleQuote(link))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorOpenDelayMs)
	// 2. Vilka fönster är NYA efter länken?
	b.WriteString("$after = [WinUtil]::List();\n")
	b.WriteString("$new = @($after | Where-Object { $before -notcontains $_ });\n")
	b.WriteString("$target = [IntPtr]::Zero;\n")
	if windowTitle != "" {
		t := psSingleQuote("*" + windowTitle + "*")
		fmt.Fprintf(&b, "foreach ($w in $new) { if ([WinUtil]::Title($w) -like %s) { $target = $w; break } }\n", t)
		fmt.Fprintf(&b, "if ($target -eq [IntPtr]::Zero) { foreach ($w in $after) { if ([WinUtil]::Title($w) -like %s) { $target = $w; break } } }\n", t)
		b.WriteString("if ($target -eq [IntPtr]::Zero -and $new.Count -gt 0) { $target = $new[$new.Count - 1] }\n")
	} else {
		b.WriteString("if ($new.Count -gt 0) { $target = $new[$new.Count - 1] }\n")
	}
	// 3. Fokusera målfönstret innan vi skriver.
	fmt.Fprintf(&b, "if ($target -ne [IntPtr]::Zero) { [WinUtil]::Focus($target); Start-Sleep -Milliseconds %d }\n", monitorStepDelayMs)
	// 4. ordernummer → Tab → ordernummer → Ctrl+L
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
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	hideConsole(cmd) // dölj PowerShell-konsolen (annars blinkar den fram + stjäl fokus)
	return cmd.Run()
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

	// Tom fönstertitel = hoppa över AppActivate och skriv direkt i fönstret som
	// monitor://-länken öppnade (default — undviker fokus-ryck till huvudfönstret).
	// Sätt en titel i Inställningar bara om rutinen behöver aktiveras explicit.
	windowTitle := c.MonitorWindowTitle
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
