package server

// Morgonbriefen ("Idag"-fliken): dagens läge byggt DETERMINISTISKT ur databasen
// — väntas idag, kom inte, cert saknas, delleveranser, att göra. Alltid rätt,
// alltid samma form, funkar utan API-nyckel. Sickans kommentar (fas 2b) läggs
// ovanpå som ett eget fält.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cert-renamer/internal/store"
)

// App-state-nycklar för briefen.
const (
	appStateBrief = "last_brief" // JSON-serialiserad BriefData
)

// BriefRow är en rad i en brief-sektion — det minsta UI:t behöver för att visa
// och agera (id:n som strängar pga JS-precision).
type BriefRow struct {
	DeliveryRowID string  `json:"delivery_row_id"`
	OrderNumber   string  `json:"order_number"`
	Supplier      string  `json:"supplier"`
	PartNumber    string  `json:"part_number"`
	Description   string  `json:"description,omitempty"`
	Qty           float64 `json:"qty,omitempty"`
	DeliveryDate  string  `json:"delivery_date"`
	RequiredCert  string  `json:"required_cert,omitempty"`
	LastNote      string  `json:"last_note,omitempty"`
	NoteCount     int     `json:"note_count,omitempty"`
}

// BriefData är hela briefen. Dismissed är dagens avfärdade item-nycklar
// ("<sektion>:<delivery_row_id>"); Comment är Sickans kommentar (fas 2b).
type BriefData struct {
	Date          string       `json:"date"`
	GeneratedAt   string       `json:"generated_at"`
	ExpectedToday []BriefRow   `json:"expected_today"`
	Overdue       []BriefRow   `json:"overdue"`
	CertMissing   []BriefRow   `json:"cert_missing"`
	PartialOrders []string     `json:"partial_orders"`
	Tasks         []store.Task `json:"tasks"`
	Dismissed     []string     `json:"dismissed,omitempty"`
	Comment       string       `json:"comment,omitempty"`
	CommentAt     string       `json:"comment_at,omitempty"`
}

// buildBrief bygger briefens stomme ur rader + tasks. Ren funktion (testbar):
// today är "YYYY-MM-DD".
func buildBrief(rows []store.UpcomingDelivery, tasks []store.Task, today string) BriefData {
	brief := BriefData{
		Date:          today,
		ExpectedToday: []BriefRow{},
		Overdue:       []BriefRow{},
		CertMissing:   []BriefRow{},
		PartialOrders: []string{},
		Tasks:         tasks,
	}
	if brief.Tasks == nil {
		brief.Tasks = []store.Task{}
	}

	type orderStat struct{ delivered, pending int }
	orders := map[string]*orderStat{}

	for _, r := range rows {
		key := r.OrderNumber
		if key == "" {
			key = "PO " + strconv.FormatInt(r.PurchaseOrderID, 10)
		}
		st := orders[key]
		if st == nil {
			st = &orderStat{}
			orders[key] = st
		}
		if r.LocalStatus == store.UpcomingDelivered {
			st.delivered++
		} else {
			st.pending++
		}

		br := briefRow(r)
		if r.LocalStatus != store.UpcomingDelivered && r.DeliveryDate != "" {
			switch {
			case r.DeliveryDate == today:
				brief.ExpectedToday = append(brief.ExpectedToday, br)
			case r.DeliveryDate < today:
				brief.Overdue = append(brief.Overdue, br)
			}
		}
		if r.CertStatus == store.CertMissing {
			brief.CertMissing = append(brief.CertMissing, br)
		}
	}

	for key, st := range orders {
		if st.delivered > 0 && st.pending > 0 {
			brief.PartialOrders = append(brief.PartialOrders, key)
		}
	}
	// Deterministisk ordning (map-iteration är slumpad).
	sortStrings(brief.PartialOrders)
	return brief
}

