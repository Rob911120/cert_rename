package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/store"
)

// handleUploadDeliveryNote tar emot en följesedel-BILD (multipart, fält "image"),
// kör Claude-vision (ExtractFromImage), sparar bilden under inbox/delivery_notes/
// och skapar en delivery_notes-rad (status unmatched). Returnerar id + extraktion.
// Matchning/registrering sker sedan via Sickan (match → propose → register).
func (s *Server) handleUploadDeliveryNote(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	c := s.snapshotCfg()
	if c.InboxDir == "" {
		http.Error(w, "välj inbox-mapp först", 400)
		return
	}
	if c.ApiKey == "" {
		http.Error(w, "ingen API-nyckel — öppna ⚙️ Inställningar", 400)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "kunde inte läsa multipart: "+err.Error(), 400)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "saknar fält 'image': "+err.Error(), 400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "läsfel: "+err.Error(), 400)
		return
	}

	name := filepath.Base(header.Filename)
	mediaType := store.ImageMediaType(name)
	if mediaType == "" {
		http.Error(w, "bara PNG/JPEG/GIF/WebP stöds för följesedel-bild", 400)
		return
	}

	dir := store.DeliveryNotesDir(c)
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	dst, err := store.WriteUniqueFile(dir, name, data)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	imageFilename := filepath.Base(dst)

	s.Logf("📥 uppladdad följesedel-bild: %s — kör vision", imageFilename)
	client := anthropic.NewClient(option.WithAPIKey(c.ApiKey))
	ext, err := ai.ExtractFromImage(r.Context(), s, &client, data, mediaType)
	if err != nil {
		s.Logf("   ❌ vision-fel: %v", err)
		http.Error(w, "vision misslyckades: "+err.Error(), 502)
		return
	}

	dn := &store.DeliveryNote{
		ImageFilename:      imageFilename,
		Supplier:           ext.Supplier,
		DeliveryDate:       ext.DeliveryDate,
		OrderNumber:        ext.OrderNumber,
		Charge:             ext.Charge,
		Material:           ext.Material,
		Quantity:           ext.Quantity,
		Unit:               ext.Unit,
		DeliveryNoteNumber: ext.DeliveryNoteNumber,
		BNumbers:           marshalJSON(ext.BNumbers),
		Confidence:         ext.Confidence,
		Status:             store.DNUnmatched,
	}
	id, err := s.repo.InsertDeliveryNote(dn)
	if err != nil {
		s.Logf("   ⚠️  kunde inte spara följesedel i DB: %v", err)
		http.Error(w, "DB-fel: "+err.Error(), 500)
		return
	}
	dn.ID = id
	s.Logf("   ✅ följesedel #%d sparad (leverantör=%q order=%q charge=%q)", id, ext.Supplier, ext.OrderNumber, ext.Charge)
	s.BroadcastStats()
	writeJSON(w, map[string]any{"id": id, "extraction": ext, "image": imageFilename})
}
