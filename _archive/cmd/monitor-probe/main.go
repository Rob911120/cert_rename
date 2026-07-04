// Command monitor-probe testar systematiskt hur man kan djuplänka direkt till en
// order i Monitor-rutinen "Rapportera inleverans".
//
// Bakgrund: Monitors interna länkmetod tar (vad vi vet) inte råa textparametrar
// utifrån — men det är inte bevisat uttömmande. Det här verktyget provar därför
// ALLA rimliga sätt att skicka med ordernumret och dokumenterar vad som händer,
// så att frågan kan avgöras empiriskt istället för med gissningar.
//
// Två lägen:
//
//	monitor-probe -capture
//	    Läser en mond://-länk från urklipp (öppna en RIKTIG order i Monitor,
//	    tryck Ctrl+Shift+K, kör sedan detta). Plockar isär länken och
//	    base64-avkodar ev. svans → avslöjar det exakta fältnamnet. HÖGST
//	    träffsäkerhet — gör detta först.
//
//	monitor-probe -order 2580
//	    Brute force: genererar ~150 länkvarianter (alla rimliga parameternamn
//	    × strukturer), öppnar var och en, fångar nya fönstrets titel, tar en
//	    skärmdump och bygger out/report.html som du bläddrar igenom för att se
//	    vilken variant som faktiskt landade på ordern.
//
// Ren Go + PowerShell/.NET (ingen CGO), Windows-only för den faktiska körningen.
// På andra OS gör verktyget en "dry run" och skriver bara ut kandidatlistan.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// Standardlänken till "Rapportera inleverans" (samma som i internal/server/monitorui.go).
const defaultLink = "mond://001.1/150bc858-55f1-4453-9150-89d9ecabd63c"

func main() {
	var (
		capture   = flag.Bool("capture", false, "läs mond://-länk från urklipp, plocka isär och avkoda den (kör efter Ctrl+Shift+K på en riktig order)")
		order     = flag.String("order", "", "ordernumret/B-numret att testa med (krävs i probe-läge)")
		baseLink  = flag.String("link", defaultLink, "baslänk till rutinen")
		outDir    = flag.String("out", "monitor-probe-out", "mapp för skärmdumpar och rapport")
		delayMs   = flag.Int("delay", 2500, "ms att vänta efter att länken öppnats innan vi läser fönstret")
		focusMs   = flag.Int("focusdelay", 300, "ms att vänta efter fönsterfokus")
		limit     = flag.Int("limit", 0, "kör bara de N första kandidaterna (0 = alla)")
		inter     = flag.Bool("interactive", false, "fråga efter varje kandidat om den landade på ordern (j/n)")
		dryRun    = flag.Bool("dry-run", false, "generera bara kandidatlistan, öppna ingenting")

		// Steg-0-dump (läs-läge): logga in mot Monitor och dumpa rå JSON så att de
		// osäkra fälten i planen kan verifieras innan UpcomingEnabled slås på.
		dump     = flag.Bool("dump", false, "läs-läge: logga in och dumpa rå JSON (PurchaseOrderDeliveryRows + PurchaseOrderRows + Parts) för Steg-0-grinden")
		dumpURL  = flag.String("url", "", "Monitor-URL (annars MONITOR_URL / config.json)")
		dumpUser = flag.String("user", "", "Monitor-användare (annars MONITOR_USER / config.json)")
		dumpPass = flag.String("password", "", "Monitor-lösenord (annars MONITOR_PASSWORD / config.json)")
		dumpLang = flag.String("lang", "sv", "språksegment i API-pathen (sv/se/en) — VERIFIERA mot servern")
		dumpOut  = flag.String("dumpout", "monitor-dump-out", "mapp för rå-JSON-dumparna")
		dumpTop  = flag.Int("top", 3, "antal rader per endpoint ($top)")
	)
	flag.Parse()

	if *capture {
		if err := runCapture(); err != nil {
			fmt.Fprintln(os.Stderr, "fel:", err)
			os.Exit(1)
		}
		return
	}

	if *dump {
		if err := runDump(dumpOptions{
			url:    *dumpURL,
			user:   *dumpUser,
			pass:   *dumpPass,
			lang:   *dumpLang,
			outDir: *dumpOut,
			top:    *dumpTop,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "fel:", err)
			os.Exit(1)
		}
		return
	}

	if *order == "" {
		fmt.Fprintln(os.Stderr, "ange -order <ordernummer> (eller -capture). Kör -h för hjälp.")
		os.Exit(2)
	}

	cands := buildCandidates(*baseLink, *order)
	if *limit > 0 && *limit < len(cands) {
		cands = cands[:*limit]
	}

	fmt.Printf("Genererade %d kandidatlänkar för order %q.\n", len(cands), *order)

	if *dryRun || runtime.GOOS != "windows" {
		if runtime.GOOS != "windows" {
			fmt.Printf("(Kör på %s — kan inte styra Monitor. Listar bara kandidaterna.)\n\n", runtime.GOOS)
		}
		for i, c := range cands {
			fmt.Printf("%3d  %-22s %s\n", i+1, c.Label, c.Link)
		}
		return
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "kunde inte skapa out-mapp:", err)
		os.Exit(1)
	}

	est := time.Duration(len(cands)) * (time.Duration(*delayMs+*focusMs+800) * time.Millisecond)
	fmt.Printf("Kör probe mot Monitor. Uppskattad tid: ~%s.\n", est.Round(time.Second))
	fmt.Println("Tips: stäng andra fönster och rör inte musen/tangentbordet under körningen.")
	fmt.Println()

	results := runProbe(cands, *outDir, *delayMs, *focusMs, *inter)

	reportPath := filepath.Join(*outDir, "report.html")
	if err := writeReport(reportPath, *order, *baseLink, results); err != nil {
		fmt.Fprintln(os.Stderr, "kunde inte skriva rapport:", err)
	}
	if err := writeJSON(filepath.Join(*outDir, "results.json"), results); err != nil {
		fmt.Fprintln(os.Stderr, "kunde inte skriva results.json:", err)
	}

	fmt.Printf("\nKlart. Öppna %s och bläddra igenom skärmdumparna.\n", reportPath)
	printSummary(results, *order)
}

