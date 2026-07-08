package intake

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cert-renamer/internal/v2/ai"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/cert"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/eml"
	"cert-renamer/internal/v2/store"
)

// fakeAI styr varje port-svar per test. Extract räknar anrop så att
// dubblett-/loop-skydd kan verifieras.
type fakeAI struct {
	category     string
	classifyYes  bool
	verifyYes    bool
	extractErr   error
	extractCalls int
}

func (f *fakeAI) MailCategory(ctx context.Context, c *eml.Content) (*ai.MailClassification, error) {
	cat := f.category
	if cat == "" {
		cat = ai.CategoryCertificate
	}
	return &ai.MailClassification{Category: cat, Confidence: "high", Reason: "test"}, nil
}

func (f *fakeAI) Classify(ctx context.Context, c *eml.Content) (*cert.Classification, error) {
	return &cert.Classification{IsCertMail: f.classifyYes, Confidence: "high", Reason: "test"}, nil
}

func (f *fakeAI) Verify(ctx context.Context, c *eml.Content) (*cert.Verification, error) {
	return &cert.Verification{AnyIsCert: f.verifyYes, Reason: "test"}, nil
}

func (f *fakeAI) Extract(ctx context.Context, pdf []byte, subject, body, filename string) (*ExtractResult, error) {
	f.extractCalls++
	if f.extractErr != nil {
		return nil, f.extractErr
	}
	return &ExtractResult{
		Extraction: &cert.Extraction{
			IsEN10204_3_1: true, CertType: "3.1", Charge: "43136", Material: "S690QL",
			EnStandardPresent: true, IsEnglish: true, ProductForm: "plåt", Dimensions: "60",
			Confidence: "high",
			// V2-fält: bevisar att hela pipelinen (inte bara IngestCert direkt)
			// för dem vidare oförändrade — is_legible=false ska INTE tvingas om.
			IsLegible: false, IsUnaltered: true, NormSystem: "Charpy",
		},
		Model: "fake-model", TokensIn: 1234, TokensOut: 56, DurationMS: 10,
	}, nil
}

func testIntake(t *testing.T, fake *fakeAI) (*Intake, store.Config) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := store.Config{InboxDir: dir}
	clock := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	a := app.New(store.NewRepository(db), func() store.Config { return cfg },
		func() time.Time { return clock }, nil)
	return &Intake{App: a, AI: fake, Config: func() store.Config { return cfg }}, cfg
}

// writeEml skapar en riktig MIME-.eml med en PDF-bilaga i inkorgen.
func writeEml(t *testing.T, cfg store.Config, name string, pdfData []byte) string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(pdfData)
	raw := "From: mill@ssab.com\r\n" +
		"To: rob@example.se\r\n" +
		"Subject: Certifikat B128293\r\n" +
		"Date: Wed, 01 Jul 2026 08:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"XYZ\"\r\n" +
		"\r\n" +
		"--XYZ\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Cert for order B128293.\r\n" +
		"--XYZ\r\n" +
		"Content-Type: application/pdf; name=\"heat_43136.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"heat_43136.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		b64 + "\r\n" +
		"--XYZ--\r\n"
	path := filepath.Join(cfg.InboxDir, name)
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessEmlHappyPath(t *testing.T) {
	fake := &fakeAI{classifyYes: true, verifyYes: true}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	pdf := []byte("%PDF-1.4 heat 43136\n")
	emlPath := writeEml(t, cfg, "cert.eml", pdf)
	emlHash := fileSHA256(emlPath) // före processning — filen raderas vid succé

	// Förbered en orderrad som matchar B-numret i ämnesraden
	if err := in.App.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 7, OrderNumber: "B128293", PartNumber: "30-101241-001",
	}, "t"); err != nil {
		t.Fatal(err)
	}

	in.ProcessInboxOnce(ctx)

	certs, err := in.App.Repo.ListCerts(ctx, domain.CertMottagen)
	if err != nil || len(certs) != 1 {
		t.Fatalf("cert-rader = %d (%v)", len(certs), err)
	}
	c := certs[0]
	if c.OriginalFilename != "heat_43136.pdf" || c.Charge != "43136" {
		t.Errorf("cert: %+v", c)
	}
	if c.IsLegible != false || c.IsUnaltered != true || c.NormSystem != "Charpy" {
		t.Errorf("v2-fält gick inte hela vägen genom pipelinen: %+v", c)
	}
	if c.TokensInput != 1234 {
		t.Errorf("riktiga tokental ska sparas: tokens_input = %d", c.TokensInput)
	}
	if len(c.BNumbers) != 1 || c.BNumbers[0] != "B128293" {
		t.Errorf("B-nummer ur mejlet: %v", c.BNumbers)
	}

	// Lagerfil + sidecar på disk
	storePath := store.StorePath(cfg, c.StoredName)
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("lagerfil saknas: %v", err)
	}
	if _, err := os.Stat(store.MetaSidecarPath(storePath)); err != nil {
		t.Errorf("sidecar saknas: %v", err)
	}

	// Länkförslag mot orderraden
	links, _ := in.App.Repo.ListLinksForCert(ctx, c.ID)
	if len(links) != 1 || links[0].Status != domain.LinkForeslagen || links[0].DeliveryRowID != 7 {
		t.Errorf("länkförslag: %+v", links)
	}

	// .eml borta, email-rad completed
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Error(".eml ska tas bort efter lyckad körning")
	}
	if s := in.App.LatestEmailStatus(ctx, "cert.eml", emlHash); s != "completed" {
		t.Errorf("emailstatus = %q", s)
	}
}

