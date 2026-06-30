package ai

const ModelClassify = "claude-haiku-4-5-20251001"
const ModelExtract = "claude-sonnet-5"

// Sickan-modeller. ChatDefault är start-valet; användaren kan byta i UI:t.
const (
	ChatHaiku   = "claude-haiku-4-5-20251001"
	ChatSonnet  = "claude-sonnet-4-6"
	ChatOpus    = "claude-opus-4-7"
	ChatDefault = ChatSonnet
)

// ModelChat finns kvar som bakåtkompatibel fallback för kod som ännu inte
// tar modell som parameter. Pekar på default.
const ModelChat = ChatDefault

// ChatCostKey mappar modell-ID till "haiku"/"sonnet"/"opus" som store.Costs
// använder. Okända ID:n returnerar tom sträng (Add ignorerar då tyst).
func ChatCostKey(model string) string {
	switch model {
	case ChatHaiku:
		return "haiku"
	case ChatSonnet:
		return "sonnet"
	case ChatOpus:
		return "opus"
	}
	return ""
}
