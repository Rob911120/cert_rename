package store

import "testing"

// TestNormalizeMonitorURL låser fast https-tvånget: tom → hårdkodad default,
// saknad scheme och http:// → https://, avslutande snedstreck trimmas. Skyddar
// mot att plaintext-HTTP mot Monitors TLS-port ("connection forcibly closed")
// kan smyga in via en sparad config.
func TestNormalizeMonitorURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", DefaultMonitorURL},
		{"   ", DefaultMonitorURL},
		{"192.168.52.232:8001", "https://192.168.52.232:8001"},
		{"http://192.168.52.232:8001", "https://192.168.52.232:8001"},
		{"http://192.168.52.232:8001/", "https://192.168.52.232:8001"},
		{"https://192.168.52.232:8001/", "https://192.168.52.232:8001"},
		{"https://192.168.52.232:8001", "https://192.168.52.232:8001"},
		{"  https://host:8001  ", "https://host:8001"},
	}
	for _, c := range cases {
		cfg := Config{MonitorURL: c.in}
		cfg.NormalizeMonitorURL()
		if cfg.MonitorURL != c.want {
			t.Errorf("NormalizeMonitorURL(%q) = %q, vill ha %q", c.in, cfg.MonitorURL, c.want)
		}
	}
}

// SaveConfig får inte persistera env-överstyrda värden (env > config.json är
// dokumenterad precedens) och ska skriva atomiskt utan att tappa övriga fält.
func TestSaveConfigExcludesEnvOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", "") // windows: falla tillbaka på HOME-varianten

	if err := SaveConfig(Config{InboxDir: "/inbox", MonitorPassword: "filhemlis"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_PASSWORD", "envhemlis")
	cfg := LoadConfig()
	if cfg.MonitorPassword != "envhemlis" {
		t.Fatalf("env ska ha företräde vid load: %q", cfg.MonitorPassword)
	}

	// Spara den EFFEKTIVA configen (som handleConfigPost gör) — env-värdet
	// får inte läcka in i filen, övriga ändringar ska med.
	cfg.Theme = "dark"
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_PASSWORD", "")
	reloaded := LoadConfig()
	if reloaded.MonitorPassword != "filhemlis" {
		t.Errorf("env-värdet persisterades till config.json: %q", reloaded.MonitorPassword)
	}
	if reloaded.Theme != "dark" || reloaded.InboxDir != "/inbox" {
		t.Errorf("övriga fält tappades vid spar: %+v", reloaded)
	}
}
