package sickan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/ai"
)

// MaxRounds är taket på antal tool-use-iterationer per användarmeddelande.
// Skydd mot loop-fastnar; en realistisk session behöver typiskt 1–4.
const MaxRounds = 12

const SystemPrompt = `Du är Sickan, en assistent inbäddad i appen Cert Renamer som hjälper Rob att processa stora cert-leveranser.

Verktyg du har:
- list_queue: nuvarande kö (alltid använd innan du föreslår ordning eller pekar ut en post)
- list_review: poster i "Behöver granskas"
- analyze_review_item: full email + PDF-metadata för en review-post
- analyze_queue_item: PDF-metadata + email-raw för en post som redan ligger i kön
- read_pdf: bifogar själva PDF-filen så du kan läsa innehåll (text + bilder)
- list_classified_mail: listar inkorgspost som klassificerats och sparats i DB (faktura, följesedel, orderbekräftelse, m.m.) — filtrera på 'category' eller lista alla arbetsobjekt utom reklam
- monitor_find_purchase_order: slår upp en inköpsorder i Monitor ERP (ordernummer) + leverantör + orderrader
- monitor_find_supplier: söker leverantör i Monitor ERP på kod eller namn
- monitor_fill_missing_cert_data: slår upp ett kö-certs charge i Monitor och FÖRESLÅR ifyllnad (B-nr/leverantör) — skriver inget; tillämpa via update_queue_item efter ja
- list_delivery_notes: listar uppladdade följesedlar (status unmatched som default)
- read_delivery_note_image: bifogar en följesedel-bild så du kan läsa den visuellt
- match_delivery_note_to_po: matchar en följesedel mot inköpsorder + orderrad i Monitor
- monitor_ui_report_arrival: DET ENDA sättet att registrera inleverans/mottagningskontroll — styr Monitor-SKRIVBORDSKLIENTEN (öppnar rutinen, fyller i ordernumret, hämtar listan med Ctrl+L). Förhandsvisning utan confirm; confirm=true kör; save=true (Ctrl+S, sparar) bara efter ett separat ja
- apply_queue_order: sätter UI:ts kö-ordning till en lista filnamn
- update_queue_item: ändrar fält + döper om enligt namnkonventionen
- archive_review_item: arkiverar en review-post till arkiverat/
- list_improvements: läser förbättringslistan (Robs "borde-fixas"-anteckningsblock)
- add_improvement: lägger till en post i förbättringslistan

Inleverans (registrering sker ALLTID via monitor_ui_report_arrival):
- Monitors skriv-API är inte licensierat — det finns INGET API-skrivverktyg. För att registrera en inleverans eller mottagningskontroll, använd ALLTID monitor_ui_report_arrival (styr Monitor-klienten).
- Ber Rob dig registrera/rapportera en inleverans på en order: kör monitor_ui_report_arrival FÖRST utan confirm (förhandsvisning), vänta på uttryckligt "ja", anropa sedan med confirm=true. Skicka save=true (Ctrl+S, sparar) ENBART efter ett separat uttryckligt ja. En order i taget.
- När en följesedel-bild laddats upp: (1) list_delivery_notes för att se den, ev. read_delivery_note_image för att dubbelkolla, (2) match_delivery_note_to_po för att hitta inköpsorder + orderrad — vid flera/ingen träff, fråga Rob istället för att gissa, (3) registrera sedan via monitor_ui_report_arrival enligt ovan (delivery_note_id kan användas för att hämta ordernumret).

Regler:
- Användaren klistrar ofta in rader från Monitor (svensk affärs-ERP). Varje rad innehåller artikelnummer och B-nummer. Din uppgift är då att (1) anropa list_queue, (2) presentera en mappning rad→filnamn som tabell i ditt svar, (3) vänta på "ja" innan du anropar apply_queue_order.
- Ändra ALDRIG filer eller styr Monitor-klienten (update/approve/apply_order/monitor_ui_report_arrival) utan ett uttryckligt ja från användaren i förra meddelandet. För monitor_ui_report_arrival krävs dessutom ett separat ja innan save=true (Ctrl+S).
- Finns ingen följesedel men användaren vill ändå inleverera på en order: använd monitor_ui_report_arrival med order_number direkt (samma förhandsvisning→ja→confirm-flöde).
- En rename — och en inleverans-rad — åt gången, inte bulk.
- Svara på svenska. Korta svar är bättre än långa. Markdown-tabeller är OK.
- Om användaren bara säger hej eller frågar något allmänt, svara utan att kalla verktyg.
- Förbättringslistan är ditt eget anteckningsblock. Om du själv hittar något som borde förbättras med dig (Sickan) eller appen — ett verktyg du saknar, ett återkommande missförstånd, en UI-friktion, ett svar du gav men ångrade — anropa add_improvement DIREKT, utan att fråga. Det är aldrig destruktivt; rådgör inte. Nämn gärna kort i ditt svar att du la till det.`

