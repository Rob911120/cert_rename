package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	"cert-renamer/internal/store"
)

const maxUploadBytes = 64 * 1024 * 1024 // 64 MB per fil

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "välj inbox-mapp först", 400)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "kunde inte läsa multipart: "+err.Error(), 400)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "saknar fält 'file': "+err.Error(), 400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "läsfel: "+err.Error(), 400)
		return
	}
	name := filepath.Base(header.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	manualB := strings.TrimSpace(r.FormValue("b_number"))

	switch ext {
	case ".eml":
		if err := os.MkdirAll(c.InboxDir, 0755); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		dst, err := store.WriteUniqueFile(c.InboxDir, name, data)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.Logf("📥 uppladdad .eml: %s", filepath.Base(dst))
		time.AfterFunc(500*time.Millisecond, func() {
			select {
			case s.workerKick <- struct{}{}:
			default:
			}
		})
		s.BroadcastStats()
		writeJSON(w, map[string]any{"kind": "eml", "name": filepath.Base(dst)})

	case ".pdf":
		if c.ApiKey == "" {
			http.Error(w, "ingen API-nyckel — öppna ⚙️ Inställningar", 400)
			return
		}
		for _, d := range []string{store.QueueDir(c), store.ReviewDir(c)} {
			if err := os.MkdirAll(d, 0755); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		bNums := eml.ExtractBNumbers(manualB, name)
		if manualB != "" {
			s.Logf("📥 uppladdad PDF: %s — manuell B-nr: %q → bNums=%v", name, manualB, bNums)
		} else {
			s.Logf("📥 uppladdad PDF: %s — kör Extract (B-nr från filnamn: %v)", name, bNums)
		}
		hintSubject := "Manuellt indragen PDF — originalfilnamn: " + name
		client := anthropic.NewClient(option.WithAPIKey(c.ApiKey))
		extr, err := ai.Extract(r.Context(), s, &client, data, hintSubject, "", name)
		fakeEml := strings.TrimSuffix(name, filepath.Ext(name)) + "-upload.eml"
		att := &eml.Attachment{Filename: name, Data: data}

		if err != nil {
			s.Logf("   ❌ %s — Claude-fel: %v", name, err)
			store.MoveToReview(c, fakeEml, nil, att, nil, bNums, fmt.Sprintf("claude error: %v", err))
			s.BroadcastStats()
			s.BroadcastReview()
			writeJSON(w, map[string]any{"kind": "pdf", "verdict": "review (claude error)"})
			return
		}
		fails := cert.Validate(extr, bNums)
		if len(fails) > 0 {
			s.Logf("   ❌ %s — %s", name, strings.Join(fails, "; "))
			store.MoveToReview(c, fakeEml, nil, att, extr, bNums, strings.Join(fails, "; "))
			s.BroadcastStats()
			s.BroadcastReview()
			writeJSON(w, map[string]any{"kind": "pdf", "verdict": "review: " + strings.Join(fails, "; ")})
			return
		}
		finalName := cert.BuildFilename(extr, bNums)
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		meta := store.PdfMeta{
			Charge:           extr.Charge,
			Material:         extr.MaterialShort,
			ProductForm:      extr.ProductForm,
			Dimensions:       extr.Dimensions,
			BNumbers:         bNums,
			Confidence:       extr.Confidence,
			Issues:           extr.Issues,
			OriginalFilename: name,
			ExtractedAt:      time.Now().Format(time.RFC3339),
			Schema:           4,
			Status:           "queue",
			Hash:             hash,
		}

		existingPath := filepath.Join(store.QueueDir(c), finalName)
		if existingMeta, ok := store.ReadMetadata(existingPath); ok && existingMeta.Hash == hash {
			if err := os.WriteFile(existingPath, data, 0644); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if err := store.EmbedMetadata(existingPath, meta); err != nil {
				s.Logf("   ⚠️  metadata-fel %s: %v", finalName, err)
			}
			s.Logf("   ♻️  ersatte befintlig: %s", finalName)
			s.BroadcastStats()
			s.BroadcastQueue()
			writeJSON(w, map[string]any{"kind": "pdf", "verdict": "ersatte befintlig: " + finalName})
			return
		}

		dst, err := store.WriteUniqueFile(store.QueueDir(c), finalName, data)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := store.EmbedMetadata(dst, meta); err != nil {
			s.Logf("   ⚠️  metadata-fel %s: %v", filepath.Base(dst), err)
		}
		s.Logf("   ✅ %s", filepath.Base(dst))
		s.IncrementOK()
		s.BroadcastStats()
		s.BroadcastQueue()
		writeJSON(w, map[string]any{"kind": "pdf", "verdict": "kö: " + filepath.Base(dst)})

	default:
		http.Error(w, "bara .pdf och .eml stöds", 400)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
