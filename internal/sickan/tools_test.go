package sickan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cert-renamer/internal/store"
)

// stubNotifier är en no-op Notifier för tester.
type stubNotifier struct{ logs int }

func (s *stubNotifier) Logf(string, ...any) { s.logs++ }
func (s *stubNotifier) BroadcastQueue()     {}
func (s *stubNotifier) BroadcastReview()    {}
func (s *stubNotifier) BroadcastStats()     {}

func setupToolbox(t *testing.T) (*Toolbox, store.Config) {
	t.Helper()
	cfg := setupCfg(t)
	for _, d := range []string{store.ReviewDir(cfg), store.ApprovedDir(cfg), store.ArkiveratDir(cfg)} {
		_ = os.MkdirAll(d, 0755)
	}
	return &Toolbox{Cfg: cfg, N: &stubNotifier{}}, cfg
}

func writeFakePDF(t *testing.T, dir, name string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("%PDF-1.4 fake\n"), 0644); err != nil {
		t.Fatalf("skriv %s: %v", name, err)
	}
	return full
}

func Test_Dispatch_ListQueue(t *testing.T) {
	tb, cfg := setupToolbox(t)
	writeFakePDF(t, store.QueueDir(cfg), "a.pdf")
	writeFakePDF(t, store.QueueDir(cfg), "b.pdf")
	res, err := tb.Dispatch("list_queue", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var resp struct {
		Count int                `json:"count"`
		Items []store.QueueItem  `json:"items"`
	}
	if err := json.Unmarshal([]byte(res.Summary), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, res.Summary)
	}
	if resp.Count != 2 {
		t.Errorf("count=%d (svar: %s)", resp.Count, res.Summary)
	}
}

func Test_Dispatch_ApplyOrder_WritesFile(t *testing.T) {
	tb, cfg := setupToolbox(t)
	writeFakePDF(t, store.QueueDir(cfg), "a.pdf")
	writeFakePDF(t, store.QueueDir(cfg), "b.pdf")
	in := json.RawMessage(`{"filenames":["b.pdf","a.pdf"]}`)
	res, err := tb.Dispatch("apply_queue_order", in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !contains(res.Summary, `"applied":2`) {
		t.Errorf("svar saknar applied:2: %s", res.Summary)
	}
	got := LoadOrder(cfg)
	if len(got) != 2 || got[0] != "b.pdf" {
		t.Errorf("order på disk: %v", got)
	}
	// Apply på listQueue ska sortera om
	items := readQueue(cfg)
	sorted := Apply(cfg, items)
	if sorted[0].Filename != "b.pdf" {
		t.Errorf("Apply sorterade inte: %+v", sorted)
	}
}

func Test_Dispatch_Approve_MovesFile(t *testing.T) {
	tb, cfg := setupToolbox(t)
	writeFakePDF(t, store.QueueDir(cfg), "a.pdf")
	_ = SaveOrder(cfg, []string{"a.pdf", "b.pdf"})
	res, err := tb.Dispatch("approve_queue_item", json.RawMessage(`{"filename":"a.pdf"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !contains(res.Summary, `"ok":true`) {
		t.Errorf("svar: %s", res.Summary)
	}
	if _, err := os.Stat(filepath.Join(store.QueueDir(cfg), "a.pdf")); !os.IsNotExist(err) {
		t.Errorf("a.pdf borde flyttats från queue")
	}
	if _, err := os.Stat(filepath.Join(store.ApprovedDir(cfg), "a.pdf")); err != nil {
		t.Errorf("a.pdf borde finnas i approved: %v", err)
	}
	if order := LoadOrder(cfg); len(order) != 1 || order[0] != "b.pdf" {
		t.Errorf("a.pdf borde tagits ur ordningen, fick: %v", order)
	}
}

func Test_Dispatch_AnalyzeReview_ReadsEmlAndReason(t *testing.T) {
	tb, cfg := setupToolbox(t)
	base := "test_msg"
	dir := filepath.Join(store.ReviewDir(cfg), base)
	_ = os.MkdirAll(dir, 0755)
	emlBody := "Subject: testämne\r\nFrom: test@example.com\r\nDate: Mon, 1 Jan 2024 12:00:00 +0000\r\n\r\nhej hej"
	_ = os.WriteFile(filepath.Join(dir, "msg.eml"), []byte(emlBody), 0644)
	_ = os.WriteFile(filepath.Join(dir, "_reason.txt"), []byte("test-reason\n"), 0644)
	res, err := tb.Dispatch("analyze_review_item", json.RawMessage(`{"base":"`+base+`"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !contains(res.Summary, "test-reason") {
		t.Errorf("reason saknas: %s", res.Summary)
	}
	if !contains(res.Summary, "testämne") {
		t.Errorf("subject saknas: %s", res.Summary)
	}
}

func Test_Dispatch_AnalyzeReview_RejectsBadBase(t *testing.T) {
	tb, _ := setupToolbox(t)
	if _, err := tb.Dispatch("analyze_review_item", json.RawMessage(`{"base":"../etc"}`)); err == nil {
		t.Errorf("borde rejecta path-traversal")
	}
}

func Test_Dispatch_PromoteReview_RejectsValidationFail(t *testing.T) {
	tb, cfg := setupToolbox(t)
	base := "promote_test"
	dir := filepath.Join(store.ReviewDir(cfg), base)
	_ = os.MkdirAll(dir, 0755)
	writeFakePDF(t, dir, "doc.pdf")
	// charge saknas → cert.Validate ska säga ifrån
	in := json.RawMessage(`{"base":"promote_test","pdf_filename":"doc.pdf","charge":"","material":"S355","product_form":"plåt","dimensions":"10","b_numbers":["B1"]}`)
	if _, err := tb.Dispatch("promote_review_to_queue", in); err == nil {
		t.Errorf("borde fail när charge saknas")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("review-mapp borde ligga kvar: %v", err)
	}
}

func Test_Dispatch_PromoteReview_RejectsBadBase(t *testing.T) {
	tb, _ := setupToolbox(t)
	in := json.RawMessage(`{"base":"../etc","pdf_filename":"x.pdf","charge":"1","material":"S","product_form":"p","dimensions":"1","b_numbers":["B1"]}`)
	if _, err := tb.Dispatch("promote_review_to_queue", in); err == nil {
		t.Errorf("borde rejecta path-traversal")
	}
}

func Test_Dispatch_UnknownTool(t *testing.T) {
	tb, _ := setupToolbox(t)
	if _, err := tb.Dispatch("some_unknown", json.RawMessage(`{}`)); err == nil {
		t.Errorf("borde vara fel för okänt tool")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && stringIndex(haystack, needle) >= 0
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
