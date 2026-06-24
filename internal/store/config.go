// Package store kapslar disk-IO, Config och PDF-metadata för cert-renamer.
package store

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
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

	// Monitor UI-automation (Windows): styr skrivbordsklienten direkt när
	// skriv-API:t inte är licensierat. Länkarna är monitor://-hyperlänkar till
	// rutinerna; WindowTitle används för AppActivate. AutoSave avgör om Ctrl+S
	// (spara/registrera) får skickas automatiskt.
	MonitorLinkReportArrival string `json:"monitor_link_report_arrival,omitempty"`
	MonitorLinkInspection    string `json:"monitor_link_inspection,omitempty"`
	MonitorWindowTitle       string `json:"monitor_window_title,omitempty"`
	MonitorUIAutoSave        bool   `json:"monitor_ui_auto_save,omitempty"`
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
	return c
}

func SaveConfig(c Config) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfigPath(), data, 0644)
}
