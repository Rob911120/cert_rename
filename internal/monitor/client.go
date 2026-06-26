// Package monitor är en LÄS-klient mot Monitor G5:s 001.1 REST+OData-API.
// (Skriv-vägen togs bort: Monitors API-skrivning är inte licensierad på det här
// systemet — inleverans sker via UI-styrning av skrivbordsklienten i stället.)
// Leaf-paket: importerar inget annat internt paket (som eml/cert), så det kan
// återanvändas fritt och testas isolerat med httptest.
//
// VIKTIGT — auth är overifierad: Monitors login-endpoint och hur sessionen ska
// skickas vidare (header vs cookie) är INTE dokumenterat i API-crawlen. Vi gör
// två saker defensivt: (1) sätter headern X-Monitor-SessionId från login-svarets
// SessionId på varje anrop, och (2) använder en cookie-jar så ev. Set-Cookie
// från login bärs med automatiskt.
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

// GetRaw gör en autentiserad GET mot apiBase()+path med OData-query och
// returnerar de OAVKODADE svarsbytena (envelopen {"value":...} bevaras). Tänkt
// för diagnostik (cmd/monitor-probe Steg-0-dump) som behöver inspektera de
// exakta fältnamn Monitor skickar innan typerna i types.go fästs. Vid icke-2xx
// returneras bodyn tillsammans med felet så att felmeddelanden kan dumpas.
func (c *Client) GetRaw(ctx context.Context, path string, q *Query) ([]byte, error) {
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
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// maxPages är ett skydd mot en server som ignorerar $skip (annars oändlig loop).
const maxPages = 10000

// getAllPages hämtar ALLA rader för en query genom paginering. Strategin väljs
// efter vad servern gör på första sidan:
//   - bär svaret @odata.nextLink → följ länkarna tills ingen mer kommer
//     (server-driven, självterminerande).
//   - annars → paginera själva med $skip och stanna FÖRST på en TOM sida (inte
//     på "färre än begärt": Monitor kan ha ett eget sidtak under vårt $top, och
//     att stanna där skulle tyst trunkera resultatet).
//
// Auto-relogin på 401 bevaras eftersom varje sida går via c.getPage → c.send.
func getAllPages[T any](ctx context.Context, c *Client, path string, q *Query, pageSize int) ([]T, error) {
	if pageSize <= 0 {
		pageSize = 200
	}
	base := *q // kopia så anroparens query inte muteras
	base.top = pageSize
	if base.skip < 0 {
		base.skip = 0
	}

	pageURL := func(skip int) string {
		pq := base
		pq.skip = skip
		u := c.apiBase() + path
		if vals := pq.Values(); len(vals) > 0 {
			u += "?" + vals.Encode()
		}
		return u
	}

	var all []T

	// Sida 1.
	var first []T
	next, err := c.getPage(ctx, pageURL(base.skip), &first)
	if err != nil {
		return all, err
	}
	all = append(all, first...)

	if next != "" {
		// Server-driven paginering: följ @odata.nextLink tills slut.
		for i := 0; i < maxPages && next != ""; i++ {
			var batch []T
			next, err = c.getPage(ctx, c.resolveNext(next), &batch)
			if err != nil {
				return all, err
			}
			all = append(all, batch...)
		}
		return all, nil
	}

	// Klient-driven $skip-paginering: fortsätt tills en tom sida.
	if len(first) == 0 {
		return all, nil
	}
	skip := base.skip + len(first)
	for i := 0; i < maxPages; i++ {
		var batch []T
		if _, err := c.getPage(ctx, pageURL(skip), &batch); err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		skip += len(batch)
	}
	return all, nil
}

// getPage gör en autentiserad GET mot en absolut URL och avkodar svaret till out.
// Returnerar ev. @odata.nextLink. Auto-relogin på 401 via c.send.
func (c *Client) getPage(ctx context.Context, fullURL string, out any) (string, error) {
	resp, err := c.send(ctx, func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Accept", "application/json")
		return r, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s: status %d: %s", fullURL, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodePage(data, out)
}

// resolveNext gör en @odata.nextLink absolut. Monitor skickar (oftast) en absolut
// URL; en relativ länk tolkas relativt service-roten apiBase().
// VERIFIERA (Steg 0): exakt form på nextLink (absolut vs relativ, $skip vs $skiptoken).
func (c *Client) resolveNext(next string) string {
	if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
		return next
	}
	return c.apiBase() + "/" + strings.TrimLeft(next, "/")
}

// decodePage avkodar en sida ({"value":[...],"@odata.nextLink":"..."} eller en
// bar array) till out och returnerar ev. nextLink.
func decodePage(data []byte, out any) (string, error) {
	t := bytes.TrimSpace(data)
	if len(t) > 0 && t[0] == '{' {
		var wrap struct {
			Value    json.RawMessage `json:"value"`
			NextLink string          `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(t, &wrap); err != nil {
			return "", err
		}
		if len(wrap.Value) > 0 {
			return wrap.NextLink, json.Unmarshal(wrap.Value, out)
		}
		// Objekt utan "value" — kan vara en enskild post.
		return wrap.NextLink, json.Unmarshal(t, out)
	}
	return "", json.Unmarshal(t, out)
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
