package server

// Sickans kommentar på morgonbriefen (fas 2b): en läs-läge-körning av
// chat-loopen (Toolbox.ReadOnly) med dagens stomme som kontext. Sickan läser
// noter så känt inte upprepas, prioriterar, kan bifoga mailutkast
// (compose_deviation_mail) och föreslå regler ur avfärdande-mönster
// (remember_rule). Utan API-nyckel visas briefen utan kommentar — stommen bär.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/sickan"
	"cert-renamer/internal/store"
)

// briefCommentTimeout är taket för hela läs-körningen (flera tool-rundor).
const briefCommentTimeout = 3 * time.Minute

// briefPromptRow är den slimmade rad Sickan ser i kommentar-prompten.
// BriefRow bär numera HELA inleveransraden (detaljpanelen i Översikt) —
// att marshala den rakt in i prompten vore token-bloat. last_note måste
// behållas: prompt-texten refererar fältet vid namn.
type briefPromptRow struct {
	OrderNumber  string  `json:"order_number"`
	SupplierName string  `json:"supplier_name,omitempty"`
	PartNumber   string  `json:"part_number"`
	PlannedQty   float64 `json:"planned_qty,omitempty"`
	DeliveryDate string  `json:"delivery_date"`
	RequiredCert string  `json:"required_cert,omitempty"`
	CertStatus   string  `json:"cert_status,omitempty"`
	MaterialOK   string  `json:"material_ok,omitempty"`
	LastNote     string  `json:"last_note,omitempty"`
	NoteCount    int     `json:"note_count,omitempty"`
}

type briefPromptData struct {
	Date          string           `json:"date"`
	ExpectedToday []briefPromptRow `json:"expected_today"`
	Overdue       []briefPromptRow `json:"overdue"`
	CertMissing   []briefPromptRow `json:"cert_missing"`
	PartialOrders []string         `json:"partial_orders"`
	Tasks         []store.Task     `json:"tasks"`
	Dismissed     []string         `json:"dismissed,omitempty"`
}

// briefPromptView projicerar briefen till det Sickan behöver för att
// prioritera — inte mer.
func briefPromptView(b *BriefData) briefPromptData {
	slim := func(rows []BriefRow) []briefPromptRow {
		out := make([]briefPromptRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, briefPromptRow{
				OrderNumber:  r.OrderNumber,
				SupplierName: r.SupplierName,
				PartNumber:   r.PartNumber,
				PlannedQty:   r.PlannedQty,
				DeliveryDate: r.DeliveryDate,
				RequiredCert: r.RequiredCert,
				CertStatus:   r.CertStatus,
				MaterialOK:   r.MaterialOK,
				LastNote:     r.LastNote,
				NoteCount:    r.NoteCount,
			})
		}
		return out
	}
	return briefPromptData{
		Date:          b.Date,
		ExpectedToday: slim(b.ExpectedToday),
		Overdue:       slim(b.Overdue),
		CertMissing:   slim(b.CertMissing),
		PartialOrders: b.PartialOrders,
		Tasks:         b.Tasks,
		Dismissed:     b.Dismissed,
	}
}

// briefCommentPrompt är uppdraget som skickas med stommen som första (och
// enda) user-meddelande i läs-körningen.
func briefCommentPrompt(b *BriefData) string {
	view := briefPromptView(b)
	raw, _ := json.MarshalIndent(view, "", "  ")
	return `Här är dagens morgonbrief-stomme, byggd ur databasen — det Rob ser i "Idag"-fliken:

` + string(raw) + `

Din uppgift: skriv en KORT kommentar (max ~8 rader) som hjälper Rob att prioritera sin morgon.
- Läs radernas noter (last_note ovan; get_upcoming_notes vid behov) så du inte föreslår sådant som redan är gjort eller känt.
- Följ arbetsreglerna (t.ex. jaga inte cert innan leveransen faktiskt dykt upp).
- Viktigast först. Nämn bara det som kräver handling — rada inte upp det Rob redan ser i sektionerna.
- Vill du föreslå ett mail: bygg utkastet med compose_deviation_mail och ta med mailto-länken i texten.
- Ser du ett mönster i avfärdande-noter ("Avfärdade i briefen: …") som borde bli en arbetsregel: spara den med remember_rule och nämn det kort.
- Något Rob inte får glömma som saknas i att-göra-listan: lägg till med add_task.
Svara med bara själva kommentaren — ingen rubrik, ingen hälsning.`
}

