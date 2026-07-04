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
