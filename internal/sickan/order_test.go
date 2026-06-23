package sickan

import (
	"os"
	"path/filepath"
	"testing"

	"cert-renamer/internal/store"
)

func setupCfg(t *testing.T) store.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := store.Config{InboxDir: dir}
	if err := os.MkdirAll(store.QueueDir(cfg), 0755); err != nil {
		t.Fatalf("mkdir queue: %v", err)
	}
	return cfg
}

func Test_OrderRoundTrip(t *testing.T) {
	cfg := setupCfg(t)
	want := []string{"a.pdf", "b.pdf", "c.pdf"}
	if err := SaveOrder(cfg, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadOrder(cfg)
	if len(got) != 3 || got[0] != "a.pdf" || got[2] != "c.pdf" {
		t.Errorf("load mismatch: %v", got)
	}
}

func Test_ApplySortsKnownItemsFirst(t *testing.T) {
	cfg := setupCfg(t)
	_ = SaveOrder(cfg, []string{"c.pdf", "a.pdf"})
	in := []store.QueueItem{
		{Filename: "a.pdf"}, {Filename: "b.pdf"}, {Filename: "c.pdf"}, {Filename: "d.pdf"},
	}
	out := Apply(cfg, in)
	names := []string{out[0].Filename, out[1].Filename, out[2].Filename, out[3].Filename}
	want := []string{"c.pdf", "a.pdf", "b.pdf", "d.pdf"}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("pos %d: vill %q, fick %q (alla: %v)", i, n, names[i], names)
		}
	}
}

func Test_ApplyNoOrderReturnsInput(t *testing.T) {
	cfg := setupCfg(t)
	in := []store.QueueItem{{Filename: "x.pdf"}, {Filename: "y.pdf"}}
	out := Apply(cfg, in)
	if len(out) != 2 || out[0].Filename != "x.pdf" {
		t.Errorf("oväntad ordning: %v", out)
	}
}

func Test_RenameAndRemoveInOrder(t *testing.T) {
	cfg := setupCfg(t)
	_ = SaveOrder(cfg, []string{"old.pdf", "keep.pdf"})
	RenameInOrder(cfg, "old.pdf", "new.pdf")
	got := LoadOrder(cfg)
	if got[0] != "new.pdf" {
		t.Errorf("efter rename, fick: %v", got)
	}
	RemoveFromOrder(cfg, "keep.pdf")
	got = LoadOrder(cfg)
	if len(got) != 1 || got[0] != "new.pdf" {
		t.Errorf("efter remove, fick: %v", got)
	}
}

func Test_OrderFileLivesInQueueDir(t *testing.T) {
	cfg := setupCfg(t)
	_ = SaveOrder(cfg, []string{"a.pdf"})
	expected := filepath.Join(store.QueueDir(cfg), ".sickan_order.json")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("ordningsfil saknas på %s: %v", expected, err)
	}
}
