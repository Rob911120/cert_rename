package server

import (
	"os"
	"path/filepath"
	"strings"

	"cert-renamer/internal/store"
)

// listQueue returnerar kölistan (DB + disk-komplettering) — se store.ListQueueItems.
func (s *Server) listQueue() []store.QueueItem {
	return store.ListQueueItems(s.snapshotCfg(), s.repo)
}

// listReview returnerar review-listan från filsystemet — se store.ListReviewItems.
// Review-items sparas inte i DB än — de behåller filbaserad lagring.
func (s *Server) listReview() []store.ReviewItem {
	return store.ListReviewItems(s.snapshotCfg())
}

// scanOverview loggar översikt över alla cert-renamer-mappar + DB-statistik.
func (s *Server) scanOverview() {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		s.Logf("📂 Ingen inbox vald")
		return
	}
	dirs := []struct{ name, path string }{
		{"inbox (rot)", c.InboxDir},
		{"queue", store.QueueDir(c)},
		{"review", store.ReviewDir(c)},
		{"approved", store.ApprovedDir(c)},
		{"arkiverat", store.ArkiveratDir(c)},
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			s.Logf("📂 %-12s %s — FEL: %v", d.name, d.path, err)
			continue
		}
		var pdfs, emls, jsons, other, sub int
		for _, e := range entries {
			if e.IsDir() {
				sub++
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			switch ext {
			case ".pdf":
				pdfs++
			case ".eml":
				emls++
			case ".json":
				jsons++
			default:
				other++
			}
		}
		s.Logf("📂 %-12s %s — pdf=%d eml=%d json=%d annat=%d undermappar=%d",
			d.name, d.path, pdfs, emls, jsons, other, sub)
	}

	// DB-statistik
	if s.repo != nil {
		queue, approved, review, archived, err := s.repo.CountCertificates()
		if err == nil {
			s.Logf("🗄️  DB: queue=%d approved=%d review=%d archived=%d", queue, approved, review, archived)
		}
	}
}
