package sickan

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// rewriteTransport skickar alla requests till en httptest-server oavsett
// vilken URL request:en byggts med. Vi sparar undan original-URL:en så
// testet kan inspektera vad AddImprovement/ListImprovements faktiskt
// försöker anropa.
type rewriteTransport struct {
	stubURL    string
	lastURL    *url.URL
	lastBody   string
	lastMethod string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.lastURL = req.URL
	rt.lastMethod = req.Method
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		rt.lastBody = string(b)
		req.Body = io.NopCloser(strings.NewReader(rt.lastBody))
	}
	stub, _ := url.Parse(rt.stubURL)
	rewritten := *req
	rewritten.URL = &url.URL{
		Scheme:   stub.Scheme,
		Host:     stub.Host,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	}
	rewritten.Host = stub.Host
	return http.DefaultTransport.RoundTrip(&rewritten)
}

func withStubClient(t *testing.T, srv *httptest.Server) *rewriteTransport {
	t.Helper()
	rt := &rewriteTransport{stubURL: srv.URL}
	original := improvementsHTTPClient
	improvementsHTTPClient = &http.Client{Transport: rt}
	t.Cleanup(func() { improvementsHTTPClient = original })
	return rt
}

func Test_AddImprovement_PostsToFormResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("vill POST, fick %s", r.Method)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	rt := withStubClient(t, srv)
	if err := AddImprovement("ny förbättring"); err != nil {
		t.Fatalf("AddImprovement: %v", err)
	}
	wantPath := "/forms/d/e/" + improvementsFormID + "/formResponse"
	if rt.lastURL.Path != wantPath {
		t.Errorf("oväntad path: %s (vill %s)", rt.lastURL.Path, wantPath)
	}
	wantBody := "entry." + improvementsEntryID + "=ny+f%C3%B6rb%C3%A4ttring"
	if rt.lastBody != wantBody {
		t.Errorf("body: %q (vill %q)", rt.lastBody, wantBody)
	}
}

func Test_AddImprovement_RejectsEmptyText(t *testing.T) {
	if err := AddImprovement("  "); err == nil {
		t.Errorf("borde fail på tom text")
	}
}

func Test_AddImprovement_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	withStubClient(t, srv)
	if err := AddImprovement("text"); err == nil {
		t.Errorf("borde fail på 500")
	}
}

func Test_ListImprovements_ParsesCSV(t *testing.T) {
	csvBody := "Tidstämpel,Kolumn 1\n2026-05-02 10:00:00,första posten\n2026-05-02 10:05:00,andra posten\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/spreadsheets/d/" + improvementsSheetID + "/export"
		if r.URL.Path != wantPath {
			t.Errorf("oväntad path: %s (vill %s)", r.URL.Path, wantPath)
		}
		if r.URL.Query().Get("format") != "csv" {
			t.Errorf("format-param saknas: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte(csvBody))
	}))
	defer srv.Close()
	withStubClient(t, srv)
	rows, err := ListImprovements()
	if err != nil {
		t.Fatalf("ListImprovements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("vill 2 rader, fick %d: %+v", len(rows), rows)
	}
	if rows[0].Text != "första posten" {
		t.Errorf("första: %+v", rows[0])
	}
	if rows[1].Timestamp != "2026-05-02 10:05:00" {
		t.Errorf("andra: %+v", rows[1])
	}
}

func Test_ListImprovements_StripsBOM(t *testing.T) {
	// Google Sheets kan i vissa locale-fall lägga UTF-8 BOM (EF BB BF)
	// först i CSV-svaret. Vi ska strippa det innan parse, annars blir
	// första header-cellen "<BOM>Tidstämpel" och vi har förlorat
	// header-matchning för framtida features.
	csvWithBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte("Tidstämpel,Kolumn 1\n2026-05-02,med-bom\n")...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(csvWithBOM)
	}))
	defer srv.Close()
	withStubClient(t, srv)
	rows, err := ListImprovements()
	if err != nil {
		t.Fatalf("ListImprovements: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("vill 1 rad, fick %d: %+v", len(rows), rows)
	}
	if rows[0].Text != "med-bom" {
		t.Errorf("text: %+v", rows[0])
	}
	if rows[0].Timestamp != "2026-05-02" {
		t.Errorf("timestamp: %+v", rows[0])
	}
}

func Test_ListImprovements_RejectsHTMLContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer srv.Close()
	withStubClient(t, srv)
	if _, err := ListImprovements(); err == nil {
		t.Errorf("borde fail när content-type inte är csv")
	}
}

func Test_ListImprovements_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	withStubClient(t, srv)
	if _, err := ListImprovements(); err == nil {
		t.Errorf("borde fail på 403")
	}
}

func Test_Dispatch_AddImprovement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	withStubClient(t, srv)
	tb, _ := setupToolbox(t)
	res, err := tb.Dispatch("add_improvement", json.RawMessage(`{"text":"vi behöver bättre fel-meddelanden"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(res.Summary, `"ok":true`) {
		t.Errorf("svar saknar ok:true: %s", res.Summary)
	}
}

func Test_Dispatch_ListImprovements(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("Tidstämpel,Kolumn 1\n2026-05-02,test\n"))
	}))
	defer srv.Close()
	withStubClient(t, srv)
	tb, _ := setupToolbox(t)
	res, err := tb.Dispatch("list_improvements", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(res.Summary, `"count":1`) {
		t.Errorf("svar saknar count:1: %s", res.Summary)
	}
	if !strings.Contains(res.Summary, "test") {
		t.Errorf("svar saknar text: %s", res.Summary)
	}
}
