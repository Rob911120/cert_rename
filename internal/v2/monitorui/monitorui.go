// Package monitorui styr Monitor-SKRIVBORDSKLIENTEN via UI-automation
// (PowerShell + .NET SendKeys, ingen CGO) — enda vägen att registrera en
// inleverans eftersom Monitors skriv-API inte är licensierat. Porterat oförändrat
// från V1 (_archive/internal/server/monitorui.go). Windows-only; på andra OS
// returneras ett tydligt fel. Inneboende bräckligt (fokus, timing, tangentlayout,
// hårdkodade rutin-GUID:er för DEN HÄR installationen) — därför bakom
// preview→confirm→(auto-save)-grindar i server-lagret.
package monitorui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Flödet i klienten:
//   1. öppna rutinen via mond://-hyperlänk (hårdkodade länkar nedan)
//   2. fokusera det nya fönstret som dök upp
//   3. ordernummer → Tab → ordernummer → Ctrl+L (hämta listan)
//   4. Ctrl+S (spara/registrera) — bara om save=true

const (
	monitorOpenDelayMs = 3000
	monitorStepDelayMs = 500
	monitorSaveDelayMs = 1200
)

// mond://-länkar till Monitor-rutinerna (hårdkodade för den här installationen).
const (
	LinkReportArrival = "mond://001.1/150bc858-55f1-4453-9150-89d9ecabd63c"
	LinkInspection    = "mond://001.1/6118e64b-734e-4878-bf53-ddcde3bc2b41"
)

// Drive öppnar rätt Monitor-rutin och fyller i ordernumret. routine är
// "report_arrival" eller "inspection". save=true skickar även Ctrl+S — anroparen
// (server-lagret) ansvarar för att bara sätta save efter MonitorUIAutoSave-grinden.
func Drive(routine, orderNumber string, save bool) error {
	link := LinkReportArrival
	if routine == "inspection" {
		link = LinkInspection
	}
	return runMonitorRoutine(link, orderNumber, save)
}

// sendKeysEscape escapar SendKeys-metatecken så ett ordernummer skickas ordagrant.
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

// psSingleQuote escapar en sträng för en PowerShell single-quoted literal.
func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// winFocusCSharp enumererar synliga topp-fönster och fokuserar ett givet fönster
// (via user32.dll P/Invoke) — för att flytta förgrundsfokus till det NYA fönster
// Monitor öppnar efter mond://-länken innan vi skriver. Skrivet i PS 5.1-kompatibel
// C#-stil.
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

// BuildReceivingScript bygger PowerShell-skriptet (ren funktion, ingen exec — kan
// enhetstestas på alla OS).
func BuildReceivingScript(link, orderNumber string, save bool) string {
	keys := sendKeysEscape(orderNumber)
	var b strings.Builder
	b.WriteString("Add-Type -AssemblyName System.Windows.Forms;\n")
	b.WriteString("Add-Type @\"\n")
	b.WriteString(winFocusCSharp)
	b.WriteString("\n\"@;\n")
	b.WriteString("$before = [WinUtil]::List();\n")
	fmt.Fprintf(&b, "Start-Process %s;\n", psSingleQuote(link))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorOpenDelayMs)
	b.WriteString("$after = [WinUtil]::List();\n")
	b.WriteString("$new = @($after | Where-Object { $before -notcontains $_ });\n")
	b.WriteString("$target = [IntPtr]::Zero;\n")
	b.WriteString("if ($new.Count -gt 0) { $target = $new[$new.Count - 1] }\n")
	fmt.Fprintf(&b, "if ($target -ne [IntPtr]::Zero) { [WinUtil]::Focus($target); Start-Sleep -Milliseconds %d }\n", monitorStepDelayMs)
	fmt.Fprintf(&b, "[System.Windows.Forms.SendKeys]::SendWait(%s);\n", psSingleQuote(keys+"{TAB}"+keys))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorStepDelayMs)
	b.WriteString("[System.Windows.Forms.SendKeys]::SendWait('^l');\n")
	if save {
		fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d;\n", monitorSaveDelayMs)
		b.WriteString("[System.Windows.Forms.SendKeys]::SendWait('^s');\n")
	}
	return b.String()
}

// runMonitorRoutine kör skriptet via PowerShell på Windows. På andra OS ett
// tydligt fel (mekanismen finns bara där Monitor-klienten kör).
func runMonitorRoutine(link, orderNumber string, save bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("UI-styrning av Monitor stöds bara på Windows (denna app kör på %s)", runtime.GOOS)
	}
	script := BuildReceivingScript(link, orderNumber, save)
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	hideConsole(cmd) // dölj PowerShell-konsolen (annars blinkar den fram + stjäl fokus)
	return cmd.Run()
}
