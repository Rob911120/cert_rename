package worker

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	"cert-renamer/internal/store"
)

// stubMap maps tool name → response body. A value with prefix "STATUS:<code>"
// makes the stub return that HTTP status instead of a JSON body.
type stubMap map[string]string

// Stubbade JSON-svar — minst de fält cert-renamer-koden läser. SDK-decodern
// är icke-strikt så övriga `api:"required"`-fält kan utelämnas.
const (
	classifyYesJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "classify_email",
			"input": {"is_cert_mail": true, "confidence": "high", "reason": "test cert"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`

	classifyNoJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "classify_email",
			"input": {"is_cert_mail": false, "confidence": "high", "reason": "inte cert"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`

	verifyYesJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "verify_pdfs",
			"input": {"any_is_cert": true, "reason": "PDF är cert"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 50, "output_tokens": 5}
	}`

	verifyNoJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "verify_pdfs",
			"input": {"any_is_cert": false, "reason": "ingen är cert"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 50, "output_tokens": 5}
	}`

	extractValidJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "submit_extraction",
			"input": {
				"is_en10204_3_1": true,
				"cert_type": "3.1",
				"charge": "610042",
				"material": "1.4307",
				"material_short": "1.4307",
				"product_form": "rundstång",
				"dimensions": "5",
				"confidence": "high",
				"issues": []
			}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 100, "output_tokens": 20}
	}`

	extractUnknownJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "submit_extraction",
			"input": {
				"is_en10204_3_1": false,
				"cert_type": "unknown",
				"charge": "",
				"material": "",
				"material_short": "",
				"product_form": "",
				"dimensions": "",
				"confidence": "low",
				"issues": ["okänd dokumenttyp"]
			}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 100, "output_tokens": 20}
	}`
)

// testNotifier är en no-op Notifier som testerna använder istället för en
// riktig Server. Räknar IncrementOK-anrop ifall vi vill assertera mot dem.
type testNotifier struct {
	okCount int
}

func (n *testNotifier) Logf(format string, args ...any)                    {}
func (n *testNotifier) RecordUsage(model string, in, out, cc, cr int64)    {}
func (n *testNotifier) IncrementOK()                                        { n.okCount++ }
func (n *testNotifier) BroadcastStats()                                     {}
func (n *testNotifier) BroadcastQueue()                                     {}
func (n *testNotifier) BroadcastReview()                                    {}

