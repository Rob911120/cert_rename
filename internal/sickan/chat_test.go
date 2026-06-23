package sickan

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestCompactHistory_StripsOlderPdfs(t *testing.T) {
	mkPdfResult := func(id string) anthropic.MessageParam {
		return anthropic.MessageParam{
			Role: anthropic.MessageParamRoleUser,
			Content: []anthropic.ContentBlockParamUnion{{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: id,
					Content: []anthropic.ToolResultBlockParamContentUnion{
						{OfText: &anthropic.TextBlockParam{Text: "PDF: " + id}},
						{OfDocument: &anthropic.DocumentBlockParam{
							Source: anthropic.DocumentBlockParamSourceUnion{
								OfBase64: &anthropic.Base64PDFSourceParam{
									Data:      "AAAA",
									MediaType: "application/pdf",
								},
							},
						}},
					},
					IsError: anthropic.Bool(false),
				},
			}},
		}
	}

	history := []anthropic.MessageParam{
		mkPdfResult("first"),
		{Role: anthropic.MessageParamRoleAssistant, Content: []anthropic.ContentBlockParamUnion{{OfText: &anthropic.TextBlockParam{Text: "ok"}}}},
		mkPdfResult("second"),
	}

	out := CompactHistory(history, 1)

	// Senaste PDF (index 2) ska vara intakt
	last := out[2].Content[0].OfToolResult
	if last == nil || len(last.Content) != 2 || last.Content[1].OfDocument == nil {
		t.Fatalf("senaste PDF skulle ha varit intakt, fick %+v", last)
	}

	// Första PDF (index 0) ska vara strippad till en text-placeholder
	first := out[0].Content[0].OfToolResult
	if first == nil || len(first.Content) != 1 || first.Content[0].OfDocument != nil {
		t.Fatalf("äldre PDF skulle ha strippats, fick %+v", first)
	}
	if first.Content[0].OfText == nil || !strings.Contains(first.Content[0].OfText.Text, "strippad") {
		t.Fatalf("placeholder-text saknas: %+v", first.Content[0].OfText)
	}
}
