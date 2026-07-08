package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/cert"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// writeV1Pdf lägger en fejkad V1-PDF med sidecar-metadata (ReadMetadata
// föredrar sidecar, så pdfcpu behövs inte i testet).
func writeV1Pdf(t *testing.T, dir, name string, meta store.PdfMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4 "+name), 0644); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(store.MetaSidecarPath(path), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImportV1(t *testing.T) {
	dir := t.TempDir()
	cfg := store.Config{InboxDir: dir}
	db, err := store.Open(filepath.Join(dir, "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := app.New(store.NewRepository(db), func() store.Config { return cfg },
		func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }, nil)
	ctx := context.Background()

	writeV1Pdf(t, store.ApprovedDir(cfg), "70703-plat-16-S690QL-B127562.pdf", store.PdfMeta{
		Charge: "70703", Material: "S690QL", EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", Dimensions: "16", BNumbers: []string{"B127562"},
		OriginalFilename: "SSAB_orig.pdf", ExtractedAt: "2026-06-01T10:00:00Z",
		Hash: "origalhash123", Schema: 5, Status: "approved",
	})
	writeV1Pdf(t, store.QueueDir(cfg), "43136-plat-60-S355J2N-B128293.pdf", store.PdfMeta{
		Charge: "43136", Material: "S355J2+N", EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", Dimensions: "60", BNumbers: []string{"B128293"},
		OriginalFilename: "cert_b128293.pdf", Hash: "origalhash456",
	})
	// PDF utan metadata — ska hoppas över
	if err := os.WriteFile(filepath.Join(store.QueueDir(cfg), "okand.pdf"), []byte("%PDF x"), 0644); err != nil {
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
	// V1-cert saknar ALDRIG is_legible/is_unaltered (fanns inte i V1:s
	// extraktion) — importern ska sätta vettiga defaults i stället för att
	// låta dem se ut som "problem upptäckt".
	if !saved.IsLegible || !saved.IsUnaltered {
		t.Errorf("importerat cert ska få IsLegible/IsUnaltered=true: %+v", saved)
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

	// Queue → levande, och ALDRIG stämplad som verifierad 3.1 (bara approved/
	// var validerad av V1 — "gissa aldrig").
	living, err := a.Repo.GetCertByHash(ctx, "origalhash456")
	if err != nil {
		t.Fatal(err)
	}
	if living.Status != domain.CertMottagen {
		t.Errorf("kö-cert ska vara levande: %s", living.Status)
	}
	if living.CertType != "" {
		t.Errorf("kö-cert ska inte stämplas med certtyp: %q", living.CertType)
	}
	if saved.CertType != "3.1" {
		t.Errorf("approved-cert ska stämplas 3.1: %q", saved.CertType)
	}
	if !living.IsLegible || !living.IsUnaltered {
		t.Errorf("importerat cert ska få IsLegible/IsUnaltered=true: %+v", living)
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

// En import som kraschade mellan IngestCert och MarkImportedSaved lämnar ett
// approved-cert som levande — en omkörning ska reparera (stämpla sparad), inte
// hoppa över det som ren dublett för alltid.
func TestImportV1RepairsInterruptedSavedImport(t *testing.T) {
	dir := t.TempDir()
	cfg := store.Config{InboxDir: dir}
	db, err := store.Open(filepath.Join(dir, "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := app.New(store.NewRepository(db), func() store.Config { return cfg },
		func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }, nil)
	ctx := context.Background()

	writeV1Pdf(t, store.ApprovedDir(cfg), "87210-plat-15-S690-B127562.pdf", store.PdfMeta{
		Charge: "87210", Material: "S690", BNumbers: []string{"B127562"},
		OriginalFilename: "orig.pdf", ExtractedAt: "2026-06-01T10:00:00Z", Hash: "avbrutenhash",
	})

	// Simulera avbrottet: certet hann in som levande, sparad-stämpeln hann inte.
	if _, _, err := a.IngestCert(ctx, app.IngestInput{
		OriginalFilename: "orig.pdf", Data: []byte("x"),
		Extraction:   &cert.Extraction{CertType: "3.1"},
		HashOverride: "avbrutenhash",
	}); err != nil {
		t.Fatal(err)
	}

	st, err := ImportV1(ctx, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
	c, err := a.Repo.GetCertByHash(ctx, "avbrutenhash")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != domain.CertSparad || c.SavedAt != "2026-06-01T10:00:00Z" {
		t.Errorf("avbruten import reparerades inte: status=%s saved_at=%q", c.Status, c.SavedAt)
	}
}