// startStubAnthropic returnerar en httptest-server som svarar på POST
// /v1/messages baserat på request-bodyns tool_choice.name (eller tools[0].name
// som fallback). Värde "STATUS:<code>" gör att stub:en returnerar den HTTP-koden.
func startStubAnthropic(t *testing.T, stubs stubMap) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("stub: read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var req struct {
			ToolChoice struct {
				Name string `json:"name"`
			} `json:"tool_choice"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("stub: unmarshal req: %v", err)
			http.Error(w, "unmarshal", http.StatusInternalServerError)
			return
		}
		toolName := req.ToolChoice.Name
		if toolName == "" && len(req.Tools) > 0 {
			toolName = req.Tools[0].Name
		}
		resp, ok := stubs[toolName]
		if !ok {
			t.Errorf("stub: ingen response för tool %q", toolName)
			http.Error(w, "no stub", http.StatusInternalServerError)
			return
		}
		if strings.HasPrefix(resp, "STATUS:") {
			code, _ := strconv.Atoi(strings.TrimPrefix(resp, "STATUS:"))
			http.Error(w, "stubbed error", code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makeTestClient bygger en anthropic.Client som pekar på stub-servern.
// Retries stängs av så att tester med 500-svar inte fördröjs.
func makeTestClient(stub *httptest.Server) *anthropic.Client {
	c := anthropic.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	return &c
}

// setupTestInbox skapar en isolerad temp-inbox med queue/review/arkiverat/approved
// och returnerar Config + Notifier-stub. Loggar dirigeras till io.Discard.
func setupTestInbox(t *testing.T) (store.Config, *testNotifier) {
	t.Helper()
	dir := t.TempDir()
	cfg := store.Config{InboxDir: dir}
	for _, d := range []string{store.QueueDir(cfg), store.ReviewDir(cfg), store.ApprovedDir(cfg), store.ArkiveratDir(cfg)} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	prevOutput := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prevOutput) })

	return cfg, &testNotifier{}
}

// copyFixture kopierar en fixture från testdata/ till inbox-roten och
// returnerar den nya sökvägen.
func copyFixture(t *testing.T, cfg store.Config, fixtureName string) string {
	t.Helper()
	src := filepath.Join("testdata", fixtureName)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("läs fixture %s: %v", src, err)
	}
	dst := filepath.Join(cfg.InboxDir, fixtureName)
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("skriv fixture %s: %v", dst, err)
	}
	return dst
}

// countFiles räknar filer (ej kataloger) i dir.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// listSubdirs returnerar namnen på alla underkataloger i dir.
func listSubdirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// readReason läser _reason.txt från dir om den finns.
func readReason(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "_reason.txt"))
	if err != nil {
		t.Fatalf("läs reason i %s: %v", dir, err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// Tester
// ---------------------------------------------------------------------------

func Test_HappyPath_OnePdfValidCert(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	if n := countFiles(t, store.QueueDir(cfg)); n < 1 {
		t.Fatalf("queue: förväntade ≥1 PDF, fick %d", n)
	}
	entries, _ := os.ReadDir(store.QueueDir(cfg))
	var foundMatch bool
	for _, e := range entries {
		// förväntat mönster: 610042-rundstang-5-1.4307-B127196.pdf
		if strings.HasPrefix(e.Name(), "610042-rundstang-5-1.4307-") && strings.HasSuffix(e.Name(), ".pdf") {
			foundMatch = true
			break
		}
	}
	if !foundMatch {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("inget queue-namn matchar 610042-rundstang-5-1.4307-*.pdf, fick: %v", names)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort vid happy path, men finns kvar")
	}
}

func Test_HappyPath_ZipAttachmentUnpacked(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_zip.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	if n := countFiles(t, store.QueueDir(cfg)); n < 1 {
		t.Fatalf("queue: förväntade ≥1 PDF (uppackad ur zip), fick %d", n)
	}
	entries, _ := os.ReadDir(store.QueueDir(cfg))
	var foundMatch bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "610042-rundstang-5-1.4307-") && strings.HasSuffix(e.Name(), ".pdf") {
			foundMatch = true
			break
		}
	}
	if !foundMatch {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("inget queue-namn matchar 610042-rundstang-5-1.4307-*.pdf, fick: %v", names)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort vid happy path, men finns kvar")
	}
}

func Test_NoAttachments_GoesToArkiverat(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{}) // inga AI-anrop förväntas
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "no_pdf_marketing.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	subs := listSubdirs(t, store.ArkiveratDir(cfg))
	if len(subs) != 1 {
		t.Fatalf("arkiverat: förväntade 1 undermapp, fick %d (%v)", len(subs), subs)
	}
	reason := readReason(t, filepath.Join(store.ArkiveratDir(cfg), subs[0]))
	if !strings.Contains(reason, "inga PDF-bilagor") {
		t.Errorf("reason saknar 'inga PDF-bilagor': %q", reason)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort efter arkivering, men finns kvar")
	}
}

func Test_ClassifyYesVerifyNo_GoesToReview(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email": classifyYesJSON,
		"verify_pdfs":    verifyNoJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_kontrollrapport.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	subs := listSubdirs(t, store.ReviewDir(cfg))
	if len(subs) != 1 {
		t.Fatalf("review: förväntade 1 undermapp, fick %d (%v)", len(subs), subs)
	}
	reason := readReason(t, filepath.Join(store.ReviewDir(cfg), subs[0]))
	if !strings.Contains(reason, "inte ett cert-mejl") {
		t.Errorf("reason saknar 'inte ett cert-mejl': %q", reason)
	}
	if n := countFiles(t, store.QueueDir(cfg)); n != 0 {
		t.Errorf("queue ska vara tom, fick %d filer", n)
	}
	if subs := listSubdirs(t, store.ArkiveratDir(cfg)); len(subs) != 0 {
		t.Errorf("arkiverat ska vara tom, fick %v", subs)
	}
}

func Test_DoubleNo_GoesToArkiverat(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email": classifyNoJSON,
		"verify_pdfs":    verifyNoJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	subs := listSubdirs(t, store.ArkiveratDir(cfg))
	if len(subs) != 1 {
		t.Fatalf("arkiverat: förväntade 1 undermapp, fick %d (%v)", len(subs), subs)
	}
	if subs := listSubdirs(t, store.ReviewDir(cfg)); len(subs) != 0 {
		t.Errorf("review ska vara tom, fick %v", subs)
	}
	if n := countFiles(t, store.QueueDir(cfg)); n != 0 {
		t.Errorf("queue ska vara tom, fick %d filer", n)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort efter arkivering")
	}
}

func Test_TrapClassifyNoVerifyYes(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyNoJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	if n := countFiles(t, store.QueueDir(cfg)); n < 1 {
		t.Fatalf("queue: förväntade ≥1 PDF (trap-fall), fick %d", n)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort vid lyckad extraktion")
	}
}

func Test_ExtractionFails_TypeUnknown(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractUnknownJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	subs := listSubdirs(t, store.ReviewDir(cfg))
	if len(subs) < 1 {
		t.Fatalf("review: förväntade ≥1 undermapp, fick %d", len(subs))
	}
	if n := countFiles(t, store.QueueDir(cfg)); n != 0 {
		t.Errorf("queue ska vara tom när extraktion misslyckas, fick %d", n)
	}
	// .eml flyttas till review-roten vid anyFail
	emlInReview := filepath.Join(store.ReviewDir(cfg), filepath.Base(emlPath))
	if _, err := os.Stat(emlInReview); err != nil {
		t.Errorf(".eml borde flyttas till review/ vid validateringsfel: %v", err)
	}
}

func Test_VerifyApiError_FallsThroughToSonnet(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       "STATUS:500",
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	if n := countFiles(t, store.QueueDir(cfg)); n < 1 {
		t.Fatalf("queue: förväntade ≥1 PDF (verify-error fall-through), fick %d", n)
	}
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort vid lyckad fall-through")
	}
}

func Test_ParseError_GoesToReview(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{}) // inga AI-anrop förväntas
	client := makeTestClient(stub)

	// skriv en korrupt .eml direkt
	emlPath := filepath.Join(cfg.InboxDir, "korrupt.eml")
	if err := os.WriteFile(emlPath, []byte("not a valid email\x00\x01\x02"), 0644); err != nil {
		t.Fatalf("skriv korrupt eml: %v", err)
	}

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	subs := listSubdirs(t, store.ReviewDir(cfg))
	if len(subs) != 1 {
		t.Fatalf("review: förväntade 1 undermapp, fick %d (%v)", len(subs), subs)
	}
	reason := readReason(t, filepath.Join(store.ReviewDir(cfg), subs[0]))
	if !strings.Contains(reason, "parse error") {
		t.Errorf("reason saknar 'parse error': %q", reason)
	}
}

func Test_Extraction_ProductForm_PropagatesToMetadata(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	entries, err := os.ReadDir(store.QueueDir(cfg))
	if err != nil {
		t.Fatalf("readdir queue: %v", err)
	}
	var pdfPath string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pdf") {
			pdfPath = filepath.Join(store.QueueDir(cfg), e.Name())
			break
		}
	}
	if pdfPath == "" {
		t.Fatalf("hittade ingen PDF i queue/")
	}
	if !strings.Contains(filepath.Base(pdfPath), "rundstang") {
		t.Errorf("filnamn ska innehålla foldat form-segment 'rundstang', fick: %s", filepath.Base(pdfPath))
	}
	m, ok := store.ReadMetadata(pdfPath)
	if !ok {
		t.Fatalf("kunde inte läsa metadata från %s", pdfPath)
	}
	if m.ProductForm != "rundstång" {
		t.Errorf("ProductForm i metadata ska vara 'rundstång' (oförändrad), fick: %q", m.ProductForm)
	}
}

// setupReviewItem skapar review/<base>/ med en riktig PDF (extraherad från
// cert_with_pdf.eml-fixturen) + valfri .eml. Returnerar (base, pdfFilename).
func setupReviewItem(t *testing.T, cfg store.Config, base string, includeEml bool) (string, string) {
	t.Helper()
	src := filepath.Join("testdata", "cert_with_pdf.eml")
	c, err := eml.Parse(src)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(c.Attachments) == 0 {
		t.Fatalf("fixturen saknar PDF-bilagor")
	}
	att := c.Attachments[0]
	dir := filepath.Join(store.ReviewDir(cfg), base)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pdfPath := filepath.Join(dir, att.Filename)
	if err := os.WriteFile(pdfPath, att.Data, 0644); err != nil {
		t.Fatalf("skriv pdf: %v", err)
	}
	if includeEml {
		emlData, _ := os.ReadFile(src)
		_ = os.WriteFile(filepath.Join(dir, "msg.eml"), emlData, 0644)
	}
	_ = os.WriteFile(filepath.Join(dir, "_reason.txt"), []byte("test\n"), 0644)
	return base, att.Filename
}

func Test_PromoteReviewToQueue_HappyPath(t *testing.T) {
	cfg, _ := setupTestInbox(t)
	base, pdfName := setupReviewItem(t, cfg, "test_promote", true)

	ext := &cert.Extraction{
		IsEN10204_3_1: true,
		CertType:      "3.1",
		Charge:        "999111",
		MaterialShort: "S355",
		ProductForm:   "rundstång",
		Dimensions:    "20",
		Confidence:    "high",
	}
	newName, err := store.PromoteReviewToQueue(cfg, base, pdfName, ext, []string{"B999"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !strings.HasPrefix(newName, "999111-rundstang-20-S355-B999") {
		t.Errorf("oväntat filnamn: %s", newName)
	}
	if _, err := os.Stat(filepath.Join(store.QueueDir(cfg), newName)); err != nil {
		t.Errorf("queue-fil saknas: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.ReviewDir(cfg), base)); !os.IsNotExist(err) {
		t.Errorf("review-mapp borde tagits bort, fick err=%v", err)
	}
	m, ok := store.ReadMetadata(filepath.Join(store.QueueDir(cfg), newName))
	if !ok {
		t.Fatalf("kunde inte läsa metadata")
	}
	if m.Status != "queue" {
		t.Errorf("Status=%q, vill ha 'queue'", m.Status)
	}
	if m.Charge != "999111" || m.Material != "S355" {
		t.Errorf("metadata-fält: %+v", m)
	}
	if m.EmailRaw == "" {
		t.Errorf("EmailRaw borde vara satt")
	}
}

func Test_PromoteReviewToQueue_ValidationFails(t *testing.T) {
	cfg, _ := setupTestInbox(t)
	base, pdfName := setupReviewItem(t, cfg, "test_validation", true)

	ext := &cert.Extraction{
		IsEN10204_3_1: true,
		CertType:      "3.1",
		Charge:        "", // tomt → validate-fail
		MaterialShort: "S355",
		ProductForm:   "rundstång",
		Dimensions:    "20",
		Confidence:    "high",
	}
	if _, err := store.PromoteReviewToQueue(cfg, base, pdfName, ext, []string{"B1"}); err == nil {
		t.Fatalf("borde fail när charge saknas")
	}
	if _, err := os.Stat(filepath.Join(store.ReviewDir(cfg), base)); err != nil {
		t.Errorf("review-mapp borde ligga kvar: %v", err)
	}
	if n := countFiles(t, store.QueueDir(cfg)); n != 0 {
		t.Errorf("queue ska vara tom, fick %d", n)
	}
}

func Test_PromoteReviewToQueue_NoEml(t *testing.T) {
	cfg, _ := setupTestInbox(t)
	base, pdfName := setupReviewItem(t, cfg, "test_noeml", false)

	ext := &cert.Extraction{
		IsEN10204_3_1: true,
		CertType:      "3.1",
		Charge:        "777",
		MaterialShort: "S275",
		ProductForm:   "plåt",
		Dimensions:    "10",
		Confidence:    "high",
	}
	newName, err := store.PromoteReviewToQueue(cfg, base, pdfName, ext, []string{"B7"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	m, ok := store.ReadMetadata(filepath.Join(store.QueueDir(cfg), newName))
	if !ok {
		t.Fatalf("kunde inte läsa metadata")
	}
	if m.EmailRaw != "" {
		t.Errorf("EmailRaw borde vara tom utan .eml, fick: %q", m.EmailRaw)
	}
}