func briefRow(r store.UpcomingDelivery) BriefRow {
	br := BriefRow{
		DeliveryRowID: strconv.FormatInt(r.DeliveryRowID, 10),
		OrderNumber:   r.OrderNumber,
		Supplier:      r.SupplierName,
		PartNumber:    r.PartNumber,
		Description:   r.Description,
		Qty:           r.PlannedQty,
		DeliveryDate:  r.DeliveryDate,
		RequiredCert:  r.RequiredCert,
		NoteCount:     len(r.RowNotes),
	}
	if n := len(r.RowNotes); n > 0 {
		br.LastNote = r.RowNotes[n-1].Text
	}
	return br
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// generateBrief bygger dagens brief ur databasen och sparar den. Avfärdanden
// och Sickans kommentar bevaras när briefen byggs om under samma dag.
func (s *Server) generateBrief() {
	rows, err := s.repo.ListUpcoming()
	if err != nil {
		s.Logf("⚠️ brief: ListUpcoming: %v", err)
		return
	}
	tasks, err := s.repo.ListTasks(true)
	if err != nil {
		s.Logf("⚠️ brief: ListTasks: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	brief := buildBrief(rows, tasks, today)
	brief.GeneratedAt = time.Now().Format(time.RFC3339)

	if prev := s.loadBrief(); prev != nil && prev.Date == today {
		brief.Dismissed = prev.Dismissed
		brief.Comment = prev.Comment
		brief.CommentAt = prev.CommentAt
	}
	s.saveBrief(&brief)
	s.BroadcastBrief(&brief)
}

func (s *Server) loadBrief() *BriefData {
	raw, err := s.repo.GetAppState(appStateBrief)
	if err != nil || raw == "" {
		return nil
	}
	var b BriefData
	if json.Unmarshal([]byte(raw), &b) != nil {
		return nil
	}
	return &b
}

func (s *Server) saveBrief(b *BriefData) {
	raw, _ := json.Marshal(b)
	if err := s.repo.SetAppState(appStateBrief, string(raw)); err != nil {
		s.Logf("⚠️ brief: kunde inte spara: %v", err)
	}
}

// BroadcastBrief pushar briefen till alla SSE-klienter.
func (s *Server) BroadcastBrief(b *BriefData) {
	raw, _ := json.Marshal(b)
	s.broadcast(ssEvent{Event: "brief", Data: string(raw)})
}

// maybeMorningBrief kollas i scheduleloopen: är briefen på, morgontiden
// passerad och ingen brief byggd idag → kicka en refresh (briefen byggs efter).
func (s *Server) maybeMorningBrief() {
	cfg := s.snapshotCfg()
	if !cfg.BriefEnabled {
		return
	}
	today := time.Now().Format("2006-01-02")
	if prev := s.loadBrief(); prev != nil && prev.Date == today {
		return
	}
	target, err := time.Parse("15:04", cfg.BriefTime)
	if err != nil {
		return
	}
	now := time.Now()
	targetToday := time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, now.Location())
	if now.Before(targetToday) {
		return
	}
	s.Logf("🌅 Morgonbrief: kickar refresh (mål %s)", cfg.BriefTime)
	if cfg.UpcomingEnabled {
		s.KickUpcoming() // briefen byggs efter refreshens slut
	} else {
		s.generateBrief() // ingen Monitor-koppling — bygg ur det vi har
	}
}

// handleBrief returnerar dagens brief. Stommen byggs ALLTID om ur databasen
// (två billiga queries) så fliken aldrig visar gammalt läge — noter, tasks och
// leveransmarkeringar som ändrats sedan förra bygget kommer med direkt.
// Avfärdanden + Sickans kommentar bevaras av generateBrief under samma dag.
func (s *Server) handleBrief(w http.ResponseWriter, r *http.Request) {
	s.generateBrief()
	b := s.loadBrief()
	if b == nil {
		httpError(w, "kunde inte bygga briefen", http.StatusInternalServerError)
		return
	}
	writeJSON(w, b)
}

// handleBriefDismiss avfärdar ett brief-item för dagen ("<sektion>:<rad-id>").
// En valfri anledning sparas som not på raden — det är så avfärdanden blir
// lärdomar (Sickan läser noterna).
func (s *Server) handleBriefDismiss(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Key == "" {
		httpError(w, "key krävs", http.StatusBadRequest)
		return
	}
	b := s.loadBrief()
	if b == nil {
		httpError(w, "ingen brief att avfärda i", http.StatusNotFound)
		return
	}
	for _, k := range b.Dismissed {
		if k == body.Key {
			writeJSON(w, b)
			return
		}
	}
	b.Dismissed = append(b.Dismissed, body.Key)
	s.saveBrief(b)

	if reason := strings.TrimSpace(body.Reason); reason != "" {
		if idx := strings.LastIndex(body.Key, ":"); idx >= 0 {
			if rowID, err := strconv.ParseInt(body.Key[idx+1:], 10, 64); err == nil && rowID > 0 {
				if _, err := s.repo.AddUpcomingNote(rowID, "rob", "Avfärdade i briefen: "+reason); err != nil {
					s.Logf("⚠️ brief: kunde inte spara avfärdande-not: %v", err)
				} else {
					s.BroadcastUpcoming()
				}
			}
		}
	}
	s.BroadcastBrief(b)
	writeJSON(w, b)
}
