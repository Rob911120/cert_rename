// Package ai kapslar Claude-anropen för cert-renamer: klassificering av mejl,
// verifiering av bifogade PDF:er, samt fält-extraktion från en cert-PDF.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
)

// Extract anropar sonnet med en PDF + email-context och returnerar extraherade fält.
func Extract(ctx context.Context, log Logger, client *anthropic.Client, pdf []byte, subject, body, filename string) (*cert.Extraction, error) {
	b64 := base64.StdEncoding.EncodeToString(pdf)
	userText := fmt.Sprintf("Extrahera fält från detta certifikat.\n\nMejlets ämnesrad: %s\nBilagans originalfilnamn: %s\n\nMejlets brödtext:\n%s\n",
		subject, filename, body)
	return logAICall(log, "sonnet Extract("+filename+")",
		func() (*cert.Extraction, anthropic.Usage, error) {
			resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
				Model:     ModelExtract,
				MaxTokens: 1024,
				System:    []anthropic.TextBlockParam{{Text: extractSystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &extractionTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "submit_extraction"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
								Data: b64, MediaType: "application/pdf",
							}),
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
			if err != nil {
				return nil, anthropic.Usage{}, err
			}
			for _, block := range resp.Content {
				if block.Type == "tool_use" {
					tu := block.AsToolUse()
					var ext cert.Extraction
					if err := json.Unmarshal(tu.Input, &ext); err != nil {
						return nil, resp.Usage, fmt.Errorf("unmarshal: %w", err)
					}
					return &ext, resp.Usage, nil
				}
			}
			return nil, resp.Usage, fmt.Errorf("inget tool_use-svar från Claude")
		},
		func(ext *cert.Extraction) string {
			return fmt.Sprintf("type=%s charge=%s mat=%s", ext.CertType, ext.Charge, ext.MaterialShort)
		},
	)
}

// Classify anropar haiku och avgör om mejlet är ett cert-mejl.
func Classify(ctx context.Context, log Logger, client *anthropic.Client, c *eml.Content) (*cert.Classification, error) {
	var attNames []string
	for _, a := range c.Attachments {
		attNames = append(attNames, a.Filename)
	}
	body := c.Body
	if len(body) > eml.MaxBodyBytes {
		body = body[:eml.MaxBodyBytes] + "\n[trunkerad]"
	}
	userText := fmt.Sprintf("Subject: %s\nFrom: %s\nDate: %s\nBilagor: %s\n\nBody:\n%s",
		c.Subject, c.From, c.Date, strings.Join(attNames, ", "), body)
	return logAICall(log, "haiku Classify",
		func() (*cert.Classification, anthropic.Usage, error) {
			resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: classifySystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &classifyTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "classify_email"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							{OfText: &anthropic.TextBlockParam{Text: userText}},
						},
					},
				},
			})
			if err != nil {
				return nil, anthropic.Usage{}, err
			}
			for _, block := range resp.Content {
				if block.Type == "tool_use" {
					tu := block.AsToolUse()
					var cls cert.Classification
					if err := json.Unmarshal(tu.Input, &cls); err != nil {
						return nil, resp.Usage, fmt.Errorf("unmarshal: %w", err)
					}
					return &cls, resp.Usage, nil
				}
			}
			return nil, resp.Usage, fmt.Errorf("inget tool_use-svar från Claude")
		},
		func(cls *cert.Classification) string {
			return fmt.Sprintf("cert=%t conf=%s — %s", cls.IsCertMail, cls.Confidence, cls.Reason)
		},
	)
}

// Verify anropar haiku på alla PDF-bilagor och avgör om någon är ett 3.1-cert.
func Verify(ctx context.Context, log Logger, client *anthropic.Client, c *eml.Content) (*cert.Verification, error) {
	content := []anthropic.ContentBlockParamUnion{}
	for _, att := range c.Attachments {
		b64 := base64.StdEncoding.EncodeToString(att.Data)
		content = append(content, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
			Data: b64, MediaType: "application/pdf",
		}))
	}
	content = append(content, anthropic.ContentBlockParamUnion{
		OfText: &anthropic.TextBlockParam{Text: "Är någon av dessa PDF:er ett 3.1-stålcert? Returnera via verktyget."},
	})
	return logAICall(log, fmt.Sprintf("haiku Verify(%d pdf)", len(c.Attachments)),
		func() (*cert.Verification, anthropic.Usage, error) {
			resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
				Model:     ModelClassify,
				MaxTokens: 256,
				System:    []anthropic.TextBlockParam{{Text: verifySystemPrompt}},
				Tools:     []anthropic.ToolUnionParam{{OfTool: &verifyTool}},
				ToolChoice: anthropic.ToolChoiceUnionParam{
					OfTool: &anthropic.ToolChoiceToolParam{Name: "verify_pdfs"},
				},
				Messages: []anthropic.MessageParam{
					{
						Role:    anthropic.MessageParamRoleUser,
						Content: content,
					},
				},
			})
			if err != nil {
				return nil, anthropic.Usage{}, err
			}
			for _, block := range resp.Content {
				if block.Type == "tool_use" {
					tu := block.AsToolUse()
					var ver cert.Verification
					if err := json.Unmarshal(tu.Input, &ver); err != nil {
						return nil, resp.Usage, fmt.Errorf("unmarshal: %w", err)
					}
					return &ver, resp.Usage, nil
				}
			}
			return nil, resp.Usage, fmt.Errorf("inget tool_use-svar från Claude")
		},
		func(ver *cert.Verification) string {
			return fmt.Sprintf("any_cert=%t — %s", ver.AnyIsCert, ver.Reason)
		},
	)
}
