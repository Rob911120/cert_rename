package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ListQueueItems bygger kölistan: certifikat med status "queue" ur databasen
// (om repo != nil) kompletterade med PDF:er som finns på disk men saknas i DB.
// Delas av HTTP-API:t och Sickan-verktygen så kön alltid listas likadant.
func ListQueueItems(cfg Config, repo *Repository) []QueueItem {
	out := []QueueItem{}
	seen := map[string]bool{}

	if repo != nil {
		certs, err := repo.ListCertificates("queue")
		if err == nil {
			for _, c := range certs {
				item := QueueItem{
					Filename:    c.Filename,
					Charge:      c.Charge,
					Material:    c.Material,
					ProductForm: c.ProductForm,
					Dimensions:  c.Dimensions,
					Confidence:  c.Confidence,
				}
				if c.BNumbers != "" {
					_ = json.Unmarshal([]byte(c.BNumbers), &item.BNumbers)
				}
				if c.Issues != "" {
					_ = json.Unmarshal([]byte(c.Issues), &item.Issues)
				}
				out = append(out, item)
				seen[c.Filename] = true
			}
		}
	}

	if cfg.InboxDir != "" {
		entries, _ := os.ReadDir(QueueDir(cfg))
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
				continue
			}
			if seen[e.Name()] {
				continue
			}
			item := QueueItem{Filename: e.Name()}
			if m, ok := ReadMetadata(filepath.Join(QueueDir(cfg), e.Name())); ok {
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

// ListReviewItems skannar review-mappen: en post per undermapp, med
// _reason.txt som anledning och övriga filer listade.
func ListReviewItems(cfg Config) []ReviewItem {
	out := []ReviewItem{}
	if cfg.InboxDir == "" {
		return out
	}
	entries, err := os.ReadDir(ReviewDir(cfg))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(ReviewDir(cfg), e.Name())
		item := ReviewItem{Base: e.Name(), Files: []string{}}
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
