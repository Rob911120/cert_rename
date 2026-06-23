package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/store"
	"cert-renamer/internal/worker"
)

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.mu.Lock()
		c := s.cfg
		s.mu.Unlock()
		hint := ""
		if len(c.ApiKey) >= 4 {
			hint = "••••" + c.ApiKey[len(c.ApiKey)-4:]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"inbox_dir":     c.InboxDir,
			"theme":         c.Theme,
			"autostart":     c.Autostart,
			"api_key_hint":  hint,
			"sickan_model":  c.SickanModel,
			"b_number_mode": c.BNumberMode,
		})
		return
	}
	if r.Method == http.MethodPost {
		var c store.Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		s.mu.Lock()
		if c.ApiKey == "" {
			c.ApiKey = s.cfg.ApiKey
		}
		if c.SickanModel == "" {
			c.SickanModel = s.cfg.SickanModel
		}
		s.cfg = c
		s.mu.Unlock()
		_ = store.SaveConfig(c)
		w.WriteHeader(204)
		return
	}
	http.Error(w, "method not allowed", 405)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.listQueue())
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.listReview())
}

func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	prompt := r.URL.Query().Get("prompt")
	if prompt == "" {
		prompt = "Välj mapp"
	}
	path, err := nativeFolderDialog(prompt)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": path})
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	name := q.Get("name")
	base := q.Get("base")
	badName := func(v string) bool {
		return v == "" || strings.ContainsAny(v, `/\`) || strings.Contains(v, "..")
	}
	if badName(name) {
		http.Error(w, "ogiltigt namn", 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}
	var dir string
	switch kind {
	case "queue":
		dir = store.QueueDir(c)
	case "approved":
		dir = store.ApprovedDir(c)
	case "review":
		if badName(base) {
			http.Error(w, "ogiltig base", 400)
			return
		}
		dir = filepath.Join(store.ReviewDir(c), base)
	default:
		http.Error(w, "ogiltig kind", 400)
		return
	}
	full := filepath.Join(dir, name)
	cleanFull := filepath.Clean(full)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanFull, cleanDir+string(os.PathSeparator)) {
		http.Error(w, "ogiltig sökväg", 400)
		return
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		w.Header().Set("Content-Type", "application/pdf")
	case ".eml":
		w.Header().Set("Content-Type", "message/rfc822")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, name))
	http.ServeFile(w, r, cleanFull)
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	name := q.Get("name")
	base := q.Get("base")
	badName := func(v string) bool {
		return v == "" || strings.ContainsAny(v, `/\`) || strings.Contains(v, "..")
	}
	if badName(name) {
		http.Error(w, "ogiltigt namn", 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}
	var dir string
	switch kind {
	case "queue":
		dir = store.QueueDir(c)
	case "approved":
		dir = store.ApprovedDir(c)
	case "review":
		if badName(base) {
			http.Error(w, "ogiltig base", 400)
			return
		}
		dir = filepath.Join(store.ReviewDir(c), base)
	default:
		http.Error(w, "ogiltig kind", 400)
		return
	}
	full := filepath.Clean(filepath.Join(dir, name))
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
		http.Error(w, "ogiltig sökväg", 400)
		return
	}
	if _, err := os.Stat(full); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if err := openLocalFile(full); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

func openLocalFile(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		// `cmd /c start "" "<path>"` — robusta Windows-mönstret för att
		// öppna en lokal fil med default-app. Tom titel-arg krävs eftersom
		// `start` annars tolkar första citerade arg som fönster-titel.
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := body.Filename
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		http.Error(w, "ogiltigt filnamn", 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}
	if _, err := store.ApproveQueueItem(c, name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.BroadcastQueue()
	w.WriteHeader(204)
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Base string `json:"base"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	base := body.Base
	if base == "" || strings.Contains(base, "/") || strings.Contains(base, "\\") || strings.Contains(base, "..") {
		http.Error(w, "ogiltig base", 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}
	src := filepath.Join(store.ReviewDir(c), base)
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		http.Error(w, "review-mapp finns inte", 404)
		return
	}
	if err := os.MkdirAll(store.ArkiveratDir(c), 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	dst := store.UniquePath(store.ArkiveratDir(c), base)
	if err := os.Rename(src, dst); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.BroadcastStats()
	s.BroadcastReview()
	w.WriteHeader(204)
}

func (s *Server) handlePromoteReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Base        string   `json:"base"`
		PdfFilename string   `json:"pdf_filename"`
		Charge      string   `json:"charge"`
		Material    string   `json:"material"`
		ProductForm string   `json:"product_form"`
		Dimensions  string   `json:"dimensions"`
		BNumbers    []string `json:"b_numbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	c := s.cfg
	s.mu.Unlock()
	if c.InboxDir == "" {
		http.Error(w, "ingen inbox vald", 400)
		return
	}
	ext := &cert.Extraction{
		IsEN10204_3_1: true,
		CertType:      "3.1",
		Charge:        body.Charge,
		Material:      body.Material,
		MaterialShort: body.Material,
		ProductForm:   body.ProductForm,
		Dimensions:    body.Dimensions,
		Confidence:    "high",
	}
	newName, err := store.PromoteReviewToQueue(c, body.Base, body.PdfFilename, ext, body.BNumbers)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.BroadcastQueue()
	s.BroadcastReview()
	s.BroadcastStats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "new_filename": newName})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.startWorker(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) startWorker() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	c := s.cfg
	if c.ApiKey == "" {
		s.mu.Unlock()
		return fmt.Errorf("Ingen API-nyckel konfigurerad — öppna ⚙️ Inställningar och spara en nyckel")
	}
	if c.InboxDir == "" {
		s.mu.Unlock()
		return fmt.Errorf("Välj inbox-mapp innan du startar")
	}
	for _, d := range []string{c.InboxDir, store.QueueDir(c), store.ReviewDir(c), store.ApprovedDir(c), store.ArkiveratDir(c)} {
		if err := os.MkdirAll(d, 0755); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel
	s.running = true
	s.mu.Unlock()
	go func() {
		s.scanOverview()
		worker.Run(ctx, c, s, s.workerKick)
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.broadcastStateInternal()
	}()
	s.broadcastStateInternal()
	return nil
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.cancelFn != nil {
		s.cancelFn()
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}
