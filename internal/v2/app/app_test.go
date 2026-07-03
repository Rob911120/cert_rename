package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

type fakeNotifier struct {
	logs     []string
	overview int
}

func (f *fakeNotifier) Logf(format string, args ...any) { f.logs = append(f.logs, format) }
func (f *fakeNotifier) OverviewChanged()                { f.overview++ }

func testApp(t *testing.T) (*App, *fakeNotifier, v1store.Config) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := v1store.Config{InboxDir: dir}
	notify := &fakeNotifier{}
	clock := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	a := New(store.NewRepository(db), func() v1store.Config { return cfg }, func() time.Time { return clock }, notify)
	return a, notify, cfg
}

// seedCert lägger ett komplett, giltigt cert med lagerfil på disk.
func seedCert(t *testing.T, a *App, cfg v1store.Config) *domain.Cert {
	t.Helper()
	data := []byte("%PDF-1.4 fejkcert for test\n")
	hash := store.HashPDF(data)
	stored := store.StoredName(hash, "SSAB_43136.pdf")
	if _, err := store.WriteStoreFile(cfg, stored, data); err != nil {
		t.Fatal(err)
	}
	c := &domain.Cert{
		PdfHash: hash, OriginalFilename: "SSAB_43136.pdf", StoredName: stored,
		CertType: "3.1", Charge: "43136", Material: "S690QL",
		EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", Dimensions: "60",
		BNumbers: []string{"B128293"}, Confidence: "high",
		ReceivedAt: "2026-07-01T08:00:00Z",
	}
	if _, err := a.Repo.InsertCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUpdateCertFieldMovesLivingName(t *testing.T) {
	a, notify, cfg := testApp(t)
	c := seedCert(t, a, cfg)
	ctx := context.Background()

	view, err := a.GetCertView(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ProposedFilename != "43136-plat-60-S690QL-B128293.pdf" {
		t.Fatalf("utgångsnamn = %q", view.ProposedFilename)
	}

	view, err = a.UpdateCertField(ctx, c.ID, "material", "S355J2+N", "rob")
	if err != nil {
		t.Fatal(err)
	}
	if view.ProposedFilename != "43136-plat-60-S355J2+N-B128293.pdf" {
		t.Errorf("namnet flyttade sig inte: %q", view.ProposedFilename)
	}
	if len(view.Cert.CorrectionLog) != 1 || view.Cert.CorrectionLog[0].TS != "2026-07-03T10:00:00Z" {
		t.Errorf("rättelselogg: %+v", view.Cert.CorrectionLog)
	}
	if notify.overview == 0 {
		t.Error("ingen SSE-ping efter mutation")
	}
}

func TestConfirmedLinkChangesName(t *testing.T) {
	a, _, cfg := testApp(t)
	c := seedCert(t, a, cfg)
	ctx := context.Background()

	// Förslag rör inte namnet…
	if _, err := a.SuggestLink(ctx, c.ID, 0, "B999999", "auto_b_number"); err != nil {
		t.Fatal(err)
	}
	view, _ := a.GetCertView(ctx, c.ID)
	if !strings.Contains(view.ProposedFilename, "B128293") {
		t.Errorf("förslag ska inte påverka namnet: %q", view.ProposedFilename)
	}

	// …men bekräftelse gör det (länk-B-nummer vinner över råa).
	link := view.Links[0]
	if _, err := a.ConfirmLink(ctx, c.ID, 0, link.OrderNumber, "manual"); err != nil {
		t.Fatal(err)
	}
	view, _ = a.GetCertView(ctx, c.ID)
	if view.ProposedFilename != "43136-plat-60-S690QL-B999999.pdf" {
		t.Errorf("bekräftad länk ska styra namnet: %q", view.ProposedFilename)
	}

	// SuggestLink skriver aldrig över ett beslut
	if l, err := a.SuggestLink(ctx, c.ID, 0, "B999999", "auto_b_number"); err != nil || l != nil {
		t.Errorf("förslag på befintlig länk ska vara no-op, fick %v/%v", l, err)
	}
}

func TestSaveCertIdempotent(t *testing.T) {
	a, _, cfg := testApp(t)
	c := seedCert(t, a, cfg)
	ctx := context.Background()

	res, err := a.SaveCert(ctx, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalFilename != "43136-plat-60-S690QL-B128293.pdf" {
		t.Errorf("slutnamn = %q", res.FinalFilename)
	}
	if _, err := os.Stat(res.OutputPath); err != nil {
		t.Fatalf("utfil saknas: %v", err)
	}

	// Dubbelanrop: idempotent no-op, samma fil, ingen _2
	res2, err := a.SaveCert(ctx, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.AlreadySaved || res2.OutputPath != res.OutputPath {
		t.Errorf("andra anropet: %+v", res2)
	}
	pdfs := listPDFs(t, store.OutputDir(cfg))
	if len(pdfs) != 1 {
		t.Errorf("utmappen ska ha exakt 1 PDF, har %v", pdfs)
	}

	// Fryst: mutatorer avvisar med ErrFrozen
	if _, err := a.UpdateCertField(ctx, c.ID, "material", "X", "rob"); !errors.Is(err, domain.ErrFrozen) {
		t.Errorf("update efter spar: err = %v", err)
	}
	if _, err := a.SetNameOverride(ctx, c.ID, "nytt"); !errors.Is(err, domain.ErrFrozen) {
		t.Errorf("override efter spar: err = %v", err)
	}
	if err := a.ArchiveCert(ctx, c.ID); !errors.Is(err, domain.ErrFrozen) {
		t.Errorf("arkivera efter spar: err = %v", err)
	}
}

func TestSaveCertCrashRecovery(t *testing.T) {
	a, _, cfg := testApp(t)
	c := seedCert(t, a, cfg)
	ctx := context.Background()

	// Simulera krasch: utfilen hann skrivas (men ej bäddas in, ej DB-commit).
	data, _ := os.ReadFile(store.StorePath(cfg, c.StoredName))
	outDir := store.OutputDir(cfg)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "43136-plat-60-S690QL-B128293.pdf"
	if err := os.WriteFile(filepath.Join(outDir, name), data, 0644); err != nil {
		t.Fatal(err)
	}

	res, err := a.SaveCert(ctx, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalFilename != name {
		t.Errorf("om-spar ska återanvända utfilen: %q", res.FinalFilename)
	}
	if pdfs := listPDFs(t, outDir); len(pdfs) != 1 {
		t.Errorf("kraschåterhämtning skapade dubblett: %v", pdfs)
	}
}

func TestSaveCertWarningsRequireConfirm(t *testing.T) {
	a, _, cfg := testApp(t)
	// Ofullständigt cert: saknar charge + dimensioner
	data := []byte("%PDF-1.4 ofullstandig\n")
	hash := store.HashPDF(data)
	stored := store.StoredName(hash, "x.pdf")
	if _, err := store.WriteStoreFile(cfg, stored, data); err != nil {
		t.Fatal(err)
	}
	c := &domain.Cert{
		PdfHash: hash, OriginalFilename: "x.pdf", StoredName: stored,
		CertType: "3.1", Material: "S690QL", EnStandardPresent: true, IsEnglish: true,
		BNumbers: []string{"B111111"}, ReceivedAt: "t",
	}
	ctx := context.Background()
	if _, err := a.Repo.InsertCert(ctx, c); err != nil {
		t.Fatal(err)
	}

	res, err := a.SaveCert(ctx, c.ID, false)
	var warnings domain.ValidationWarnings
	if !errors.As(err, &warnings) {
		t.Fatalf("utan confirm: err = %v, vill ha ValidationWarnings", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("varningarna ska följa med i resultatet")
	}
	// Certet är fortfarande levande
	got, _ := a.Repo.GetCert(ctx, c.ID)
	if got.Status != domain.CertMottagen {
		t.Errorf("status efter avvisat spar = %s", got.Status)
	}

	// "Spara ändå"
	res, err = a.SaveCert(ctx, c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalFilename == "" {
		t.Error("confirm=true ska spara")
	}
}

func TestArchiveAndUnarchive(t *testing.T) {
	a, _, cfg := testApp(t)
	c := seedCert(t, a, cfg)
	ctx := context.Background()

	if err := a.ArchiveCert(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Repo.GetCert(ctx, c.ID); got.Status != domain.CertArkiverad {
		t.Errorf("status = %s", got.Status)
	}
	// Arkiverad kan inte sparas
	if _, err := a.SaveCert(ctx, c.ID, true); !errors.Is(err, domain.ErrTransition) {
		t.Errorf("spara arkiverad: err = %v", err)
	}
	if err := a.UnarchiveCert(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Repo.GetCert(ctx, c.ID); got.Status != domain.CertMottagen {
		t.Errorf("status efter ångra = %s", got.Status)
	}
}

func listPDFs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pdf") {
			out = append(out, e.Name())
		}
	}
	return out
}
