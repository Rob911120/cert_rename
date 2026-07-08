package intake

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

func testDeliveryIntake(t *testing.T, fake *fakeAI) (*Intake, *app.App, store.Config) {
	t.Helper()
	dir := t.TempDir()
	deliveryDir := filepath.Join(dir, "foljesedlar")
	if err := os.MkdirAll(deliveryDir, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := store.Config{InboxDir: dir, DeliveryInboxDir: deliveryDir}
	clock := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	a := app.New(store.NewRepository(db), func() store.Config { return cfg },
		func() time.Time { return clock }, nil)
	return &Intake{App: a, AI: fake, Config: func() store.Config { return cfg }}, a, cfg
}

// writeImageEml skapar en .eml med en JPEG-bilaga i följesedel-mappen.
func writeImageEml(t *testing.T, cfg store.Config, name string, img []byte) string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(img)
	raw := "From: rob@example.se\r\n" +
		"To: rob@example.se\r\n" +
		"Subject: Foljesedel\r\n" +
		"Date: Wed, 01 Jul 2026 08:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"XYZ\"\r\n" +
		"\r\n" +
		"--XYZ\r\n" +
		"Content-Type: image/jpeg; name=\"foljesedel.jpg\"\r\n" +
		"Content-Disposition: attachment; filename=\"foljesedel.jpg\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		b64 + "\r\n" +
		"--XYZ--\r\n"
	path := filepath.Join(cfg.DeliveryInboxDir, name)
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessDeliveryEml_CreatesRowAndRemovesEml(t *testing.T) {
	// fakeAI.ExtractFromImage default returnerar order B127562.
	in, a, cfg := testDeliveryIntake(t, &fakeAI{})
	ctx := context.Background()

	emlPath := writeImageEml(t, cfg, "foljesedel.eml", []byte("\xff\xd8\xff jpeg"))
	in.ProcessDeliveryOnce(ctx)

	notes, err := a.ListDeliveryNotes(ctx, domain.DNMottagen)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("förväntade 1 följesedel, fick %d", len(notes))
	}
	if notes[0].OrderNumber != "B127562" {
		t.Errorf("ordernummer = %q", notes[0].OrderNumber)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Error("färdigbehandlad .eml ska ha raderats")
	}
}

func TestProcessDeliveryEml_NoImageArchives(t *testing.T) {
	in, a, cfg := testDeliveryIntake(t, &fakeAI{})
	ctx := context.Background()
	// En .eml utan bild-bilaga (bara text) → ingen följesedel skapas.
	raw := "From: rob@example.se\r\nTo: rob@example.se\r\nSubject: tom\r\n" +
		"Date: Wed, 01 Jul 2026 08:00:00 +0000\r\nContent-Type: text/plain\r\n\r\ningen bild\r\n"
	if err := os.WriteFile(filepath.Join(cfg.DeliveryInboxDir, "tom.eml"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	in.ProcessDeliveryOnce(ctx)
	notes, _ := a.ListDeliveryNotes(ctx, domain.DNMottagen)
	if len(notes) != 0 {
		t.Errorf("bildlöst mejl ska inte skapa följesedel, fick %d", len(notes))
	}
}