// ---- Kandidatgenerering ----

// Candidate är en länkvariant att testa.
type Candidate struct {
	Label string `json:"label"`
	Link  string `json:"link"`
}

// paramNames är alla rimliga namn på "ordernummer/B-nummer"-fältet som Monitors
// länkhanterare skulle kunna lyssna på.
var paramNames = []string{
	"orderno", "ordernumber", "ordernr", "order", "orderid", "ordno",
	"identity", "id",
	"number", "no", "nr", "num",
	"filter", "filtervalue", "filterfromlink",
	"key", "search", "q", "query",
	"purchaseorder", "purchaseorderno", "purchaseordernumber", "purchaseorders",
	"bnumber", "bnr", "bno", "b",
	"value", "val",
}

// buildCandidates korsar parameternamn med olika URL-strukturer och lägger till
// rena strukturvarianter utan namn. Dubbletter tas bort.
func buildCandidates(base, val string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(label, link string) {
		if seen[link] {
			return
		}
		seen[link] = true
		out = append(out, Candidate{Label: label, Link: link})
	}

	for _, name := range paramNames {
		cap := capitalize(name)
		add("query:"+name, fmt.Sprintf("%s?%s=%s", base, name, val))
		add("queryCap:"+cap, fmt.Sprintf("%s?%s=%s", base, cap, val))
		add("frag:"+name, fmt.Sprintf("%s#%s=%s", base, name, val))
		add("filter:"+name+"~", fmt.Sprintf("%s?filter=%s~%s", base, name, val))
		add("filter:"+name+":", fmt.Sprintf("%s?filter=%s:%s", base, name, val))
		add("filter:"+name+"=", fmt.Sprintf("%s?filter=%s=%s", base, name, val))
	}

	// Strukturvarianter utan namngiven parameter.
	add("path-append", fmt.Sprintf("%s/%s", base, val))
	add("path-query", fmt.Sprintf("%s/?%s", base, val))
	add("bare-query", fmt.Sprintf("%s?%s", base, val))
	add("bare-frag", fmt.Sprintf("%s#%s", base, val))
	add("filter-bare", fmt.Sprintf("%s?filter=%s", base, val))
	add("baseline", base) // referens: bara rutinen, inget värde

	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ---- Probe-körning (Windows) ----

// Result samlar utfallet för en kandidat.
type Result struct {
	Candidate
	NewCount int    `json:"new_window_count"`
	Title    string `json:"new_window_title"`
	Shot     string `json:"screenshot"` // filnamn relativt out-mappen
	Err      string `json:"error,omitempty"`
	Verdict  string `json:"verdict,omitempty"` // från -interactive: "hit"/"miss"/"skip"
}

func runProbe(cands []Candidate, outDir string, delayMs, focusMs int, interactive bool) []Result {
	results := make([]Result, 0, len(cands))
	reader := bufio.NewReader(os.Stdin)
	scriptPath := filepath.Join(outDir, "_probe_step.ps1")

	for i, c := range cands {
		shotName := fmt.Sprintf("%03d-%s.png", i+1, slug(c.Label))
		shotPath := filepath.Join(outDir, shotName)
		fmt.Printf("[%3d/%d] %-22s ", i+1, len(cands), c.Label)

		out, err := runStep(scriptPath, c.Link, shotPath, delayMs, focusMs)
		r := Result{Candidate: c, Shot: shotName}
		if err != nil {
			r.Err = err.Error()
			fmt.Printf("FEL: %s\n", err)
		} else {
			r.NewCount = out.NewCount
			r.Title = out.Title
			if out.Err != "" {
				r.Err = out.Err
			}
			fmt.Printf("nya fönster=%d  titel=%q\n", out.NewCount, truncate(out.Title, 60))
		}

		if interactive {
			fmt.Print("        Landade den på ordern? [j]a/[n]ej/[s]kippa: ")
			line, _ := reader.ReadString('\n')
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "j", "y":
				r.Verdict = "hit"
			case "s":
				r.Verdict = "skip"
			default:
				r.Verdict = "miss"
			}
		}
		results = append(results, r)
	}
	_ = os.Remove(scriptPath)
	return results
}

