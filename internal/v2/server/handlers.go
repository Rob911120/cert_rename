package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// ---------------------------------------------------------------------------
// Gemensamt: JSON-hjälpare + central felmappning (arkitekturregel 5)
// ---------------------------------------------------------------------------

// id64 accepterar både "123" och 123 i request-JSON (64-bitars ID:n skickas
// som strängar till UI:t för att undvika JS-precisionfel).
type id64 int64

func (v *id64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	*v = id64(n)
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "ogiltig JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// writeError är den ENDA översättningen från domänfel till HTTP-status.
func writeError(w http.ResponseWriter, err error) {
	var warnings domain.ValidationWarnings
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, domain.ErrFrozen), errors.Is(err, domain.ErrTransition):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &warnings):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"warnings": []string(warnings)})
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// Cert-mutationer
// ---------------------------------------------------------------------------

func (s *Server) handleCertUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertID id64   `json:"cert_id"`
		Field  string `json:"field"`
		Value  string `json:"value"`
		Who    string `json:"who"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Who == "" {
		req.Who = "rob"
	}
	view, err := s.App.UpdateCertField(r.Context(), int64(req.CertID), req.Field, req.Value, req.Who)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, s.certJSON(r.Context(), view))
}

func (s *Server) handleCertName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertID id64   `json:"cert_id"`
		Name   string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	view, err := s.App.SetNameOverride(r.Context(), int64(req.CertID), req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"proposed_filename": view.ProposedFilename})
}

func (s *Server) handleCertSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertID  id64 `json:"cert_id"`
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := s.App.SaveCert(r.Context(), int64(req.CertID), req.Confirm)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) handleCertArchive(w http.ResponseWriter, r *http.Request) {
	s.certTransition(w, r, s.App.ArchiveCert)
}

func (s *Server) handleCertUnarchive(w http.ResponseWriter, r *http.Request) {
	s.certTransition(w, r, s.App.UnarchiveCert)
}

func (s *Server) certTransition(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, certID int64) error) {
	var req struct {
		CertID id64 `json:"cert_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := fn(r.Context(), int64(req.CertID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Länkar
// ---------------------------------------------------------------------------

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertID        id64   `json:"cert_id"`
		DeliveryRowID id64   `json:"delivery_row_id"`
		OrderNumber   string `json:"order_number"`
		Who           string `json:"who"`
	}
	if !decode(w, r, &req) {
		return
	}
	source := "manual"
	if req.Who == "sickan" {
		source = "sickan"
	}
	link, err := s.App.ConfirmLink(r.Context(), int64(req.CertID), int64(req.DeliveryRowID), req.OrderNumber, source)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, linkJSONOf(link))
}

