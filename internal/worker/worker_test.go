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
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
	"cert-renamer/internal/monitor"
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
				"en_standard_present": true,
				"product_form": "rundstång",
				"dimensions": "5",
				"country_of_origin": "",
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
				"en_standard_present": false,
				"product_form": "",
				"dimensions": "",
				"country_of_origin": "",
				"confidence": "low",
				"issues": ["okänd dokumenttyp"]
			}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 100, "output_tokens": 20}
	}`

	// classifyCategoryCertJSON routar mejlet in i det befintliga cert-flödet
	// (steg 0 = kategori-klassificering säger "certificate").
	classifyCategoryCertJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "classify_mail_category",
			"input": {"category": "certificate", "confidence": "high", "reason": "cert-mejl"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`

	// classifyCategoryInvoiceJSON: en faktura — ej cert, ej reklam. Ska
	// persisteras i emails med mail_category="invoice" och arkiveras, UTAN
	// att gå genom verify/extract-vägen.
	classifyCategoryInvoiceJSON = `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": [{
			"type": "tool_use",
			"id": "toolu_test",
			"name": "classify_mail_category",
			"input": {"category": "invoice", "confidence": "high", "reason": "faktura"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`
)

// testNotifier är en no-op Notifier som testerna använder istället för en
// riktig Server. Räknar IncrementOK-anrop ifall vi vill assertera mot dem.
type testNotifier struct {
	okCount int
	repo    *store.Repository
	mon     *monitor.Client // nil i de flesta tester
	monErr  error           // returneras av MonitorClient() när mon är nil
}

func (n *testNotifier) Logf(format string, args ...any)                 {}
func (n *testNotifier) RecordUsage(model string, in, out, cc, cr int64) {}
func (n *testNotifier) IncrementOK()                                    { n.okCount++ }
func (n *testNotifier) BroadcastStats()                                 {}
func (n *testNotifier) BroadcastQueue()                                 {}
func (n *testNotifier) BroadcastReview()                                {}
func (n *testNotifier) Repo() *store.Repository                         { return n.repo }
func (n *testNotifier) MonitorClient() (*monitor.Client, error)         { return n.mon, n.monErr }

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
			// Steg 0-kategoriklassificeringen (classify_mail_category) körs för
			// ALLA mejl. Default:a den till "certificate" så att befintliga
			// cert-tester routas in i cert-flödet utan att behöva deklarera den
			// explicit. Tester som vill testa en annan kategori sätter
			// "classify_mail_category" själva i sin stubMap.
			if toolName == "classify_mail_category" {
				resp = classifyCategoryCertJSON
			} else {
				t.Errorf("stub: ingen response för tool %q", toolName)
				http.Error(w, "no stub", http.StatusInternalServerError)
				return
			}
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

	// Initiera testdatabas
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	repo := store.NewRepository(db)

	return cfg, &testNotifier{repo: repo, monErr: errNoMonitorInTest}
}

// errNoMonitorInTest gör att MonitorClient() failar tydligt om något test råkar
// kalla den utan att ha satt en stub-klient.
var errNoMonitorInTest = errTest("ingen Monitor-klient i test")

type errTest string

func (e errTest) Error() string { return string(e) }

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

	// Verifiera att certifikat skrevs till DB
	certs, err := n.repo.ListCertificates("queue")
	if err != nil {
		t.Fatalf("list certs från DB: %v", err)
	}
	if len(certs) == 0 {
		t.Errorf("förväntade minst 1 cert i DB, fick 0")
	}
	// Kontrollera att alla cert har rätt fält
	for i, cert := range certs {
		if cert.Charge != "610042" {
			t.Errorf("cert[%d].Charge = %q, vill ha %q", i, cert.Charge, "610042")
		}
		if cert.Material != "1.4307" {
			t.Errorf("cert[%d].Material = %q, vill ha %q", i, cert.Material, "1.4307")
		}
		if cert.Status != "queue" {
			t.Errorf("cert[%d].Status = %q, vill ha %q", i, cert.Status, "queue")
		}
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
		IsEN10204_3_1:     true,
		CertType:          "3.1",
		Charge:            "999111",
		Material:          "S355",
		EnStandardPresent: true,
		ProductForm:       "rundstång",
		Dimensions:        "20",
		Confidence:        "high",
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
	if m.EmailSubject == "" && m.EmailBody == "" {
		t.Errorf("EmailSubject/EmailBody borde vara satt")
	}
}