// stepOut är JSON-utdata från PowerShell-steget.
type stepOut struct {
	Title    string `json:"title"`
	NewCount int    `json:"newCount"`
	Err      string `json:"err"`
}

func runStep(scriptPath, link, shotPath string, delayMs, focusMs int) (stepOut, error) {
	script := buildStepScript(link, shotPath, delayMs, focusMs)
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return stepOut{}, err
	}
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-STA", "-File", scriptPath)
	raw, err := cmd.Output()
	if err != nil {
		return stepOut{}, err
	}
	// Ta sista icke-tomma raden (JSON) — Add-Type m.m. kan skriva annat före.
	var last string
	for _, ln := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			last = t
		}
	}
	var o stepOut
	if last != "" {
		_ = json.Unmarshal([]byte(last), &o)
	}
	return o, nil
}

// winUtilCSharp enumererar/fokuserar/minimerar topp-fönster. C# 2-stil så den
// kompilerar i Windows PowerShell 5.1.
const winUtilCSharp = `using System;
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
        uint pid; uint t1 = GetWindowThreadProcessId(fg, out pid);
        uint t2 = GetCurrentThreadId();
        AttachThreadInput(t2, t1, true);
        ShowWindow(h, 9); BringWindowToTop(h); SetForegroundWindow(h);
        AttachThreadInput(t2, t1, false);
    }
    public static void Minimize(IntPtr h) { ShowWindow(h, 6); }
}`

