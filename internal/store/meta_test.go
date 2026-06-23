package store

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_EmbedMetadata_SidecarFallback_OnInvalidPdf(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(pdfPath, []byte("inte en riktig pdf"), 0644); err != nil {
		t.Fatal(err)
	}
	meta := PdfMeta{Charge: "C123", Material: "S355", Schema: 4}
	if err := EmbedMetadata(pdfPath, meta); err != nil {
		t.Fatalf("EmbedMetadata ska falla tillbaka på sidecar utan fel, fick: %v", err)
	}
	if _, err := os.Stat(MetaSidecarPath(pdfPath)); err != nil {
		t.Fatalf("sidecar ska finnas: %v", err)
	}
	got, ok := ReadMetadata(pdfPath)
	if !ok {
		t.Fatal("ReadMetadata ska hitta sidecar")
	}
	if got.Charge != "C123" || got.Material != "S355" {
		t.Fatalf("fel innehåll: %+v", got)
	}
}

func Test_ArchiveQueueItem_MovesPdfAndSidecar(t *testing.T) {
	root := t.TempDir()
	cfg := Config{InboxDir: root}
	if err := os.MkdirAll(QueueDir(cfg), 0755); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(QueueDir(cfg), "dup.pdf")
	if err := os.WriteFile(pdf, []byte("pdfdata"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf+".json", []byte(`{"charge":"X"}`), 0644); err != nil {
		t.Fatal(err)
	}
	dst, err := ArchiveQueueItem(cfg, "dup.pdf")
	if err != nil {
		t.Fatalf("ArchiveQueueItem: %v", err)
	}
	if filepath.Dir(dst) != ArkiveratDir(cfg) {
		t.Fatalf("dst ska ligga i arkiverat/, fick %s", dst)
	}
	if _, err := os.Stat(pdf); !os.IsNotExist(err) {
		t.Fatalf("queue-pdf ska vara borta, fick err=%v", err)
	}
	if _, err := os.Stat(dst + ".json"); err != nil {
		t.Fatalf("sidecar ska ha följt med: %v", err)
	}
}