func TestDuplicateIngestIsNoop(t *testing.T) {
	fake := &fakeAI{classifyYes: true, verifyYes: true}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	pdf := []byte("%PDF-1.4 samma cert\n")
	writeEml(t, cfg, "first.eml", pdf)
	in.ProcessInboxOnce(ctx)
	secondPath := writeEml(t, cfg, "second.eml", pdf) // exakt samma PDF igen
	secondHash := fileSHA256(secondPath)
	in.ProcessInboxOnce(ctx)

	certs, _ := in.App.Repo.ListCerts(ctx, "")
	if len(certs) != 1 {
		t.Errorf("dublett skapade extra rad: %d certs", len(certs))
	}
	// Båda mejlen slutade lyckat (dubletten är en ren no-op)
	if s := in.App.LatestEmailStatus(ctx, "second.eml", secondHash); s != "completed" {
		t.Errorf("dublettmejl status = %q", s)
	}
}

func TestNonCertCategoryIsDeleted(t *testing.T) {
	fake := &fakeAI{category: ai.CategoryInvoice}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	emlPath := writeEml(t, cfg, "faktura.eml", []byte("%PDF fake"))
	emlHash := fileSHA256(emlPath)
	in.ProcessInboxOnce(ctx)

	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Error("icke-cert ska tas bort")
	}
	certs, _ := in.App.Repo.ListCerts(ctx, "")
	if len(certs) != 0 {
		t.Errorf("icke-cert skapade cert-rad: %d", len(certs))
	}
	if fake.extractCalls != 0 {
		t.Error("extraktion ska inte köras för icke-cert")
	}
	if s := in.App.LatestEmailStatus(ctx, "faktura.eml", emlHash); s != "archived" {
		t.Errorf("status = %q", s)
	}
}

func TestExtractErrorKeepsFileAndSkipsRetry(t *testing.T) {
	fake := &fakeAI{classifyYes: true, verifyYes: true, extractErr: errors.New("claude 500")}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	emlPath := writeEml(t, cfg, "trasig.eml", []byte("%PDF fake"))
	emlHash := fileSHA256(emlPath)
	in.ProcessInboxOnce(ctx)

	if _, err := os.Stat(emlPath); err != nil {
		t.Error("felad .eml ska ligga kvar i inkorgen")
	}
	if s := in.App.LatestEmailStatus(ctx, "trasig.eml", emlHash); s != "error" {
		t.Errorf("status = %q", s)
	}

	// Nästa tick: skip-logiken hindrar AI-loop
	calls := fake.extractCalls
	in.ProcessInboxOnce(ctx)
	if fake.extractCalls != calls {
		t.Error("felad fil omprocessades — bränner AI-anrop i loop")
	}
}

// En färdigbehandlad .eml som inte kan raderas (t.ex. skrivskyddad på Windows)
// ska stämplas error så skip-logiken tar den — annars körs hela AI-pipelinen
// om var 30:e sekund för alltid. En redan försvunnen fil är däremot inget fel.
func TestUnremovableEmlIsMarkedError(t *testing.T) {
	fake := &fakeAI{classifyYes: true, verifyYes: true}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	// En "fil" som inte kan raderas: en icke-tom katalog felar även som root.
	locked := filepath.Join(cfg.InboxDir, "låst.eml")
	if err := os.MkdirAll(filepath.Join(locked, "x"), 0755); err != nil {
		t.Fatal(err)
	}
	id := in.App.EmailStarted(ctx, "låst.eml", "")
	in.App.EmailFinished(ctx, id, "completed", "")
	in.removeEml(ctx, id, locked)
	if s := in.App.LatestEmailStatus(ctx, "låst.eml", ""); s != "error" {
		t.Errorf("oraderbar fil: status = %q, vill ha error", s)
	}

	// Redan borta = ofarligt, statusen ska stå kvar.
	id2 := in.App.EmailStarted(ctx, "borta.eml", "")
	in.App.EmailFinished(ctx, id2, "completed", "")
	in.removeEml(ctx, id2, filepath.Join(cfg.InboxDir, "borta.eml"))
	if s := in.App.LatestEmailStatus(ctx, "borta.eml", ""); s != "completed" {
		t.Errorf("redan raderad fil: status = %q, vill ha completed", s)
	}
}

func TestVerifyConflictBecomesError(t *testing.T) {
	// classify säger ja, verify säger nej → fel-rad, filen kvar
	fake := &fakeAI{classifyYes: true, verifyYes: false}
	in, cfg := testIntake(t, fake)
	ctx := context.Background()

	emlPath := writeEml(t, cfg, "oense.eml", []byte("%PDF fake"))
	emlHash := fileSHA256(emlPath)
	in.ProcessInboxOnce(ctx)

	if _, err := os.Stat(emlPath); err != nil {
		t.Error("oense-mejl ska ligga kvar")
	}
	if s := in.App.LatestEmailStatus(ctx, "oense.eml", emlHash); s != "error" {
		t.Errorf("status = %q", s)
	}

	// classify nej + verify nej → arkiv (radera)
	fake2 := &fakeAI{classifyYes: false, verifyYes: false}
	in2, cfg2 := testIntake(t, fake2)
	emlPath2 := writeEml(t, cfg2, "reklam.eml", []byte("%PDF fake"))
	in2.ProcessInboxOnce(ctx)
	if _, err := os.Stat(emlPath2); !os.IsNotExist(err) {
		t.Error("eniga nej ska raderas")
	}
}
