package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cert-renamer/internal/server"
)

// ---------------------------------------------------------------------------
// Loggning (filbaserad)
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

func logFilePath() string {
	return filepath.Join(logDir(), "cert-renamer-"+time.Now().Format("2006-01-02")+".log")
}

func initFileLog() (*os.File, error) {
	if err := os.MkdirAll(logDir(), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	return f, nil
}

func pruneOldLogs(keep time.Duration) {
	entries, err := os.ReadDir(logDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-keep)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "cert-renamer-") || !strings.HasSuffix(e.Name(), ".log") {
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
// Browser-launch (Chrome/Edge --app, fallback default)
// ---------------------------------------------------------------------------

func openBrowser(url string) {
	if launchAppMode(url) {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func launchAppMode(url string) bool {
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Arc"} {
			if _, err := os.Stat("/Applications/" + app + ".app"); err != nil {
				continue
			}
			cmd := exec.Command("open", "-na", app, "--args",
				"--app="+url, "--window-size=900,720")
			if err := cmd.Run(); err == nil {
				return true
			}
		}
	case "windows":
		paths := []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				cmd := exec.Command(p, "--app="+url, "--window-size=900,720")
				if err := cmd.Start(); err == nil {
					return true
				}
			}
		}
	default:
		for _, b := range []string{"google-chrome", "chromium", "microsoft-edge"} {
			if path, err := exec.LookPath(b); err == nil {
				cmd := exec.Command(path, "--app="+url, "--window-size=900,720")
				if err := cmd.Start(); err == nil {
					return true
				}
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	log.SetFlags(log.LstdFlags)
	if logFile, err := initFileLog(); err != nil {
		log.Printf("⚠️  kunde inte öppna loggfil: %v (fortsätter med stdout)", err)
	} else {
		defer logFile.Close()
		pruneOldLogs(30 * 24 * time.Hour)
		log.Printf("🟢 cert-renamer startad — pid=%d log=%s", os.Getpid(), logFilePath())
	}
	srv := server.New()
	mux := server.NewMux(srv)
	go srv.Autostart()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("kan inte binda port: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	url := fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
	log.Printf("🌐 Öppnar %s", url)

	go func() {
		time.Sleep(300 * time.Millisecond)
		openBrowser(url)
	}()

	if err := http.Serve(ln, mux); err != nil {
		log.Fatal(err)
	}
}
