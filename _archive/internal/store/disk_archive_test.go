package store

// Fryst V1-test — flyttat hit ur internal/store/meta_test.go när V1 arkiverades.
// Testar ArchiveQueueItem (V1:s disk.go). Kompileras aldrig: Config/QueueDir/
// ArkiveratDir bor numera i internal/v2/store. Behållet enbart som historik.

import (
	"os"
	"path/filepath"
	"testing"
)

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