func buildStepScript(link, shotPath string, delayMs, focusMs int) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference='Continue'\n")
	b.WriteString("Add-Type -AssemblyName System.Windows.Forms\n")
	b.WriteString("Add-Type -AssemblyName System.Drawing\n")
	b.WriteString("Add-Type @\"\n")
	b.WriteString(winUtilCSharp)
	b.WriteString("\n\"@\n")
	b.WriteString("$before = [WinUtil]::List()\n")
	b.WriteString("$err=''\n")
	fmt.Fprintf(&b, "try { Start-Process %s } catch { $err = $_.Exception.Message }\n", psSingleQuote(link))
	fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d\n", delayMs)
	b.WriteString("$after = [WinUtil]::List()\n")
	b.WriteString("$new = @($after | Where-Object { $before -notcontains $_ })\n")
	b.WriteString("$title=''\n")
	b.WriteString("if ($new.Count -gt 0) {\n")
	b.WriteString("  $h = $new[$new.Count-1]\n")
	b.WriteString("  [WinUtil]::Focus($h)\n")
	fmt.Fprintf(&b, "  Start-Sleep -Milliseconds %d\n", focusMs)
	b.WriteString("  $title = [WinUtil]::Title($h)\n")
	b.WriteString("}\n")
	// Skärmdump av hela skrivbordet.
	b.WriteString("try {\n")
	b.WriteString("  $bd = [System.Windows.Forms.SystemInformation]::VirtualScreen\n")
	b.WriteString("  $bmp = New-Object System.Drawing.Bitmap $bd.Width, $bd.Height\n")
	b.WriteString("  $g = [System.Drawing.Graphics]::FromImage($bmp)\n")
	b.WriteString("  $g.CopyFromScreen($bd.Location, [System.Drawing.Point]::Empty, $bd.Size)\n")
	fmt.Fprintf(&b, "  $bmp.Save(%s, [System.Drawing.Imaging.ImageFormat]::Png)\n", psSingleQuote(shotPath))
	b.WriteString("  $g.Dispose(); $bmp.Dispose()\n")
	b.WriteString("} catch {}\n")
	// Minimera nya fönster så skärmen är ren inför nästa kandidat.
	b.WriteString("foreach ($h in $new) { [WinUtil]::Minimize($h) }\n")
	b.WriteString("$o = @{ title=$title; newCount=$new.Count; err=$err } | ConvertTo-Json -Compress\n")
	b.WriteString("Write-Output $o\n")
	return b.String()
}

// ---- Capture-läge ----

func runCapture() error {
	link, err := readClipboard()
	if err != nil {
		return err
	}
	link = strings.TrimSpace(link)
	if link == "" {
		return fmt.Errorf("urklipp tomt — öppna en order i Monitor, tryck Ctrl+Shift+K, kör sedan igen")
	}
	fmt.Println("Länk från urklipp:")
	fmt.Println(" ", link)
	if !strings.Contains(link, "://") {
		fmt.Println("\n(Ser inte ut som en URI — är det rätt rad som kopierades?)")
	}
	analyzeLink(link)
	return nil
}

func analyzeLink(link string) {
	fmt.Println("\nUppdelning:")
	scheme, rest := split2(link, "://")
	fmt.Printf("  scheme : %s\n", scheme)

	pathPart, query := rest, ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		pathPart, query = rest[:i], rest[i+1:]
	}
	frag := ""
	if i := strings.IndexByte(pathPart, '#'); i >= 0 {
		pathPart, frag = pathPart[:i], pathPart[i+1:]
	}
	fmt.Printf("  path   : %s\n", pathPart)
	if query != "" {
		fmt.Printf("  query  : %s\n", query)
		for _, kv := range strings.Split(query, "&") {
			fmt.Printf("           - %s\n", kv)
		}
	}
	if frag != "" {
		fmt.Printf("  frag   : %s\n", frag)
	}

	// Försök base64-avkoda sista path-segmentet (där en ev. serialiserad payload bor).
	seg := pathPart
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	tryDecode("path-svans", seg)
	if query != "" {
		tryDecode("query", query)
	}
	if frag != "" {
		tryDecode("frag", frag)
	}
	fmt.Println("\nKör med ett annat ordernummer och jämför de två länkarna — det som skiljer ÄR ditt ordernummer-fält.")
}

func tryDecode(what, s string) {
	if len(s) < 8 {
		return
	}
	cand := strings.NewReplacer("-", "+", "_", "/").Replace(s)
	for len(cand)%4 != 0 {
		cand += "="
	}
	data, err := base64.StdEncoding.DecodeString(cand)
	if err != nil {
		return
	}
	printable := 0
	for _, b := range data {
		if b >= 0x20 && b < 0x7f {
			printable++
		}
	}
	if len(data) == 0 {
		return
	}
	if float64(printable)/float64(len(data)) > 0.6 {
		fmt.Printf("\n  base64(%s) → klartext: %q\n", what, string(data))
	} else {
		fmt.Printf("\n  base64(%s) → binärt (serialiserat objekt?), %d byte\n", what, len(data))
	}
}

