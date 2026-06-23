// Package sickan implementerar chatboten Sickan: en streaming-chat-loop med
// tool-use mot lokala disk-operationer (kö-ordning, granskning, omdöpning,
// godkännande). Lever som en sidoflöde mellan server-paketet (HTTP/SSE) och
// store/cert/ai. Importerar inte server.
package sickan

import (
	"encoding/json"
	"os"
	"path/filepath"

	"cert-renamer/internal/store"
)

const orderFilename = ".sickan_order.json"

// orderPath returnerar full sökväg till ordningsfilen i queue-mappen.
func orderPath(cfg store.Config) string {
	return filepath.Join(store.QueueDir(cfg), orderFilename)
}

// LoadOrder läser ordningslistan om den finns; returnerar tom slice annars.
func LoadOrder(cfg store.Config) []string {
	data, err := os.ReadFile(orderPath(cfg))
	if err != nil {
		return nil
	}
	var out []string
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// SaveOrder persisterar listan. Listan kan innehålla filnamn som inte längre
// finns i kön — Apply ignorerar saknade poster.
func SaveOrder(cfg store.Config, filenames []string) error {
	if err := os.MkdirAll(store.QueueDir(cfg), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(filenames, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(orderPath(cfg), data, 0644)
}

// Apply sorterar items enligt LoadOrder(cfg). Filnamn som finns i ordningen
// hamnar först i den ordningen; filnamn som saknas hamnar sist i ursprunglig
// ordning.
func Apply(cfg store.Config, items []store.QueueItem) []store.QueueItem {
	order := LoadOrder(cfg)
	if len(order) == 0 {
		return items
	}
	rank := make(map[string]int, len(order))
	for i, name := range order {
		rank[name] = i
	}
	known := make([]store.QueueItem, 0, len(items))
	rest := make([]store.QueueItem, 0, len(items))
	for _, it := range items {
		if _, ok := rank[it.Filename]; ok {
			known = append(known, it)
		} else {
			rest = append(rest, it)
		}
	}
	// stable sort known by rank
	for i := 1; i < len(known); i++ {
		for j := i; j > 0 && rank[known[j].Filename] < rank[known[j-1].Filename]; j-- {
			known[j], known[j-1] = known[j-1], known[j]
		}
	}
	return append(known, rest...)
}

// RenameInOrder ersätter oldName med newName i ordningsfilen om oldName finns.
// No-op om filen saknas eller oldName inte är listad.
func RenameInOrder(cfg store.Config, oldName, newName string) {
	order := LoadOrder(cfg)
	if len(order) == 0 || oldName == newName {
		return
	}
	changed := false
	for i, n := range order {
		if n == oldName {
			order[i] = newName
			changed = true
		}
	}
	if changed {
		_ = SaveOrder(cfg, order)
	}
}

// RemoveFromOrder tar bort filename ur ordningsfilen om det finns där.
func RemoveFromOrder(cfg store.Config, filename string) {
	order := LoadOrder(cfg)
	if len(order) == 0 {
		return
	}
	out := order[:0]
	changed := false
	for _, n := range order {
		if n == filename {
			changed = true
			continue
		}
		out = append(out, n)
	}
	if changed {
		_ = SaveOrder(cfg, out)
	}
}