func (s *Server) handleLinkReject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkID id64 `json:"link_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.App.RejectLink(r.Context(), int64(req.LinkID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Orderrader, noteringar, tasks
// ---------------------------------------------------------------------------

func (s *Server) handleRowDelivered(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryRowIDs []id64 `json:"delivery_row_ids"`
		Delivered      bool   `json:"delivered"`
	}
	if !decode(w, r, &req) {
		return
	}
	ids := make([]int64, len(req.DeliveryRowIDs))
	for i, v := range req.DeliveryRowIDs {
		ids[i] = int64(v)
	}
	if err := s.App.MarkDelivered(r.Context(), ids, req.Delivered); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleNoteAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind        string `json:"kind"`
		RefID       id64   `json:"ref_id"`
		OrderNumber string `json:"order_number"`
		PartNumber  string `json:"part_number"`
		Author      string `json:"author"`
		Text        string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Author == "" {
		req.Author = "rob"
	}
	note, err := s.App.AddNote(r.Context(), req.Kind, int64(req.RefID), req.OrderNumber, req.PartNumber, req.Author, req.Text)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, note)
}

func (s *Server) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID id64 `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.App.DeleteNote(r.Context(), int64(req.ID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTasksList(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Repo.ListTasks(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"tasks": orEmptyTasks(tasks)})
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text        string `json:"text"`
		DueDate     string `json:"due_date"`
		OrderNumber string `json:"order_number"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := s.App.AddTask(r.Context(), req.Text, req.DueDate, req.OrderNumber, "rob")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *Server) handleTaskDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID id64 `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.App.CompleteTask(r.Context(), int64(req.ID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID id64 `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.App.DeleteTask(r.Context(), int64(req.ID)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// PDF, refresh, upload, worker, config, costs
// ---------------------------------------------------------------------------

func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("cert_id"), 10, 64)
	if err != nil {
		http.Error(w, "cert_id saknas", http.StatusBadRequest)
		return
	}
	c, err := s.Repo.GetCert(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	path := store.StorePath(s.Config(), c.StoredName)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+c.OriginalFilename+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	if cfg.MonitorURL == "" || cfg.MonitorUser == "" || cfg.MonitorPassword == "" {
		http.Error(w, "Monitor-anslutning saknas — fyll i uppgifterna i ⚙️ Inställningar", http.StatusBadRequest)
		return
	}
	s.KickSync()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"kicked": true})
}

// handleUpload tar emot drag-drop: .eml läggs i inkorgen (workern tar dem),
// .pdf går direkt genom extraktionsvägen (kräver att workern kör).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "ogiltig multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	var saved, ingested int
	for _, fhs := range r.MultipartForm.File {
		for _, fh := range fhs {
			name := filepath.Base(fh.Filename)
			if !store.SafeName(name) {
				http.Error(w, "otillåtet filnamn: "+name, http.StatusBadRequest)
				return
			}
			f, err := fh.Open()
			if err != nil {
				writeError(w, err)
				return
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				writeError(w, err)
				return
			}
			switch strings.ToLower(filepath.Ext(name)) {
			case ".eml":
				inbox := s.Config().InboxDir
				if inbox == "" {
					// Utan guard hamnar filen i processens arbetskatalog och
					// processas aldrig — men UI:t hade rapporterat "Mottaget".
					http.Error(w, "ingen inkorgsmapp konfigurerad — öppna ⚙️ Inställningar", http.StatusBadRequest)
					return
				}
				if _, err := store.WriteUniqueFile(inbox, name, data); err != nil {
					writeError(w, err)
					return
				}
				saved++
			case ".pdf":
				in := s.currentIntake()
				if in == nil {
					http.Error(w, "starta arbetaren först (▶) för direkt PDF-intag", http.StatusConflict)
					return
				}
				if _, _, err := in.IngestPDF(r.Context(), name, data); err != nil {
					writeError(w, err)
					return
				}
				ingested++
			default:
				http.Error(w, "bara .eml och .pdf stöds", http.StatusBadRequest)
				return
			}
		}
	}
	if saved > 0 {
		s.KickIntake()
	}
	writeJSON(w, map[string]int{"eml": saved, "pdf": ingested})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.StartWorker(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"running": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.StopWorker()
	writeJSON(w, map[string]bool{"running": false})
}

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Config())
}

func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	var cfg store.Config
	if !decode(w, r, &cfg) {
		return
	}
	// Tomt hemlighets-/inloggningsfält = behåll befintligt värde. Webbläsaren tömmer
	// ofta programsatta lösenordsfält (autocomplete=off), och utan detta skulle ett
	// spar utan att röra fälten nollställa credentials man redan lagt in. Samma känsla
	// som Anthropic-API-nyckeln — den ska sitta kvar. Monitor-URL:en skickas inte längre
	// av UI:t (hårdkodad), så tom URL behåller den effektiva (default/env).
	prev := s.Config()
	if strings.TrimSpace(cfg.ApiKey) == "" {
		cfg.ApiKey = prev.ApiKey
	}
	if strings.TrimSpace(cfg.MonitorUser) == "" {
		cfg.MonitorUser = prev.MonitorUser
	}
	if strings.TrimSpace(cfg.MonitorPassword) == "" {
		cfg.MonitorPassword = prev.MonitorPassword
	}
	if strings.TrimSpace(cfg.MonitorURL) == "" {
		cfg.MonitorURL = prev.MonitorURL
	}
	cfg.NormalizeUpcoming()
	cfg.NormalizeMonitorURL()
	if err := store.SaveConfig(cfg); err != nil {
		writeError(w, err)
		return
	}
	s.setConfig(cfg)
	s.Logf("⚙️  konfiguration sparad")
	// Pågående intags-worker behåller sin gamla Claude-klient — säg det i
	// stället för att tyst fortsätta med fel nyckel.
	if cfg.ApiKey != prev.ApiKey && s.workerRunning() {
		s.Logf("ℹ️  API-nyckeln byttes — stoppa och starta intaget (▶) för att den nya ska användas")
	}
	writeJSON(w, cfg)
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	s.costsMu.Lock()
	defer s.costsMu.Unlock()
	writeJSON(w, s.costs)
}

func orEmptyTasks(t []*store.Task) []*store.Task {
	if t == nil {
		return []*store.Task{}
	}
	return t
}
