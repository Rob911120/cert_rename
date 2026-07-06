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
