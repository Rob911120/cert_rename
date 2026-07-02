package ai

const ModelClassify = "claude-haiku-4-5-20251001"
const ModelExtract = "claude-sonnet-5"

// Sickan-modeller. ChatDefault är start-valet; användaren kan byta i UI:t.
const (
	ChatHaiku   = "claude-haiku-4-5-20251001"
	ChatSonnet  = "claude-sonnet-5"
	ChatOpus    = "claude-opus-4-8"
	ChatDefault = ChatSonnet
)

// ChatCostKey mappar modell-ID till "haiku"/"sonnet"/"opus" som store.Costs
// använder. Okända ID:n returnerar tom sträng (Add ignorerar då tyst).
// Utfasade ID:n behålls som fall: config.json kan bära ett gammalt sparat
// modellval, och serverns modell-allowlist bygger på den här mappningen.
func ChatCostKey(model string) string {
	switch model {
	case ChatHaiku:
		return "haiku"
	case ChatSonnet, "claude-sonnet-4-6":
		return "sonnet"
	case ChatOpus, "claude-opus-4-7":
		return "opus"
	}
	return ""
}