// generateBriefComment kör Sickan i läs-läge över dagens brief och sparar
// kommentaren i den. force=true skriver över en befintlig kommentar (annars
// körs den max en gång per dag). Avsedd att köras i egen goroutine.
func (s *Server) generateBriefComment(force bool) {
	if !s.briefCommenting.CompareAndSwap(false, true) {
		return // en körning i taget
	}
	defer s.briefCommenting.Store(false)

	cfg := s.snapshotCfg()
	if cfg.ApiKey == "" {
		return // stommen bär — kommentaren är ett tillägg
	}
	today := time.Now().Format("2006-01-02")
	b := s.loadBrief()
	if b == nil || b.Date != today {
		s.generateBrief()
		b = s.loadBrief()
	}
	if b == nil || (b.Comment != "" && !force) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), briefCommentTimeout)
	defer cancel()

	s.mu.Lock()
	mon := s.mon
	s.mu.Unlock()
	client := anthropic.NewClient(option.WithAPIKey(cfg.ApiKey))
	tb := &sickan.Toolbox{
		Cfg: cfg, N: s, Repo: s.repo, Monitor: mon, MonitorConnect: s.ensureMonitor,
		Rules:    s.agentRules(),
		ReadOnly: true, // muterande verktyg bortplockade + spärrade i Dispatch
	}
	model := cfg.SickanModel
	if model == "" {
		model = ai.ChatDefault
	}

	history := []anthropic.MessageParam{{
		Role: anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{
			{OfText: &anthropic.TextBlockParam{Text: briefCommentPrompt(b)}},
		},
	}}
	// Behåll bara text efter SISTA tool-anropet — mellanliggande "jag kollar
	// noterna…"-text hör inte hemma i den sparade kommentaren.
	var text strings.Builder
	emit := func(ev sickan.Event) {
		switch ev.Kind {
		case "text":
			text.WriteString(ev.Data)
		case "tool_call":
			text.Reset()
		}
	}
	s.Logf("🤖 Sickan tittar på dagens brief…")
	if _, err := sickan.Run(ctx, &client, tb, s, model, history, emit); err != nil {
		s.Logf("⚠️ Brief-kommentar misslyckades: %v", err)
		if text.Len() == 0 {
			return
		}
	}
	comment := strings.TrimSpace(text.String())
	if comment == "" {
		return
	}

	// Briefen kan ha byggts om under körningen — läs om och skriv i färsk kopia.
	if cur := s.loadBrief(); cur != nil && cur.Date == today {
		cur.Comment = comment
		cur.CommentAt = time.Now().Format(time.RFC3339)
		s.saveBrief(cur)
		s.BroadcastBrief(cur)
		s.Logf("🤖 Sickans kommentar till dagens brief är klar")
	}
}

// handleBriefComment (POST) kickar en (om)körning av Sickans kommentar.
// Svarar 202 direkt — resultatet pushas via SSE-eventet "brief".
func (s *Server) handleBriefComment(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if s.snapshotCfg().ApiKey == "" {
		httpError(w, "ingen API-nyckel — fyll i ⚙️ Inställningar först", http.StatusBadRequest)
		return
	}
	if s.briefCommenting.Load() {
		httpError(w, "Sickan tittar redan på briefen — vänta in kommentaren", http.StatusConflict)
		return
	}
	go s.generateBriefComment(true)
	w.WriteHeader(http.StatusAccepted)
}
