package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cert-renamer/internal/store"
)

// listQueue läser från databasen och returnerar QueueItem för varje certifikat
// med status "queue". Om en PDF finns på disk men inte i DB läggs den till.
func (s *Server) listQueue() []store.QueueItem {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()

	out := []store.QueueItem{}
	seen := map[string]bool{}

	// 1. Hämta från DB
	if s.repo != nil {
		certs, err := s.repo.ListCertificates("queue")
		if err == nil {
			for _, cert := range certs {
				item := store.QueueItem{
					Filename:    cert.Filename,
					Charge:      cert.Charge,
					Material:    cert.Material,
					ProductForm: cert.ProductForm,
					Dimensions:  cert.Dimensions,
					Confidence:  cert.Confidence,
				}
				if cert.BNumbers != "" {
					_ = json.Unmarshal([]byte(cert.BNumbers), &item.BNumbers)
				}
				if cert.Issues != "" {
					_ = json.Unmarshal([]byte(cert.Issues), &item.Issues)
				}
				out = append(out, item)
				seen[cert.Filename] = true
			}
		}
	}

	// 2. Skanna filsystem och lägg till saknade
	if c.InboxDir != "" {
		entries, _ := os.ReadDir(store.QueueDir(c))
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
				continue
			}
			if seen[e.Name()] {
				continue
			}
			item := store.QueueItem{Filename: e.Name()}
			pdfPath := filepath.Join(store.QueueDir(c), e.Name())
			if m, ok := store.ReadMetadata(pdfPath); ok {
				item.Charge = m.Charge
				item.Material = m.Material
				item.ProductForm = m.ProductForm
				item.Dimensions = m.Dimensions
				item.Confidence = m.Confidence
				item.BNumbers = m.BNumbers
				item.Issues = m.Issues
			}
			out = append(out, item)
		}
	}

	return out
}

// listReview läser från filsystemet (review-mappen) och returnerar ReviewItem.
// Review-items sparas inte i DB än — de behåller filbaserad lagring.
func (s *Server) listReview() []store.ReviewItem {
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	out := []store.ReviewItem{}
	if c.InboxDir == "" {
		return out
	}
	entries, err := os.ReadDir(store.ReviewDir(c))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(store.ReviewDir(c), e.Name())
		item := store.ReviewItem{Base: e.Name(), Files: []string{}}
		if data, err := os.ReadFile(filepath.Join(dir, "_reason.txt")); err == nil {
			item.Reason = strings.TrimSpace(string(data))
		}
		if files, err := os.ReadDir(dir); err == nil {
			for _, f := range files {
				if f.IsDir() || f.Name() == "_reason.txt" {
					continue
				}
				item.Files = append(item.Files, f.Name())
			}
		}
		out = append(out, item)
	}
	return out
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
