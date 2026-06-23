package store

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// TokenCounts ackumulerar token-användning per modell sedan installationen.
type TokenCounts struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
}

// Costs är persistenta token-räknare per modell. Belopp beräknas on-read
// via Pricing — själva räknaren håller bara råa tokens.
type Costs struct {
	Sonnet    TokenCounts `json:"sonnet"`
	Haiku     TokenCounts `json:"haiku"`
	Opus      TokenCounts `json:"opus"`
	UpdatedAt string      `json:"updated_at,omitempty"`
}

// Pricing är USD per miljon tokens per kategori.
type Pricing struct {
	Input         float64
	Output        float64
	CacheCreation float64
	CacheRead     float64
}

var (
	SonnetPricing = Pricing{Input: 3, Output: 15, CacheCreation: 3.75, CacheRead: 0.30}
	HaikuPricing  = Pricing{Input: 1, Output: 5, CacheCreation: 1.25, CacheRead: 0.10}
	OpusPricing   = Pricing{Input: 15, Output: 75, CacheCreation: 18.75, CacheRead: 1.50}
)

// Usd returnerar totalt belopp i USD för givet token-räkne-set.
func (p Pricing) Usd(tc TokenCounts) float64 {
	return (float64(tc.Input)*p.Input +
		float64(tc.Output)*p.Output +
		float64(tc.CacheCreation)*p.CacheCreation +
		float64(tc.CacheRead)*p.CacheRead) / 1_000_000
}

// Add ökar token-räknarna för givet modell-id ("sonnet" eller "haiku").
// Andra modell-id:n ignoreras tyst — vi vill inte krascha appen om
// modellen byts en framtida release.
func (c *Costs) Add(model string, in, out, cacheCreate, cacheRead int64) {
	var tc *TokenCounts
	switch model {
	case "sonnet":
		tc = &c.Sonnet
	case "haiku":
		tc = &c.Haiku
	case "opus":
		tc = &c.Opus
	default:
		return
	}
	tc.Input += in
	tc.Output += out
	tc.CacheCreation += cacheCreate
	tc.CacheRead += cacheRead
	c.UpdatedAt = time.Now().Format(time.RFC3339)
}

func costsPath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), "costs.json")
}

// LoadCosts läser costs.json om den finns. Saknas filen returneras tom Costs.
func LoadCosts() Costs {
	var c Costs
	p := costsPath()
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &c)
	} else if !os.IsNotExist(err) {
		log.Printf("⚠️  costs.json kunde inte läsas (%s): %v", p, err)
	}
	return c
}

// SaveCosts skriver costs.json atomiskt (skriv tmp + rename).
func SaveCosts(c Costs) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := costsPath()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
