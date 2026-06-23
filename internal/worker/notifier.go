package worker

import (
	"cert-renamer/internal/ai"
	"cert-renamer/internal/store"
)

// Notifier är hela kontaktytan worker har mot omvärlden för logging,
// stats och UI-broadcasting. Server (server-paketet) implementerar denna;
// tester använder en stub. Embed:ar ai.Logger så att Notifier alltid kan
// passas direkt till ai-anrop.
type Notifier interface {
	ai.Logger
	IncrementOK()
	BroadcastStats()
	BroadcastQueue()
	BroadcastReview()
	Repo() *store.Repository
}