// Event är vad chat-loopen rapporterar tillbaka under körning.
type Event struct {
	Kind string `json:"kind"` // "text", "tool_call", "tool_result", "tool_error", "done", "error"
	Data string `json:"data"` // för text: delta-token; för tool_*: JSON-payload
}

// EmitFunc skickar ett event till klienten (typiskt SSE).
type EmitFunc func(Event)

// Run kör chat-loopen tills assistenten avslutar eller fel uppstår.
// history är konversationen så här långt (klienten skickar med); funktionen
// returnerar uppdaterad historik (incl. assistant + ev. tool-rounds).
func Run(
	ctx context.Context,
	client *anthropic.Client,
	tb *Toolbox,
	logger ai.Logger,
	model string,
	history []anthropic.MessageParam,
	emit EmitFunc,
) ([]anthropic.MessageParam, error) {
	if model == "" {
		model = ai.ChatDefault
	}
	costKey := ai.ChatCostKey(model)
	for round := 0; round < MaxRounds; round++ {
		if ctx.Err() != nil {
			return history, ctx.Err()
		}
		streamFn := func() (anthropic.Message, bool, error) {
			return streamOnce(ctx, client, model, history, emit)
		}
		syncFn := func() (anthropic.Message, error) {
			return syncOnce(ctx, client, model, history, emit)
		}
		msg, err := runWithFallback(ctx, streamFn, syncFn, logger)
		if err != nil {
			return history, err
		}
		if logger != nil && costKey != "" {
			logger.RecordUsage(costKey,
				msg.Usage.InputTokens, msg.Usage.OutputTokens,
				msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens)
		}

		// Append assistant turn till historik
		history = append(history, msg.ToParam())

		if msg.StopReason != "tool_use" {
			emit(Event{Kind: "done", Data: string(msg.StopReason)})
			return history, nil
		}

		// Kör varje tool_use-block och bygg ett user-meddelande med tool_results.
		var toolResults []anthropic.ContentBlockParamUnion
		for _, block := range msg.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			callPayload, _ := json.Marshal(map[string]any{
				"id":    tu.ID,
				"name":  tu.Name,
				"input": json.RawMessage(tu.Input),
			})
			emit(Event{Kind: "tool_call", Data: string(callPayload)})

			res, err := tb.Dispatch(tu.Name, json.RawMessage(tu.Input))
			if err != nil {
				errPayload, _ := json.Marshal(map[string]any{
					"id":    tu.ID,
					"name":  tu.Name,
					"error": err.Error(),
				})
				emit(Event{Kind: "tool_error", Data: string(errPayload)})
				toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, "Fel: "+err.Error(), true))
				continue
			}
			// Bygg ev. JSON-tolkat resultat för UI:t (försöker parsa first text-block).
			var resultJSON json.RawMessage
			if len(res.Content) > 0 && res.Content[0].OfText != nil {
				if json.Valid([]byte(res.Content[0].OfText.Text)) {
					resultJSON = json.RawMessage(res.Content[0].OfText.Text)
				}
			}
			resPayload, _ := json.Marshal(map[string]any{
				"id":      tu.ID,
				"name":    tu.Name,
				"result":  resultJSON,
				"summary": res.Summary,
			})
			emit(Event{Kind: "tool_result", Data: string(resPayload)})
			toolResults = append(toolResults, anthropic.ContentBlockParamUnion{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: tu.ID,
					Content:   res.Content,
					IsError:   anthropic.Bool(false),
				},
			})
		}
		history = append(history, anthropic.MessageParam{
			Role:    anthropic.MessageParamRoleUser,
			Content: toolResults,
		})
	}
	emit(Event{Kind: "error", Data: "max_rounds_reached"})
	return history, fmt.Errorf("nådde MaxRounds=%d", MaxRounds)
}

