// Package store kapslar disk-IO, Config och PDF-metadata för cert-renamer.
package store

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Defaultvärden för "Kommande inleveranser"-schemat.
const (
	DefaultUpcomingTime       = "16:30"
	DefaultUpcomingWindowDays = 14
	DefaultUpcomingBackDays   = 365
)

// DefaultBriefTime är morgonbriefens schemalagda tid.
const DefaultBriefTime = "06:45"

// DefaultMonitorURL är den hårdkodade Monitor-adressen. Monitor G5:s REST/OData-API
// kräver TLS (self-signed på den lokala servern) — därför https. URL:en är medvetet
// inte redigerbar i inställningarna (en operatör kan inte råka posta plaintext-HTTP
// mot TLS-porten, vilket ger "connection forcibly closed"). Env MONITOR_URL kan
// fortfarande överstyra som escape hatch.
const DefaultMonitorURL = "https://192.168.52.232:8001"

type Config struct {
	InboxDir string `json:"inbox_dir"`
	// DeliveryInboxDir är en EGEN mapp för inmejlade följesedel-foton (en
	// Outlook-regel flyttar dem dit). Routing sker per mapp: allt här behandlas
	// som följesedel, aldrig som cert. Tom = följesedel-flödet är av.
	DeliveryInboxDir string `json:"delivery_inbox_dir,omitempty"`
	ApiKey           string `json:"api_key,omitempty"`
	Theme            string `json:"theme,omitempty"`
	Autostart        bool   `json:"autostart"`
	SickanModel      string `json:"sickan_model,omitempty"`
	BNumberMode      string `json:"b_number_mode,omitempty"`

	// Monitor ERP (Fas 3). Klartext-lösen i config.json är känd skuld — löses i
	// auth-planen. Env-varianterna (MONITOR_URL/USER/PASSWORD) har företräde.
	MonitorURL      string `json:"monitor_url,omitempty"`
	MonitorUser     string `json:"monitor_user,omitempty"`
	MonitorPassword string `json:"monitor_password,omitempty"`

	// Monitor UI-automation (Windows): styr skrivbordsklienten direkt eftersom
	// skriv-API:t inte är licensierat. Länkarna till rutinerna är hårdkodade
	// (se internal/server/monitorui.go). AutoSave avgör om Ctrl+S
	// (spara/registrera) får skickas automatiskt.
	MonitorUIAutoSave bool `json:"monitor_ui_auto_save,omitempty"`

	// Kommande inleveranser. UpcomingEnabled är HÅRD live-grind: av som default
	// tills Steg 0 (auth + en riktig query) är grön på jobbdatorn. UpcomingTime
	// är väggklockstid "HH:MM" för dagens schemalagda körning (default 16:30,
	// ogiltig avvisas). UpcomingWindowDays är hur långt framåt vi hämtar (default 14).
	// UpcomingBackDays är hur långt BAKÅT vi tar med (default 365) så överförda/
	// försenade rader som borde ha kommit redan också syns.
	UpcomingEnabled    bool   `json:"upcoming_enabled"`
	UpcomingTime       string `json:"upcoming_time,omitempty"`
	UpcomingWindowDays int    `json:"upcoming_window_days,omitempty"`
	UpcomingBackDays   int    `json:"upcoming_back_days,omitempty"`

	// HiddenSuppliers är leverantörsnamn dolda av operatören i "Kommande
	// inleveranser"-vyn (rent UI-filter, raderna finns kvar i databasen).
	HiddenSuppliers []string `json:"hidden_suppliers,omitempty"`

	// ReportEmail är mottagaren för avvikelsemail ("Maila Daniel"): mailto-utkast
	// om positioner som inte levererades in vid en delleverans.
	ReportEmail string `json:"report_email,omitempty"`

	// Morgonbrief ("Idag"-fliken): BriefEnabled slår på generering (kräver i
	// praktiken UpcomingEnabled för färsk Monitor-data); BriefTime är den
	// schemalagda morgontiden — briefen byggs dessutom alltid vid appöppning
	// på en ny dag.
	BriefEnabled bool   `json:"brief_enabled"`
	BriefTime    string `json:"brief_time,omitempty"`

	// V2 (cmd/cert-renamer-v2): certlager (stabila original) respektive utmapp
	// för sparade cert. Tomma = härleds från InboxDir (<inbox>/v2/store,
	// <inbox>/v2/out). V1 läser aldrig dessa fält.
	StoreDirV2  string `json:"v2_store_dir,omitempty"`
	OutputDirV2 string `json:"v2_output_dir,omitempty"`
}

// NormalizeUpcoming sätter defaults och avvisar ogiltig UpcomingTime. Anropas
// från LoadConfig och vid spara (handleConfig) så att resten av koden kan lita
// på fälten.
func (c *Config) NormalizeUpcoming() {
	if strings.TrimSpace(c.UpcomingTime) == "" {
		c.UpcomingTime = DefaultUpcomingTime
	} else if _, err := time.Parse("15:04", c.UpcomingTime); err != nil {
		log.Printf("⚠️  ogiltig upcoming_time %q — använder default %s", c.UpcomingTime, DefaultUpcomingTime)
		c.UpcomingTime = DefaultUpcomingTime
	}
	if c.UpcomingWindowDays <= 0 {
		c.UpcomingWindowDays = DefaultUpcomingWindowDays
	}
	if c.UpcomingBackDays <= 0 {
		c.UpcomingBackDays = DefaultUpcomingBackDays
	}
	if strings.TrimSpace(c.BriefTime) == "" {
		c.BriefTime = DefaultBriefTime
	} else if _, err := time.Parse("15:04", c.BriefTime); err != nil {
		log.Printf("⚠️  ogiltig brief_time %q — använder default %s", c.BriefTime, DefaultBriefTime)
		c.BriefTime = DefaultBriefTime
	}
}

