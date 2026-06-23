package ai

// Logger låter ai-paketet logga och rapportera token-användning utan att
// importera server-paketet. Server implementerar detta.
type Logger interface {
	Logf(format string, args ...any)
	// RecordUsage rapporterar token-användning från ett Claude-anrop.
	// model är "sonnet" eller "haiku" (härlett från call-label-prefixet).
	RecordUsage(model string, inputTokens, outputTokens, cacheCreation, cacheRead int64)
}
