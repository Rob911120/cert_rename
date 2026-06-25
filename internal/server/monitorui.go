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
//   1. öppna rutinen via mond://-hyperlänk (hårdkodade länkar nedan)
//   2. fokusera det nya fönstret som dök upp
//   3. ordernummer → Tab → ordernummer (intervallets från/till) → Ctrl+L (hämta)
//   4. Ctrl+S (spara/registrera) — bara om save=true
//
// Detta är inneboende bräckligt (fokus, timing, tangentlayout) och fungerar bara
// på Windows med Monitor installerat och mond://-protokollet registrerat.

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
// NYA fönstret som dök upp efter länken, fokuserar det, fyller i ordernumret i
// båda fälten, hämtar listan (Ctrl+L) och — om save — sparar (Ctrl+S). Ren
// funktion (ingen exec) så den kan enhetstestas på alla OS.
func buildReceivingScript(link, orderNumber string, save bool) string {
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
	// 2. Fokusera det nyaste fönstret som dök upp efter länken (= rutinen).
	b.WriteString("$after = [WinUtil]::List();\n")
	b.WriteString("$new = @($after | Where-Object { $before -notcontains $_ });\n")
	b.WriteString("$target = [IntPtr]::Zero;\n")
	b.WriteString("if ($new.Count -gt 0) { $target = $new[$new.Count - 1] }\n")
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
func runMonitorRoutine(link, orderNumber string, save bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("UI-styrning av Monitor stöds bara på Windows (denna app kör på %s)", runtime.GOOS)
	}
	script := buildReceivingScript(link, orderNumber, save)
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	hideConsole(cmd) // dölj PowerShell-konsolen (annars blinkar den fram + stjäl fokus)
	return cmd.Run()
}

// mond://-länkar till Monitor-rutinerna (hårdkodade för den här installationen).
const (
	monitorLinkReportArrival = "mond://001.1/150bc858-55f1-4453-9150-89d9ecabd63c"
	monitorLinkInspection    = "mond://001.1/6118e64b-734e-4878-bf53-ddcde3bc2b41"
)

// DriveMonitorRoutine öppnar rätt Monitor-rutin och fyller i ordernumret.
// routine är "report_arrival" eller "inspection". save=true skickar även Ctrl+S
// — men bara om MonitorUIAutoSave är på (säkerhetsspärr: tvinga aldrig fram en
// skrivning utan att det är uttryckligen tillåtet). Del av sickan.Notifier.
func (s *Server) DriveMonitorRoutine(routine, orderNumber string, save bool) error {
	s.mu.Lock()
	autoSave := s.cfg.MonitorUIAutoSave
	s.mu.Unlock()

	link := monitorLinkReportArrival
	if routine == "inspection" {
		link = monitorLinkInspection
	}
	return runMonitorRoutine(link, orderNumber, save && autoSave)
}
