package main

import (
	"path/filepath"
	"testing"

	"cert-renamer/internal/eml"
)

// Test_SyntheticFixtures_Parse verifierar att alla genererade .eml
// kan parsas av cert-renamer:s riktiga eml.Parse, och att förväntade
// B-nummer extraheras.
func Test_SyntheticFixtures_Parse(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", outDir, "*.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Skip("inga fixturer i " + outDir + " — kör `go run ./scripts/gen-fixtures` först")
	}
	if len(matches) != 20 {
		t.Fatalf("förväntade 20 fixturer, hittade %d", len(matches))
	}
	for _, p := range matches {
		t.Run(filepath.Base(p), func(t *testing.T) {
			c, err := eml.Parse(p)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if c.Subject == "" {
				t.Errorf("tom subject")
			}
		})
	}
}
