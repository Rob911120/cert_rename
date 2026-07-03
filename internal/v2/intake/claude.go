package intake

import (
	"context"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
)

// Claude implementerar AI-porten mot riktiga internal/ai-anrop. Base-loggern
// får all logg + global kostnadsräkning; Extract fångar dessutom tokenåtgång
// per anrop (en capture per anrop — ingen delad räknar-state, inga races).
type Claude struct {
	Client *anthropic.Client
	Base   ai.Logger
}

func NewClaude(apiKey string, base ai.Logger) *Claude {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Claude{Client: &client, Base: base}
}

func (c *Claude) MailCategory(ctx context.Context, m *eml.Content) (*ai.MailClassification, error) {
	return ai.ClassifyMailCategory(ctx, c.Base, c.Client, m)
}

func (c *Claude) Classify(ctx context.Context, m *eml.Content) (*cert.Classification, error) {
	return ai.Classify(ctx, c.Base, c.Client, m)
}

func (c *Claude) Verify(ctx context.Context, m *eml.Content) (*cert.Verification, error) {
	return ai.Verify(ctx, c.Base, c.Client, m)
}

func (c *Claude) Extract(ctx context.Context, pdf []byte, subject, body, filename string) (*ExtractResult, error) {
	capture := &usageCapture{base: c.Base}
	start := time.Now()
	ext, err := ai.Extract(ctx, capture, c.Client, pdf, subject, body, filename)
	if err != nil {
		return nil, err
	}
	return &ExtractResult{
		Extraction: ext,
		Model:      ai.ModelExtract,
		TokensIn:   capture.in,
		TokensOut:  capture.out,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// usageCapture summerar tokenåtgång för ETT anrop och vidarebefordrar allt
// till bas-loggern (som sköter global kostnadsräkning).
type usageCapture struct {
	base    ai.Logger
	in, out int64
}

func (u *usageCapture) Logf(format string, args ...any) { u.base.Logf(format, args...) }

func (u *usageCapture) RecordUsage(model string, in, out, cacheCreation, cacheRead int64) {
	u.in += in
	u.out += out
	u.base.RecordUsage(model, in, out, cacheCreation, cacheRead)
}
