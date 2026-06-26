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
)

type Config struct {
	InboxDir    string `json:"inbox_dir"`
	ApiKey      string `json:"api_key,omitempty"`
	Theme       string `json:"theme,omitempty"`
	Autostart   bool   `json:"autostart"`
	SickanModel string `json:"sickan_model,omitempty"`
	BNumberMode string `json:"b_number_mode,omitempty"`

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
	UpcomingEnabled    bool   `json:"upcoming_enabled"`
	UpcomingTime       string `json:"upcoming_time,omitempty"`
	UpcomingWindowDays int    `json:"upcoming_window_days,omitempty"`
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
	c.NormalizeUpcoming()
	return c
}

func SaveConfig(c Config) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfigPath(), data, 0644)
}