// NormalizeMonitorURL tvingar en giltig https-URL. Tom → hårdkodad default.
// Saknad scheme → https:// läggs till. http:// → uppgraderas till https:// (Monitor
// kräver TLS; plaintext mot TLS-porten ger "connection forcibly closed"). Avslutande
// snedstreck trimmas. Anropas från LoadConfig och vid spara så resten av koden kan
// lita på fältet.
func (c *Config) NormalizeMonitorURL() {
	u := strings.TrimRight(strings.TrimSpace(c.MonitorURL), "/")
	switch {
	case u == "":
		u = DefaultMonitorURL
	case strings.HasPrefix(u, "http://"):
		log.Printf("⚠️  Monitor-URL var http:// — tvingar https:// (Monitor kräver TLS)")
		u = "https://" + strings.TrimPrefix(u, "http://")
	case !strings.Contains(u, "://"):
		u = "https://" + u
	}
	c.MonitorURL = u
}

func QueueDir(c Config) string         { return filepath.Join(c.InboxDir, "queue") }
func ReviewDir(c Config) string        { return filepath.Join(c.InboxDir, "review") }
func ApprovedDir(c Config) string      { return filepath.Join(c.InboxDir, "approved") }
func ArkiveratDir(c Config) string     { return filepath.Join(c.InboxDir, "arkiverat") }
func DeliveryNotesDir(c Config) string { return filepath.Join(c.InboxDir, "delivery_notes") }

func ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Application Support", "cert-renamer")
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			dir = filepath.Join(appdata, "cert-renamer")
		} else {
			dir = filepath.Join(home, "cert-renamer")
		}
	default:
		dir = filepath.Join(home, ".config", "cert-renamer")
	}
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "config.json")
}

func LoadConfig() Config {
	var c Config
	p := ConfigPath()
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &c)
		log.Printf("📖 config laddad från %s: inbox=%q", p, c.InboxDir)
	} else {
		log.Printf("📖 config saknas (%s): %v", p, err)
	}
	// Monitor: env har företräde, config.json som fallback.
	if v := os.Getenv("MONITOR_URL"); v != "" {
		c.MonitorURL = v
	}
	if v := os.Getenv("MONITOR_USER"); v != "" {
		c.MonitorUser = v
	}
	if v := os.Getenv("MONITOR_PASSWORD"); v != "" {
		c.MonitorPassword = v
	}
	// Kommande inleveranser: env-override (samma mönster som Monitor).
	if v := os.Getenv("UPCOMING_ENABLED"); v != "" {
		c.UpcomingEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("UPCOMING_TIME"); v != "" {
		c.UpcomingTime = v
	}
	if v := os.Getenv("UPCOMING_WINDOW_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.UpcomingWindowDays = n
		}
	}
	if v := os.Getenv("UPCOMING_BACK_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.UpcomingBackDays = n
		}
	}
	c.NormalizeUpcoming()
	c.NormalizeMonitorURL()
	return c
}

func SaveConfig(c Config) error {
	// Env-överstyrda fält får ALDRIG persisteras: env > config.json är den
	// dokumenterade precedensen, och den effektiva configen bär env-värdena.
	// Läs tillbaka filens egna värden för fält som just nu styrs av env.
	var onDisk Config
	if data, err := os.ReadFile(ConfigPath()); err == nil {
		_ = json.Unmarshal(data, &onDisk)
	}
	if os.Getenv("MONITOR_URL") != "" {
		c.MonitorURL = onDisk.MonitorURL
	}
	if os.Getenv("MONITOR_USER") != "" {
		c.MonitorUser = onDisk.MonitorUser
	}
	if os.Getenv("MONITOR_PASSWORD") != "" {
		c.MonitorPassword = onDisk.MonitorPassword
	}
	if os.Getenv("UPCOMING_ENABLED") != "" {
		c.UpcomingEnabled = onDisk.UpcomingEnabled
	}
	if os.Getenv("UPCOMING_TIME") != "" {
		c.UpcomingTime = onDisk.UpcomingTime
	}
	if os.Getenv("UPCOMING_WINDOW_DAYS") != "" {
		c.UpcomingWindowDays = onDisk.UpcomingWindowDays
	}
	if os.Getenv("UPCOMING_BACK_DAYS") != "" {
		c.UpcomingBackDays = onDisk.UpcomingBackDays
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Atomiskt (tmp + rename, samma mönster som SaveCosts): en krasch mitt i
	// en truncate-write skulle annars lämna en korrupt config som LoadConfig
	// tyst sväljer — appen "glömmer" inbox och API-nyckel.
	p := ConfigPath()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
