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
			"inbox_dir":            c.InboxDir,
			"theme":                c.Theme,
			"autostart":            c.Autostart,
			"api_key_hint":         hint,
			"sickan_model":         c.SickanModel,
			"b_number_mode":        c.BNumberMode,
			"monitor_url":          c.MonitorURL,
			"monitor_user":         c.MonitorUser,
			"monitor_configured":   c.MonitorURL != "" && c.MonitorUser != "" && c.MonitorPassword != "",
			"monitor_ui_auto_save": c.MonitorUIAutoSave,
			"upcoming_enabled":     c.UpcomingEnabled,
			"upcoming_time":        c.UpcomingTime,
			"upcoming_window_days": c.UpcomingWindowDays,
			"upcoming_back_days":   c.UpcomingBackDays,
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
		// Tomma fält i POST:en betyder "behåll det sparade" — UI:t skickar inte
		// alltid med hemligheter (samma mönster som API-nyckeln).
		if c.ApiKey == "" {
			c.ApiKey = s.cfg.ApiKey
		}
		if c.SickanModel == "" {
			c.SickanModel = s.cfg.SickanModel
		}
		if c.MonitorURL == "" {
			c.MonitorURL = s.cfg.MonitorURL
		}
		if c.MonitorUser == "" {
			c.MonitorUser = s.cfg.MonitorUser
		}
		if c.MonitorPassword == "" {
			c.MonitorPassword = s.cfg.MonitorPassword
		}
		// MonitorUIAutoSave (bool) skickas alltid med av UI:t, som autostart.
		// Ingen Monitor-inloggning här — den sker lazy först vid faktisk
		// API-användning (annars loggas den interaktiva Monitor-sessionen ut).
		c.NormalizeUpcoming() // defaulta/validera UpcomingTime + WindowDays
		s.cfg = c
		s.mu.Unlock()
		if err := store.SaveConfig(c); err != nil {
			s.Logf("⚠️  Kunde inte spara config: %v", err)
		}
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

// resolveRequestFile validerar kind/name/base-parametrarna och returnerar den
// verifierade sökvägen innanför rätt mapp. Delas av handleFile och handleOpen.
func (s *Server) resolveRequestFile(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	kind := q.Get("kind")
	name := q.Get("name")
	base := q.Get("base")
	if !store.SafeName(name) {
		http.Error(w, "ogiltigt namn", http.StatusBadRequest)
		return "", false
	}
	c, ok := s.requireInbox(w)
	if !ok {
		return "", false
	}
	var dir string
	switch kind {
	case "queue":
		dir = store.QueueDir(c)
	case "approved":
		dir = store.ApprovedDir(c)
	case "review":
		if !store.SafeName(base) {
			http.Error(w, "ogiltig base", http.StatusBadRequest)
			return "", false
		}
		dir = filepath.Join(store.ReviewDir(c), base)
	default:
		http.Error(w, "ogiltig kind", http.StatusBadRequest)
		return "", false
	}
	full := filepath.Clean(filepath.Join(dir, name))
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
		http.Error(w, "ogiltig sökväg", http.StatusBadRequest)
		return "", false
	}
	return full, true
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	full, ok := s.resolveRequestFile(w, r)
	if !ok {
		return
	}
	name := filepath.Base(full)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		w.Header().Set("Content-Type", "application/pdf")
	case ".eml":
		w.Header().Set("Content-Type", "message/rfc822")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, name))
	http.ServeFile(w, r, full)
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	full, ok := s.resolveRequestFile(w, r)
	if !ok {
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
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Filename string `json:"filename"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := body.Filename
	if !store.SafeName(name) {
		http.Error(w, "ogiltigt filnamn", 400)
		return
	}
	c, ok := s.requireInbox(w)
	if !ok {
		return
	}
	if _, err := store.ApproveQueueItem(c, name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Uppdatera status i DB
	if s.repo != nil {
		if cert, err := s.repo.GetCertificateByFilename(name); err == nil {
			if err := s.repo.UpdateCertificateStatus(cert.ID, "approved"); err != nil {
				s.Logf("⚠️  DB-status-update misslyckades för %s: %v", name, err)
			}
		} else {
			s.Logf("⚠️  Kunde inte hitta %s i DB: %v", name, err)
		}
	}
	s.BroadcastQueue()
	s.BroadcastStats()
	w.WriteHeader(204)
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Base string `json:"base"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	base := body.Base
	if !store.SafeName(base) {
		http.Error(w, "ogiltig base", 400)
		return
	}
	c, ok := s.requireInbox(w)
	if !ok {
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
	if !requirePOST(w, r) {
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
	if !decodeJSON(w, r, &body) {
		return
	}
	c, ok := s.requireInbox(w)
	if !ok {
		return
	}
	newName, insertErr, err := store.PromoteReview(c, s.repo, store.PromoteReviewInput{
		Base:        body.Base,
		PdfFilename: body.PdfFilename,
		Charge:      body.Charge,
		Material:    body.Material,
		ProductForm: body.ProductForm,
		Dimensions:  body.Dimensions,
		BNumbers:    body.BNumbers,
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if insertErr != nil {
		s.Logf("⚠️  DB-insert vid promote misslyckades: %v", insertErr)
	}

	s.BroadcastQueue()
	s.BroadcastReview()
	s.BroadcastStats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "new_filename": newName})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
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
	if !requirePOST(w, r) {
		return
	}
	s.mu.Lock()
	if s.cancelFn != nil {
		s.cancelFn()
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}
