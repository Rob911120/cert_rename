package sickan

// Chat-loopen är kopierad ~verbatim från internal/sickan/chat.go (V1) —
// medveten kopia i stället för extraktion: en delad loop-modul skulle tvinga
// in ändringar i V1 (Run tar V1:s *Toolbox konkret). Enda skillnaderna här är
// Toolbox-typen och systemprompten.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/v2/ai"
)

// MaxRounds är taket på antal tool-use-iterationer per användarmeddelande.
const MaxRounds = 12

// buildSystem: cachad grundprompt + (om regler finns) ett OCACHAT block med
// Robs inlärda regler efter cache-brytpunkten.
func buildSystem(rules []string) []anthropic.TextBlockParam {
	system := []anthropic.TextBlockParam{{
		Text:         SystemPrompt,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}}
	if len(rules) > 0 {
		text := "Robs arbetsregler (inlärda — följ dem):\n"
		for _, r := range rules {
			text += "- " + r + "\n"
		}
		system = append(system, anthropic.TextBlockParam{Text: text})
	}
	return system
}

// Event är vad chat-loopen rapporterar tillbaka under körning.
type Event struct {
	Kind string `json:"kind"` // "text", "tool_call", "tool_result", "tool_error", "done", "error"
	Data string `json:"data"`
}

// EmitFunc skickar ett event till klienten (typiskt SSE).
type EmitFunc func(Event)

// Run kör chat-loopen tills assistenten avslutar eller fel uppstår.
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
	system := buildSystem(tb.Rules)
	tools := ToolDefs(tb.ReadOnly)
	for round := 0; round < MaxRounds; round++ {
		if ctx.Err() != nil {
			return history, ctx.Err()
		}
		streamFn := func() (anthropic.Message, bool, error) {
			return streamOnce(ctx, client, model, system, tools, history, emit)
		}
		syncFn := func() (anthropic.Message, error) {
			return syncOnce(ctx, client, model, system, tools, history, emit)
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

		history = append(history, msg.ToParam())

		if msg.StopReason != "tool_use" {
			emit(Event{Kind: "done", Data: string(msg.StopReason)})
			return history, nil
		}

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

func streamOnce(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	system []anthropic.TextBlockParam,
	tools []anthropic.ToolUnionParam,
	history []anthropic.MessageParam,
	emit EmitFunc,
) (anthropic.Message, bool, error) {
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 4096,
		System:    system,
		Tools:     tools,
		Messages:  history,
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

func syncOnce(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	system []anthropic.TextBlockParam,
	tools []anthropic.ToolUnionParam,
	history []anthropic.MessageParam,
	emit EmitFunc,
) (anthropic.Message, error) {
	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 4096,
		System:    system,
		Tools:     tools,
		Messages:  history,
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
// kort placeholder; de senaste keepLatestPdfs behålls intakta.
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
