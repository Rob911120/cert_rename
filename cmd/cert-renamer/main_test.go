package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cert-renamer/internal/v2/server"
	"cert-renamer/internal/v2/store"
)

// buildTestMux reser en riktig V2-server + mux mot en temp-databas, så testet
// motionerar samma SSE-handler (/api/events) som produktionen.
func buildTestMux(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return server.NewMux(server.New(store.Config{InboxDir: dir}, db, logger))
}

// openSSE öppnar en långlivad /api/events-anslutning och väntar tills första
// bytet kommit, så vi vet att handlern sitter fast i sin select-loop.
func openSSE(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("läsa SSE-ström: %v", err)
	}
	return resp
}

// TestGracefulShutdown_WithOpenSSE bevisar att gracefulServer stänger ner rent
// även med en öppen SSE-ström — dvs. att "context deadline exceeded" är borta.
func TestGracefulShutdown_WithOpenSSE(t *testing.T) {
	httpSrv, cancelConns := gracefulServer(buildTestMux(t))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go httpSrv.Serve(ln)
	baseURL := "http://" + ln.Addr().String()

	resp := openSSE(t, baseURL)
	defer resp.Body.Close()

	cancelConns() // det gracefulServer ger oss; avslutar streaming-handlern
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returnerade fel med öppen SSE-ström: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Shutdown tog %v — borde vara nästan omedelbart efter cancelConns", d)
	}
}

// TestNaiveShutdown_TimesOut karaktäriserar buggen: utan att avbryta
// request-kontexterna fastnar Shutdown tills sin deadline eftersom SSE-
// anslutningen aldrig blir inaktiv. Låser fast VARFÖR fixen behövs.
func TestNaiveShutdown_TimesOut(t *testing.T) {
	httpSrv := &http.Server{Handler: buildTestMux(t)} // som main.go gjorde före fixen

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go httpSrv.Serve(ln)
	baseURL := "http://" + ln.Addr().String()

	resp := openSSE(t, baseURL)
	defer resp.Body.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != context.DeadlineExceeded {
		t.Fatalf("förväntade context.DeadlineExceeded, fick: %v", err)
	}
}
