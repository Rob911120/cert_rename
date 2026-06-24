// Package monitor är en läs-/skrivklient mot Monitor G5:s 001.1 REST+OData-API.
// Leaf-paket: importerar inget annat internt paket (som eml/cert), så det kan
// återanvändas fritt och testas isolerat med httptest.
//
// VIKTIGT — auth är overifierad: Monitors login-endpoint och hur sessionen ska
// skickas vidare (header vs cookie) är INTE dokumenterat i API-crawlen. Vi gör
// två saker defensivt: (1) sätter headern X-Monitor-SessionId från login-svarets
// SessionId på varje anrop, och (2) använder en cookie-jar så ev. Set-Cookie
// från login bärs med automatiskt. Exakt mekanism måste bekräftas live mot
// 192.168.52.232:8001 innan skriv-vägen (ReportArrivals) tas i bruk.
package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SessionHeader är header-namnet vi skickar sessionen i. Defaulten är Monitors
// konvention men kan behöva justeras efter live-verifiering (se paketdoc).
const SessionHeader = "X-Monitor-SessionId"

// Client är en Monitor-API-klient. Session/credentials skyddas av mu och
// (re)login serialiseras av loginMu, så samtidiga anrop är säkra. Vid 401
// (utgången session) loggar klienten in igen med sparade uppgifter och försöker
// anropet en gång till — se send().
type Client struct {
	baseURL string
	lang    string // språkkod i path-prefixet, t.ex. "sv" → /sv/001.1/...
	http    *http.Client

	mu      sync.Mutex // skyddar session/user/pass
	session string
	user    string
	pass    string

	loginMu sync.Mutex // serialiserar (re)login så samtidiga 401 inte loggar in i kapp
}

// New skapar en klient mot baseURL (t.ex. "https://192.168.52.232:8001").
// TLS-verifiering stängs av (self-signed cert på lokala Monitor-servern) och en
// rimlig timeout sätts. Språkkoden defaultar till "sv".
func New(baseURL string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		lang:    "sv",
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// SetLanguage byter språkkoden i path-prefixet (t.ex. "se"/"en").
func (c *Client) SetLanguage(lang string) {
	if lang != "" {
		c.lang = lang
	}
}

// SessionID returnerar nuvarande session (tom innan Login). Mest för logg/test.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// LoggedIn säger om en session finns.
func (c *Client) LoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session != ""
}

// apiBase är prefixet för alla anrop, t.ex. "https://host/sv/001.1".
func (c *Client) apiBase() string { return c.baseURL + "/" + c.lang + "/001.1" }

// Login autentiserar och sparar SessionId. Body: {Username, Password,
// ForceRelogin:true}. Returnerar fel vid transport-/HTTP-fel eller om inget
// SessionId kom i svaret (och ingen cookie sattes).
func (c *Client) Login(ctx context.Context, user, pass string) error {
	body, _ := json.Marshal(map[string]any{
		"Username":     user,
		"Password":     pass,
		"ForceRelogin": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+"/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("login: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var lr struct {
		SessionId string `json:"SessionId"`
	}
	_ = json.Unmarshal(data, &lr)
	// Spara session + credentials under lås. Credentials behövs för auto-relogin
	// vid 401 (utgången session).
	c.mu.Lock()
	c.session = lr.SessionId
	c.user, c.pass = user, pass
	session := c.session
	c.mu.Unlock()
	// Cookie-jar kan ha fått en session-cookie även om svaret saknar SessionId.
	if session == "" && !c.hasCookies() {
		return fmt.Errorf("login: varken SessionId i svaret eller session-cookie")
	}
	return nil
}

// reloginIfStale loggar in igen med sparade credentials, men bara om sessionen
// fortfarande är den utgångna (stale) — har en annan goroutine redan förnyat
// hoppar vi över. Serialiseras av loginMu så samtidiga 401 inte loggar in i kapp.
func (c *Client) reloginIfStale(ctx context.Context, stale string) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.mu.Lock()
	cur, user, pass := c.session, c.user, c.pass
	c.mu.Unlock()
	if cur != stale {
		return nil // redan förnyad av en annan goroutine
	}
	if user == "" {
		return fmt.Errorf("ingen sparad credential för relogin")
	}
	return c.Login(ctx, user, pass)
}

// send utför en autentiserad request och, vid 401 (utgången session), loggar in
// igen en gång och försöker om. build kallas på nytt per försök eftersom en
// request-body konsumeras vid Do. Andra fel (inkl. 403) propageras orört.
func (c *Client) send(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	c.mu.Lock()
	stale := c.session
	c.mu.Unlock()

	req, err := build()
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	// Session utgången → töm/stäng svaret, logga in igen, försök en gång till.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if rerr := c.reloginIfStale(ctx, stale); rerr != nil {
		return nil, fmt.Errorf("session utgången och relogin misslyckades: %w", rerr)
	}
	req2, err := build()
	if err != nil {
		return nil, err
	}
	c.auth(req2)
	return c.http.Do(req2)
}

func (c *Client) hasCookies() bool {
	if c.http.Jar == nil {
		return false
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	return len(c.http.Jar.Cookies(u)) > 0
}

// auth sätter session-headern på en request (cookie-jar sköts av http.Client).
func (c *Client) auth(r *http.Request) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s != "" {
		r.Header.Set(SessionHeader, s)
	}
}

// getList gör en GET mot apiBase()+path med OData-query och avkodar svaret
// (både {"value":[...]} och bart [...]) till out.
func (c *Client) getList(ctx context.Context, path string, q *Query, out any) error {
	u := c.apiBase() + path
	if vals := q.Values(); len(vals) > 0 {
		u += "?" + vals.Encode()
	}
	resp, err := c.send(ctx, func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Accept", "application/json")
		return r, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeList(data, out)
}

// postJSON gör en POST mot apiBase()+path med body som JSON och avkodar svaret
// till out (om out != nil). Används för write-kommandon som ReportArrivals.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.send(ctx, func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+path, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		return r, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// decodeList avkodar antingen ett OData-wrappat svar {"value":[...]} eller en
// bar JSON-array [...] till out.
func decodeList(data []byte, out any) error {
	t := bytes.TrimSpace(data)
	if len(t) > 0 && t[0] == '{' {
		var wrap struct {
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(t, &wrap); err != nil {
			return err
		}
		if len(wrap.Value) > 0 {
			return json.Unmarshal(wrap.Value, out)
		}
		// Objekt utan "value" — sista försök: avkoda direkt (kan vara enskild post).
		return json.Unmarshal(t, out)
	}
	return json.Unmarshal(t, out)
}
