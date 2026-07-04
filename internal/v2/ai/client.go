package ai

import (
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// logAICall är en gemensam helper som tidsmäter ett anrop, hämtar usage,
// loggar i ett enhetligt format med ett verdict-textstycke, och rapporterar
// token-användning till Logger för kostnadsspårning.
func logAICall[T any](
	log Logger,
	label string,
	fn func() (*T, anthropic.Usage, error),
	verdict func(*T) string,
) (*T, error) {
	start := time.Now()
	result, usage, err := fn()
	dt := time.Since(start).Round(time.Millisecond)
	model := modelFromLabel(label)
	log.RecordUsage(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	if err != nil {
		log.Logf("   ❌ %s: %s, in=%d out=%d, fel: %v", label, dt, usage.InputTokens, usage.OutputTokens, err)
		return nil, err
	}
	v := ""
	if verdict != nil && result != nil {
		v = verdict(result)
		runes := []rune(v)
		if len(runes) > 80 {
			v = string(runes[:80]) + "…"
		}
	}
	log.Logf("   ⚙️  %s: %s, in=%d out=%d — %s", label, dt, usage.InputTokens, usage.OutputTokens, v)
	return result, nil
}

// modelFromLabel plockar första ordet ur ett anropslabel ("sonnet Extract(...)" → "sonnet").
// Returnerar tom sträng om labeln inte börjar med ett känt modell-prefix —
// store.Costs.Add ignorerar i sin tur tomt id, så cost-tracking blir no-op.
func modelFromLabel(label string) string {
	first, _, _ := strings.Cut(label, " ")
	switch first {
	case "sonnet", "haiku":
		return first
	}
	return ""
}