func readClipboard() (string, error) {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw").Output()
		return string(out), err
	case "darwin":
		out, err := exec.Command("pbpaste").Output()
		return string(out), err
	default:
		out, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
		if err != nil {
			return "", fmt.Errorf("kunde inte läsa urklipp på %s (kräver xclip): %w", runtime.GOOS, err)
		}
		return string(out), nil
	}
}

// ---- Rapport & utskrift ----

func writeReport(path, order, base string, results []Result) error {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8>\n")
	b.WriteString("<title>monitor-probe</title>\n")
	b.WriteString("<style>body{font-family:system-ui,sans-serif;margin:2rem;background:#111;color:#eee}")
	b.WriteString("h1{font-size:1.2rem}.card{border:1px solid #333;border-radius:8px;margin:1rem 0;padding:1rem;background:#1a1a1a}")
	b.WriteString(".hit{border-color:#3a3}.lbl{font-weight:600}.link{color:#8cf;word-break:break-all;font-family:monospace;font-size:.85rem}")
	b.WriteString(".meta{color:#aaa;font-size:.85rem;margin:.3rem 0}img{max-width:100%;border:1px solid #333;border-radius:4px;margin-top:.5rem}</style>\n")
	fmt.Fprintf(&b, "<h1>monitor-probe — order %s</h1>\n", htmlEscape(order))
	fmt.Fprintf(&b, "<p class=meta>Baslänk: <span class=link>%s</span> · %d kandidater</p>\n", htmlEscape(base), len(results))
	b.WriteString("<p class=meta>Leta efter kortet där skärmdumpen visar ordern inladdad — den länkens variant fungerar.</p>\n")
	for _, r := range results {
		cls := "card"
		if r.Verdict == "hit" {
			cls = "card hit"
		}
		fmt.Fprintf(&b, "<div class=\"%s\">\n", cls)
		fmt.Fprintf(&b, "<div class=lbl>%s%s</div>\n", htmlEscape(r.Label), verdictBadge(r.Verdict))
		fmt.Fprintf(&b, "<div class=link>%s</div>\n", htmlEscape(r.Link))
		fmt.Fprintf(&b, "<div class=meta>nya fönster: %d · titel: %s%s</div>\n",
			r.NewCount, htmlEscape(orDash(r.Title)), errSuffix(r.Err))
		if r.Shot != "" {
			fmt.Fprintf(&b, "<img loading=lazy src=\"%s\" alt=\"\">\n", htmlEscape(r.Shot))
		}
		b.WriteString("</div>\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeJSON(path string, results []Result) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func printSummary(results []Result, order string) {
	var hits, withNew, errs int
	for _, r := range results {
		if r.Verdict == "hit" {
			hits++
		}
		if r.NewCount > 0 {
			withNew++
		}
		if r.Err != "" {
			errs++
		}
	}
	fmt.Printf("\nSammanfattning: %d kandidater, %d öppnade nytt fönster, %d fel", len(results), withNew, errs)
	if hits > 0 {
		fmt.Printf(", %d markerade som TRÄFF", hits)
	}
	fmt.Println(".")
	// Heuristik: titlar som innehåller ordernumret är intressanta.
	var interesting []Result
	for _, r := range results {
		if r.Title != "" && strings.Contains(r.Title, order) {
			interesting = append(interesting, r)
		}
	}
	if len(interesting) > 0 {
		fmt.Println("Kandidater vars fönstertitel innehåller ordernumret (titta extra här):")
		for _, r := range interesting {
			fmt.Printf("  - %-22s %s\n", r.Label, r.Link)
		}
	}
}

// ---- Småhjälpare ----

func psSingleQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func split2(s, sep string) (string, string) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):]
	}
	return s, ""
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func errSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " · fel: " + s
}

func verdictBadge(v string) string {
	switch v {
	case "hit":
		return " ✅"
	case "miss":
		return " ❌"
	case "skip":
		return " ⏭"
	}
	return ""
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}
