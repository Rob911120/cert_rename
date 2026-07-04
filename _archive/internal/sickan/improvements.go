package sickan

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ImprovementEntry struct {
	Timestamp string `json:"timestamp"`
	Text      string `json:"text"`
}

const (
	improvementsTimeout = 15 * time.Second

	// Hårdkodade IDn för Robs Google Form/Sheet — verifierat under planering:
	// POST → 200 utan redirect, ny rad i Sheet:en inom 3 sek.
	improvementsFormID  = "1FAIpQLScc-nFVRlw3J51ku306OraQUEqbQl3j_q3fvTWL3P4ggc-9MA"
	improvementsEntryID = "1195741770"
	improvementsSheetID = "1JLUra3pkNtkiZQ0YTbjmpglil6jjrDlHPtryFENe_hs"
)

// improvementsHTTPClient kan stubbas i tester (Transport som rewrite:ar
// docs.google.com → httptest-server).
var improvementsHTTPClient = &http.Client{Timeout: improvementsTimeout}

// AddImprovement POST:ar text till det hårdkodade Google Form:et.
func AddImprovement(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("tom text")
	}
	target := fmt.Sprintf("https://docs.google.com/forms/d/e/%s/formResponse", improvementsFormID)
	body := url.Values{"entry." + improvementsEntryID: {text}}.Encode()
	req, err := http.NewRequest("POST", target, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := improvementsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("oväntad status: %d", resp.StatusCode)
	}
	return nil
}

// ListImprovements hämtar Sheet:en som CSV och returnerar rader.
func ListImprovements() ([]ImprovementEntry, error) {
	target := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/export?format=csv", improvementsSheetID)
	resp, err := improvementsHTTPClient.Get(target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d — kontrollera att Sheet är delat 'Anyone with the link'", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "csv") {
		return nil, fmt.Errorf("oväntat content-type — Sheet kanske inte är publikt delad")
	}
	br := bufio.NewReader(resp.Body)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	rows, err := csv.NewReader(br).ReadAll()
	if err != nil {
		return nil, err
	}
	out := []ImprovementEntry{}
	for i, r := range rows {
		if i == 0 || len(r) < 2 {
			continue
		}
		out = append(out, ImprovementEntry{Timestamp: r[0], Text: r[1]})
	}
	return out, nil
}
