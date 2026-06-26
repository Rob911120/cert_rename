package main

// Steg-0-dumpläget: ett rent LÄSANDE läge som loggar in mot Monitor och dumpar
// rå JSON från de endpoints "Kommande inleveranser"-planen vilar på. Syftet är
// att verifiera de osäkra (// VERIFIERA) antagandena — fältnamn, semantik och
// auth — på den riktiga jobbdatorn INNAN UpcomingEnabled slås på. Inget skrivs
// till Monitor; bara GET.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// dumpOptions samlar parametrarna för Steg-0-dumpen.
type dumpOptions struct {
	url, user, pass string
	lang, outDir    string
	top             int
}

// dumpTarget är en endpoint att dumpa rå JSON från, med en kort not om vad Rob
// ska titta extra på i svaret.
type dumpTarget struct {
	name  string
	path  string
	query *monitor.Query
	note  string
}

// runDump loggar in mot Monitor (verifierar läs-auth) och dumpar rå JSON från de
// endpoints planen behöver verifiera. Allt skrivs till outDir som filer Rob kan
// skicka tillbaka. Detta är Steg 0:s hårda live-grind — körs på jobbdatorn mot
// den riktiga Monitor-servern (192.168.52.232:8001).
func runDump(opt dumpOptions) error {
	cfg := store.LoadConfig() // env (MONITOR_*) + config.json, samma precedens som appen
	url := firstNonEmpty(opt.url, cfg.MonitorURL)
	user := firstNonEmpty(opt.user, cfg.MonitorUser)
	pass := firstNonEmpty(opt.pass, cfg.MonitorPassword)
	if url == "" || user == "" || pass == "" {
		return fmt.Errorf("saknar Monitor-uppgifter: ange -url/-user/-password, eller MONITOR_URL/USER/PASSWORD, eller fyll i dem i appens ⚙️ Inställningar")
	}
	if err := os.MkdirAll(opt.outDir, 0o755); err != nil {
		return fmt.Errorf("kunde inte skapa %s: %w", opt.outDir, err)
	}

	lang := opt.lang
	if lang == "" {
		lang = "sv"
	}
	mc := monitor.New(url)
	mc.SetLanguage(lang)

	fmt.Printf("Loggar in mot %s (lang=%s, user=%s) …\n", url, lang, user)
	// VARNING: Login skickar ForceRelogin:true och kan sparka ut operatörens
	// interaktiva skrivbordssession. Kör därför INNAN Monitor-klienten öppnas,
	// eller med ett separat läs-konto (se planens driftsrisk-avsnitt).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := mc.Login(ctx, user, pass); err != nil {
		return fmt.Errorf("login misslyckades (kontrollera url/lang/segment/creds): %w", err)
	}
	switch {
	case mc.SessionID() != "":
		fmt.Printf("✅ Inloggad — SessionId i svaret (header-auth). lang=%s, segment=001.1\n", lang)
	case mc.LoggedIn():
		fmt.Printf("✅ Inloggad — ingen SessionId i svaret, session via cookie. lang=%s, segment=001.1\n", lang)
	default:
		fmt.Println("⚠️  Login svarade utan SessionId och utan cookie — auth-mekanismen är oklar (VERIFIERA).")
	}

	top := opt.top
	if top <= 0 {
		top = 3
	}
	targets := []dumpTarget{
		{
			name:  "delivery-rows",
			path:  "/api/v1/Purchase/PurchaseOrderDeliveryRows",
			query: monitor.NewQuery().Top(top).Expand("PurchaseOrderRow($expand=Part)").OrderBy("DeliveryDate asc"),
			note:  "PRIMÄR KÄLLA. VERIFIERA: DeliveryDate ifyllt? ArrivedQuantity-semantik (=0 = ej anländ?)? PurchaseOrderRow+Part inline via $expand? RowStatus-enum?",
		},
		{
			name:  "purchase-order-rows-fallback",
			path:  "/api/v1/Purchase/PurchaseOrderRows",
			query: monitor.NewQuery().Top(top).Expand("Part"),
			note:  "FALLBACK om delivery-rows inte används i Pellys Monitor. VERIFIERA: RestQuantity gt 0 som 'kommande'? Kan Part expanderas?",
		},
		{
			name:  "parts",
			path:  "/api/v1/Inventory/Parts",
			query: monitor.NewQuery().Top(top),
			note:  "VERIFIERA: bär ExtraDescription stålsort + cert-krav? Finns ReceivingInspectionType / TraceabilityMode / CurrentAlloyId?",
		},
	}

	var failures int
	var summary strings.Builder
	fmt.Fprintf(&summary, "monitor-probe dump — %s (lang=%s, segment=001.1)\n", url, lang)
	fmt.Fprintf(&summary, "auth: SessionId=%q loggedIn=%v\n\n", mc.SessionID(), mc.LoggedIn())

	for _, tgt := range targets {
		fmt.Printf("\n── %s ──\n%s\n", tgt.name, tgt.note)
		raw, err := mc.GetRaw(ctx, tgt.path, tgt.query)
		outFile := filepath.Join(opt.outDir, "dump-"+tgt.name+".json")
		if err != nil {
			failures++
			fmt.Printf("❌ %s: %v\n", tgt.path, err)
			fmt.Fprintf(&summary, "[FEL] %-28s %s\n        %v\n", tgt.name, tgt.path, err)
			if len(raw) > 0 { // Monitor returnerar ofta en JSON-felbody — spara den.
				_ = os.WriteFile(outFile, raw, 0o644)
			}
			continue
		}
		if werr := os.WriteFile(outFile, raw, 0o644); werr != nil {
			return fmt.Errorf("kunde inte skriva %s: %w", outFile, werr)
		}
		n := countValue(raw)
		fmt.Printf("✅ %s → %s (%d rader, %d byte)\n", tgt.path, outFile, n, len(raw))
		fmt.Println(previewJSON(raw, 1600))
		fmt.Fprintf(&summary, "[OK]  %-28s %s  (%d rader, %d byte) → %s\n", tgt.name, tgt.path, n, len(raw), filepath.Base(outFile))
	}

	summaryPath := filepath.Join(opt.outDir, "dump-summary.txt")
	if err := os.WriteFile(summaryPath, []byte(summary.String()), 0o644); err != nil {
		return fmt.Errorf("kunde inte skriva %s: %w", summaryPath, err)
	}
	fmt.Printf("\nKlart. Dumparna ligger i %s/ — skicka hela mappen tillbaka.\n", opt.outDir)
	if failures > 0 {
		fmt.Printf("⚠️  %d av %d endpoints gav fel — se felmeddelandena ovan (path/segment/lang kan behöva justeras).\n", failures, len(targets))
	}
	return nil
}

// firstNonEmpty returnerar första icke-tomma (trimmade) strängen.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// countValue räknar antalet element i ett OData-{"value":[...]}-svar (eller en
// bar array). Returnerar -1 om strukturen inte känns igen.
func countValue(raw []byte) int {
	var wrap struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Value != nil {
		return len(wrap.Value)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		return len(arr)
	}
	return -1
}

// previewJSON indenterar och trunkerar JSON för konsol-förhandsvisning.
func previewJSON(raw []byte, max int) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return truncate(string(raw), max) // inte JSON — visa råtext trunkerad
	}
	s := buf.String()
	if len(s) > max {
		return s[:max] + "\n… (trunkerat, se filen för hela svaret)"
	}
	return s
}