func Test_PromoteReviewToQueue_ValidationFails(t *testing.T) {
	cfg, _ := setupTestInbox(t)
	base, pdfName := setupReviewItem(t, cfg, "test_validation", true)

	ext := &cert.Extraction{
		IsEN10204_3_1:     true,
		CertType:          "3.1",
		Charge:            "", // tomt → validate-fail
		Material:          "S355",
		EnStandardPresent: true,
		ProductForm:       "rundstång",
		Dimensions:        "20",
		Confidence:        "high",
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
		IsEN10204_3_1:     true,
		CertType:          "3.1",
		Charge:            "777",
		Material:          "S275",
		EnStandardPresent: true,
		ProductForm:       "plåt",
		Dimensions:        "10",
		Confidence:        "high",
	}
	newName, err := store.PromoteReviewToQueue(cfg, base, pdfName, ext, []string{"B7"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	m, ok := store.ReadMetadata(filepath.Join(store.QueueDir(cfg), newName))
	if !ok {
		t.Fatalf("kunde inte läsa metadata")
	}
	if m.EmailSubject != "" || m.EmailBody != "" {
		t.Errorf("EmailSubject/EmailBody borde vara tom utan .eml, fick: subject=%q body=%q", m.EmailSubject, m.EmailBody)
	}
}

// ---------------------------------------------------------------------------
// Reconciliation-tester
// ---------------------------------------------------------------------------

func Test_ReconcileQueue_AddsFilesystemItems(t *testing.T) {
	cfg, n := setupTestInbox(t)

	// Skapa PDF-fil i queue/ utan DB-post
	src := filepath.Join("testdata", "cert_with_pdf.eml")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("läs fixture: %v", err)
	}
	// Skapa en riktig PDF-fil (vi behöver bara en fil med .pdf-extension)
	// Använd en av testfixturerna om de finns, annars skapa en dummy
	pdfPath := filepath.Join(store.QueueDir(cfg), "test-reconcile.pdf")
	if err := os.WriteFile(pdfPath, data, 0644); err != nil {
		t.Fatalf("skriv dummy pdf: %v", err)
	}

	// Kör ReconcileQueue
	added, removed, err := n.repo.ReconcileQueue(store.QueueDir(cfg))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if added != 1 {
		t.Errorf("förväntade 1 added, fick %d", added)
	}
	if removed != 0 {
		t.Errorf("förväntade 0 removed, fick %d", removed)
	}

	// Verifiera att DB-post skapades
	certs, err := n.repo.ListCertificates("queue")
	if err != nil {
		t.Fatalf("list certs: %v", err)
	}
	if len(certs) != 1 {
		t.Errorf("förväntade 1 cert i DB, fick %d", len(certs))
	}
	if len(certs) > 0 && certs[0].Filename != "test-reconcile.pdf" {
		t.Errorf("förväntade filename=test-reconcile.pdf, fick %s", certs[0].Filename)
	}
}

func Test_ReconcileQueue_RemovesStaleDBEntries(t *testing.T) {
	cfg, n := setupTestInbox(t)

	// Infoga DB-post utan motsvarande fil
	cert := &store.Certificate{
		PDFHash:     "test-hash-stale",
		Filename:    "nonexistent.pdf",
		CertType:    "3.1",
		Charge:      "123456",
		Material:    "S355",
		Confidence:  "high",
		Status:      "queue",
		ExtractedAt: time.Now().Format(time.RFC3339),
		ModelUsed:   "test",
	}
	_, err := n.repo.InsertCertificate(cert)
	if err != nil {
		t.Fatalf("insert cert: %v", err)
	}

	// Verifiera att posten finns
	certs, _ := n.repo.ListCertificates("queue")
	if len(certs) != 1 {
		t.Fatalf("förväntade 1 cert före reconcile, fick %d", len(certs))
	}

	// Kör ReconcileQueue
	added, removed, err := n.repo.ReconcileQueue(store.QueueDir(cfg))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if added != 0 {
		t.Errorf("förväntade 0 added, fick %d", added)
	}
	if removed != 1 {
		t.Errorf("förväntade 1 removed, fick %d", removed)
	}

	// Verifiera att posten togs bort
	certs, _ = n.repo.ListCertificates("queue")
	if len(certs) != 0 {
		t.Errorf("förväntade 0 cert efter reconcile, fick %d", len(certs))
	}
}

func Test_ReconcileQueue_HandlesMixedScenario(t *testing.T) {
	cfg, n := setupTestInbox(t)

	// 1. Infoga DB-post utan fil (stale)
	staleCert := &store.Certificate{
		PDFHash:     "hash-stale",
		Filename:    "stale.pdf",
		CertType:    "3.1",
		Charge:      "111111",
		Material:    "S355",
		Confidence:  "high",
		Status:      "queue",
		ExtractedAt: time.Now().Format(time.RFC3339),
		ModelUsed:   "test",
	}
	n.repo.InsertCertificate(staleCert)

	// 2. Infoga DB-post med fil (ska bevaras)
	okCert := &store.Certificate{
		PDFHash:     "hash-ok",
		Filename:    "ok.pdf",
		CertType:    "3.1",
		Charge:      "222222",
		Material:    "S355",
		Confidence:  "high",
		Status:      "queue",
		ExtractedAt: time.Now().Format(time.RFC3339),
		ModelUsed:   "test",
	}
	n.repo.InsertCertificate(okCert)

	// Skapa filen för ok.pdf
	okPath := filepath.Join(store.QueueDir(cfg), "ok.pdf")
	if err := os.WriteFile(okPath, []byte("fake pdf content"), 0644); err != nil {
		t.Fatalf("skriv ok.pdf: %v", err)
	}

	// 3. Skapa fil utan DB-post (ska läggas till)
	missingPath := filepath.Join(store.QueueDir(cfg), "missing.pdf")
	if err := os.WriteFile(missingPath, []byte("fake pdf content"), 0644); err != nil {
		t.Fatalf("skriv missing.pdf: %v", err)
	}

	// Kör ReconcileQueue
	added, removed, err := n.repo.ReconcileQueue(store.QueueDir(cfg))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if added != 1 {
		t.Errorf("förväntade 1 added (missing.pdf), fick %d", added)
	}
	if removed != 1 {
		t.Errorf("förväntade 1 removed (stale.pdf), fick %d", removed)
	}

	// Verifiera slutresultat
	certs, _ := n.repo.ListCertificates("queue")
	if len(certs) != 2 {
		t.Errorf("förväntade 2 cert (ok.pdf + missing.pdf), fick %d", len(certs))
	}
}

// ---------------------------------------------------------------------------
// Utökade DB-verifieringstester
// ---------------------------------------------------------------------------

func Test_HappyPath_VerifiesDBContent(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	// Verifiera DB-innehåll
	certs, err := n.repo.ListCertificates("queue")
	if err != nil {
		t.Fatalf("list certs: %v", err)
	}
	if len(certs) == 0 {
		t.Fatalf("förväntade minst 1 cert i DB, fick 0")
	}

	// Kontrollera att alla cert har rätt fält
	for i, cert := range certs {
		if cert.Charge != "610042" {
			t.Errorf("cert[%d].Charge = %q, vill ha %q", i, cert.Charge, "610042")
		}
		if cert.Material != "1.4307" {
			t.Errorf("cert[%d].Material = %q, vill ha %q", i, cert.Material, "1.4307")
		}
		if cert.Material != "1.4307" {
			t.Errorf("cert[%d].Material = %q, vill ha %q", i, cert.Material, "1.4307")
		}
		if cert.Status != "queue" {
			t.Errorf("cert[%d].Status = %q, vill ha %q", i, cert.Status, "queue")
		}
		if cert.CertType != "3.1" {
			t.Errorf("cert[%d].CertType = %q, vill ha %q", i, cert.CertType, "3.1")
		}
		if cert.Confidence != "high" {
			t.Errorf("cert[%d].Confidence = %q, vill ha %q", i, cert.Confidence, "high")
		}
		if cert.PDFHash == "" {
			t.Errorf("cert[%d].PDFHash borde vara satt", i)
		}
		if cert.ExtractedAt == "" {
			t.Errorf("cert[%d].ExtractedAt borde vara satt", i)
		}
		if cert.ModelUsed == "" {
			t.Errorf("cert[%d].ModelUsed borde vara satt", i)
		}
	}
}

func Test_Reconciliation_Integration(t *testing.T) {
	cfg, n := setupTestInbox(t)

	// 1. Kör processEml (skapar cert i DB + fil på disk)
	stub := startStubAnthropic(t, stubMap{
		"classify_email":    classifyYesJSON,
		"verify_pdfs":       verifyYesJSON,
		"submit_extraction": extractValidJSON,
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")
	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	// 2. Verifiera att DB och disk är synkade
	certs, _ := n.repo.ListCertificates("queue")
	diskFiles := countFiles(t, store.QueueDir(cfg))
	if len(certs) != diskFiles {
		t.Errorf("DB (%d) och disk (%d) är inte synkade", len(certs), diskFiles)
	}

	// 3. Kör reconciliation (borde inte ändra något)
	added, removed, err := n.repo.ReconcileQueue(store.QueueDir(cfg))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if added != 0 {
		t.Errorf("reconciliation borde inte lägga till något, fick added=%d", added)
	}
	if removed != 0 {
		t.Errorf("reconciliation borde inte ta bort något, fick removed=%d", removed)
	}

	// 4. Verifiera att DB och disk fortfarande är synkade
	certsAfter, _ := n.repo.ListCertificates("queue")
	diskFilesAfter := countFiles(t, store.QueueDir(cfg))
	if len(certsAfter) != diskFilesAfter {
		t.Errorf("DB (%d) och disk (%d) är inte synkade efter reconciliation", len(certsAfter), diskFilesAfter)
	}
}

func Test_PromoteReviewToQueue_InsertsIntoDB(t *testing.T) {
	cfg, n := setupTestInbox(t)
	base, pdfName := setupReviewItem(t, cfg, "test_promote_db", true)

	ext := &cert.Extraction{
		IsEN10204_3_1:     true,
		CertType:          "3.1",
		Charge:            "999111",
		Material:          "S355",
		EnStandardPresent: true,
		ProductForm:       "rundstång",
		Dimensions:        "20",
		Confidence:        "high",
	}
	newName, err := store.PromoteReviewToQueue(cfg, base, pdfName, ext, []string{"B999"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	// Infoga i DB (detta görs normalt av handler/tools)
	metaPath := filepath.Join(store.QueueDir(cfg), newName)
	if m, ok := store.ReadMetadata(metaPath); ok {
		cert := &store.Certificate{
			PDFHash:           m.Hash,
			Filename:          newName,
			CertType:          "3.1",
			Charge:            m.Charge,
			Material:          m.Material,
			EnStandardPresent: m.EnStandardPresent,
			Status:            "queue",
			ExtractedAt:       m.ExtractedAt,
			ModelUsed:         m.ModelUsed,
		}
		_, err = n.repo.InsertCertificate(cert)
		if err != nil {
			t.Fatalf("DB-insert: %v", err)
		}
	}

	// Verifiera att certifikatet finns i DB
	certs, err := n.repo.ListCertificates("queue")
	if err != nil {
		t.Fatalf("list certs: %v", err)
	}
	if len(certs) != 1 {
		t.Errorf("förväntade 1 cert i DB, fick %d", len(certs))
	}
	if len(certs) > 0 {
		if certs[0].Charge != "999111" {
			t.Errorf("cert.Charge = %q, vill ha %q", certs[0].Charge, "999111")
		}
		if certs[0].Filename != newName {
			t.Errorf("cert.Filename = %q, vill ha %q", certs[0].Filename, newName)
		}
	}
}

// Test_ClassifyCategory_NonCert_PersistedAndArchived verifierar Fas 2:
// steg 0 kategori-klassificering. Ett mejl som klassas som "invoice" (ej cert,
// ej reklam) ska persisteras i emails med mail_category="invoice" och arkiveras
// på disk — UTAN att gå genom verify/extract-vägen (inga sådana stubs finns, så
// om koden ändå anropar dem skriker stub:en). Inget cert hamnar i kön.
func Test_ClassifyCategory_NonCert_PersistedAndArchived(t *testing.T) {
	cfg, n := setupTestInbox(t)
	stub := startStubAnthropic(t, stubMap{
		"classify_mail_category": classifyCategoryInvoiceJSON,
		// medvetet INGA verify_pdfs/submit_extraction/classify_email — cert-vägen
		// ska aldrig nås för en faktura.
	})
	client := makeTestClient(stub)
	emlPath := copyFixture(t, cfg, "cert_with_pdf.eml")

	processEml(context.Background(), client, cfg, emlPath, n, 1, 1)

	// Inget cert i kön — cert-flödet hoppades över.
	if cnt := countFiles(t, store.QueueDir(cfg)); cnt != 0 {
		t.Errorf("queue ska vara tom för faktura, fick %d filer", cnt)
	}
	// Arkiverat på disk.
	subs := listSubdirs(t, store.ArkiveratDir(cfg))
	if len(subs) != 1 {
		t.Fatalf("arkiverat: förväntade 1 undermapp, fick %d (%v)", len(subs), subs)
	}
	// .eml borttagen från inbox.
	if _, err := os.Stat(emlPath); !os.IsNotExist(err) {
		t.Errorf("eml borde tas bort efter arkivering, men finns kvar")
	}
	// Persisterat i emails med rätt kategori.
	emails, err := n.repo.ListEmailsByCategory("invoice")
	if err != nil {
		t.Fatalf("list emails by category: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("förväntade 1 faktura-mejl i DB, fick %d", len(emails))
	}
	if emails[0].MailCategory != "invoice" {
		t.Errorf("mail_category = %q, vill ha %q", emails[0].MailCategory, "invoice")
	}
}
