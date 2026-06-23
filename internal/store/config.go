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
}

func QueueDir(c Config) string     { return filepath.Join(c.InboxDir, "queue") }
func ReviewDir(c Config) string    { return filepath.Join(c.InboxDir, "review") }
func ApprovedDir(c Config) string  { return filepath.Join(c.InboxDir, "approved") }
func ArkiveratDir(c Config) string { return filepath.Join(c.InboxDir, "arkiverat") }

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
	return c
}

func SaveConfig(c Config) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfigPath(), data, 0644)
}