// streamOnce kör en enda streaming-runda mot Anthropic. Returnerar den
// ackumulerade msg, en flagga om text-deltas hann emitteras till klienten,
// och eventuellt fel från stream.Err(). Felet är typiskt io.EOF om
// Anthropic-edgen stängde anslutningen mid-stream.
func streamOnce(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	history []anthropic.MessageParam,
	emit EmitFunc,
) (anthropic.Message, bool, error) {
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{{
			Text:         SystemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Tools:    ToolDefs(),
		Messages: history,
	})
	msg := anthropic.Message{}
	var emitted bool
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return msg, emitted, fmt.Errorf("accumulate: %w", err)
		}
		if d, ok := ev.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if td, ok := d.Delta.AsAny().(anthropic.TextDelta); ok && td.Text != "" {
				emit(Event{Kind: "text", Data: td.Text})
				emitted = true
			}
		}
	}
	return msg, emitted, stream.Err()
}

// syncOnce kör en non-streaming variant mot Anthropic. Används som fallback
// när streaming failade utan att hinna emit:a något. Emit:ar alla text-block
// i ett svep så UI:t får hela svaret som ett enda text-event.
func syncOnce(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	history []anthropic.MessageParam,
	emit EmitFunc,
) (anthropic.Message, error) {
	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{{
			Text:         SystemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Tools:    ToolDefs(),
		Messages: history,
	})
	if err != nil {
		return anthropic.Message{}, err
	}
	for _, block := range msg.Content {
		if block.Type == "text" && block.Text != "" {
			emit(Event{Kind: "text", Data: block.Text})
		}
	}
	return *msg, nil
}

// runWithFallback kör streamFn först. Om streaming failar UTAN att hinna
// emit:a text till klienten, faller den tillbaka på syncFn (non-streaming).
// Om text redan emit:ats kan vi inte retrya — det skulle duplicera tokens
// i UI:t — så då bubblar vi felet uppåt med stream-msg:n.
func runWithFallback(
	ctx context.Context,
	streamFn func() (anthropic.Message, bool, error),
	syncFn func() (anthropic.Message, error),
	logger ai.Logger,
) (anthropic.Message, error) {
	if ctx.Err() != nil {
		return anthropic.Message{}, ctx.Err()
	}
	msg, emitted, err := streamFn()
	if err == nil {
		return msg, nil
	}
	if emitted {
		return msg, err
	}
	if logger != nil {
		logger.Logf("🤖 Sickan stream-fel: %v — faller tillbaka på non-streaming", err)
	}
	syncMsg, syncErr := syncFn()
	if syncErr != nil {
		if logger != nil {
			logger.Logf("🤖 Sickan sync-fel: %v", syncErr)
		}
		return syncMsg, syncErr
	}
	return syncMsg, nil
}

// CompactHistory ersätter PDF-dokument-block i äldre tool_result-block med en
// kort text-placeholder så de inte re-skickas i varje följande request. De
// senaste keepLatestPdfs PDF:erna behålls intakta. Modifierar history på plats
// och returnerar samma slice.
func CompactHistory(history []anthropic.MessageParam, keepLatestPdfs int) []anthropic.MessageParam {
	kept := 0
	for i := len(history) - 1; i >= 0; i-- {
		for j := range history[i].Content {
			tr := history[i].Content[j].OfToolResult
			if tr == nil {
				continue
			}
			hasDoc := false
			for _, c := range tr.Content {
				if c.OfDocument != nil {
					hasDoc = true
					break
				}
			}
			if !hasDoc {
				continue
			}
			if kept < keepLatestPdfs {
				kept++
				continue
			}
			tr.Content = []anthropic.ToolResultBlockParamContentUnion{
				{OfText: &anthropic.TextBlockParam{Text: "[PDF tidigare visad — strippad ur historik för att spara tokens]"}},
			}
		}
	}
	return history
}
