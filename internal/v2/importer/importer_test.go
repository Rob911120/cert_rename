package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// writeV1Pdf lägger en fejkad V1-PDF med sidecar-metadata (ReadMetadata
// föredrar sidecar, så pdfcpu behövs inte i testet).
func writeV1Pdf(t *testing.T, dir, name string, meta v1store.PdfMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4 "+name), 0644); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(v1store.MetaSidecarPath(path), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImportV1(t *testing.T) {
	dir := t.TempDir()
	cfg := v1store.Config{InboxDir: dir}
	db, err := store.Open(filepath.Join(dir, "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := app.New(store.NewRepository(db), func() v1store.Config { return cfg },
		func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }, nil)
	ctx := context.Background()

	writeV1Pdf(t, v1store.ApprovedDir(cfg), "70703-plat-16-S690QL-B127562.pdf", v1store.PdfMeta{
		Charge: "70703", Material: "S690QL", EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", Dimensions: "16", BNumbers: []string{"B127562"},
		OriginalFilename: "SSAB_orig.pdf", ExtractedAt: "2026-06-01T10:00:00Z",
		Hash: "origalhash123", Schema: 5, Status: "approved",
	})
	writeV1Pdf(t, v1store.QueueDir(cfg), "43136-plat-60-S355J2N-B128293.pdf", v1store.PdfMeta{
		Charge: "43136", Material: "S355J2+N", EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", Dimensions: "60", BNumbers: []string{"B128293"},
		OriginalFilename: "cert_b128293.pdf", Hash: "origalhash456",
	})
	// PDF utan metadata — ska hoppas över
	if err := os.WriteFile(filepath.Join(v1store.QueueDir(cfg), "okand.pdf"), []byte("%PDF x"), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := ImportV1(ctx, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 2 || st.NoMeta != 1 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}

	// Approved → sparad/fryst, original outputPath, dedupe-hash = originalhash
	saved, err := a.Repo.GetCertByHash(ctx, "origalhash123")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != domain.CertSparad || saved.FinalFilename != "70703-plat-16-S690QL-B127562.pdf" {
		t.Errorf("sparad: %+v", saved)
	}
	if saved.SavedAt != "2026-06-01T10:00:00Z" {
		t.Errorf("saved_at ska tas från metadatan: %q", saved.SavedAt)
	}
	if saved.OriginalFilename != "SSAB_orig.pdf" {
		t.Errorf("original: %q", saved.OriginalFilename)
	}
	// PDF:en kopierad in i V2-lagret
	if _, err := os.Stat(store.StorePath(cfg, saved.StoredName)); err != nil {
		t.Errorf("lagerfil saknas: %v", err)
	}
	// Fryst: mutation avvisas
	if _, err := a.UpdateCertField(ctx, saved.ID, "material", "X", "rob"); err == nil {
		t.Error("importerat sparat cert ska vara fryst")
	}

	// Queue → levande
	living, err := a.Repo.GetCertByHash(ctx, "origalhash456")
	if err != nil {
		t.Fatal(err)
	}
	if living.Status != domain.CertMottagen {
		t.Errorf("kö-cert ska vara levande: %s", living.Status)
	}

	// Om-import är no-op (dubbletter på hash)
	st2, err := ImportV1(ctx, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Imported != 0 || st2.Skipped != 2 {
		t.Errorf("om-import: %+v", st2)
	}
	all, _ := a.Repo.ListCerts(ctx, "")
	if len(all) != 2 {
		t.Errorf("cert-rader efter om-import: %d", len(all))
	}
}
