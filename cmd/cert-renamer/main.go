// cert-renamer — appens enda binär (V2-arkitekturen "DB är sanningen"). Endast
// wiring: konfig, databas, HTTP-server, browser-launch och graceful shutdown.
// All logik bor i internal/v2/*. (V1 är arkiverat under _archive/.)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/importer"
	"cert-renamer/internal/v2/server"
	"cert-renamer/internal/v2/store"
)

func main() {
	noBrowser := flag.Bool("no-browser", false, "starta utan att öppna webbläsaren")
	importV1 := flag.Bool("import-v1", false, "engångsimport av V1:s approved/ + queue/ till V2, avsluta sedan")
	flag.Parse()

	logger, closeLog := newLogger()
	defer closeLog()
	slog.SetDefault(logger)

	if *importV1 {
		if err := runImport(logger); err != nil {
			logger.Error("V1-import misslyckades", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(logger, *noBrowser); err != nil {
		logger.Error("avslutas med fel", "err", err)
		os.Exit(1)
	}
}

// runImport kör engångs-bootstrappen -import-v1 och avslutar.
func runImport(logger *slog.Logger) error {
	cfg := store.LoadConfig()
	db, err := store.Open(store.DBPath())
	if err != nil {
		return fmt.Errorf("öppna databas: %w", err)
	}
	defer db.Close()
	if err := store.EnsureDirs(cfg); err != nil {
		return err
	}
	a := app.New(store.NewRepository(db), func() store.Config { return cfg }, nil, slogNotifier{logger})
	stats, err := importer.ImportV1(context.Background(), a, cfg)
	logger.Info("V1-import", "importerade", stats.Imported, "dubbletter", stats.Skipped,
		"utan_metadata", stats.NoMeta, "fel", stats.Errors)
	return err
}

// slogNotifier uppfyller app.Notifier för CLI-läget (ingen SSE att pinga).
type slogNotifier struct{ log *slog.Logger }

func (n slogNotifier) Logf(format string, args ...any) { n.log.Info(fmt.Sprintf(format, args...)) }
func (n slogNotifier) OverviewChanged()                {}

func run(logger *slog.Logger, noBrowser bool) error {
	cfg := store.LoadConfig()

	db, err := store.Open(store.DBPath())
	if err != nil {
		return fmt.Errorf("öppna databas: %w", err)
	}
	defer db.Close()
	logger.Info("databas initierad", "path", store.DBPath())

	srv := server.New(cfg, db, logger)
	mux := server.NewMux(srv)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("binda port: %w", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	logger.Info("lyssnar", "url", url)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv, cancelConns := gracefulServer(mux)
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { srv.RunSyncScheduler(ctx); return nil })
	go srv.Autostart()

	g.Go(func() error {
		if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		logger.Info("stänger ner")
		// Avbryt först alla pågående request-kontexter så långlivade
		// streaming-handlers (SSE: /api/events) avslutar — annars blir
		// anslutningen aldrig inaktiv och Shutdown fastnar tills sin
		// deadline och returnerar "context deadline exceeded".
		cancelConns()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	})

	if !noBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openBrowser(url)
		}()
	}

	return g.Wait()
}

// gracefulServer bygger en http.Server vars alla request-kontexter härleds ur
// en gemensam bas-kontext. cancelFunc:en avbryter den basen, vilket avslutar
// långlivade streaming-handlers (SSE) så deras anslutningar blir inaktiva och
// httpSrv.Shutdown kan slutföra i stället för att fastna tills sin deadline.
func gracefulServer(mux http.Handler) (*http.Server, context.CancelFunc) {
	baseCtx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler:     mux,
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	return srv, cancel
}

// ---------------------------------------------------------------------------
// Loggning: slog till stderr + dagsroterad fil (V1:s logDir-konvention).
// ---------------------------------------------------------------------------

func logDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "cert-renamer")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "cert-renamer", "Logs")
		}
		return filepath.Join(home, "AppData", "Local", "cert-renamer", "Logs")
	default:
		return filepath.Join(home, ".local", "state", "cert-renamer")
	}
}

func newLogger() (*slog.Logger, func()) {
	var w io.Writer = os.Stderr
	closer := func() {}
	if err := os.MkdirAll(logDir(), 0755); err == nil {
		path := filepath.Join(logDir(), "cert-renamer-v2-"+time.Now().Format("2006-01-02")+".log")
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			w = io.MultiWriter(os.Stderr, f)
			closer = func() { f.Close() }
			pruneOldLogs(30 * 24 * time.Hour)
		}
	}
	return slog.New(slog.NewTextHandler(w, nil)), closer
}

func pruneOldLogs(keep time.Duration) {
	entries, err := os.ReadDir(logDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-keep)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "cert-renamer-v2-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(logDir(), e.Name()))
	}
}

// ---------------------------------------------------------------------------
// Browser-launch (Chrome/Edge --app, fallback default) — samma som V1.
// ---------------------------------------------------------------------------

func openBrowser(url string) {
	if launchAppMode(url) {
		return
	}
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	_ = execCommand(name, args...)
}

func launchAppMode(url string) bool {
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Arc"} {
			if _, err := os.Stat("/Applications/" + app + ".app"); err != nil {
				continue
			}
			if execCommand("open", "-na", app, "--args", "--app="+url, "--window-size=1100,800") == nil {
				return true
			}
		}
	case "windows":
		for _, p := range []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				if execCommand(p, "--app="+url, "--window-size=1100,800") == nil {
					return true
				}
			}
		}
	default:
		for _, b := range []string{"google-chrome", "chromium", "microsoft-edge"} {
			if execCommand(b, "--app="+url, "--window-size=1100,800") == nil {
				return true
			}
		}
	}
	return false
}

// execCommand startar ett program utan att vänta på att det avslutas.
func execCommand(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return exec.Command(path, args...).Start()
}
