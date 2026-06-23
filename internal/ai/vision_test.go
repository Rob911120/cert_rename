package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type nopLogger struct{}

func (nopLogger) Logf(string, ...any)                 {}
func (nopLogger) RecordUsage(string, int64, int64, int64, int64) {}

func TestExtractFromImage(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "extract_delivery_note",
				"input": {
					"supplier": "BE Group",
					"delivery_date": "2026-06-20",
					"order_number": "B127196",
					"b_numbers": ["B127196"],
					"charge": "610042",
					"material": "S355J2",
					"quantity": 6,
					"unit": "st",
					"delivery_note_number": "CCF000195",
					"confidence": "high"
				}
			}],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 100, "output_tokens": 20}
		}`))
	}))
	defer stub.Close()

	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	ext, err := ExtractFromImage(context.Background(), nopLogger{}, &client, []byte("fakeimagebytes"), "image/png")
	if err != nil {
		t.Fatalf("ExtractFromImage: %v", err)
	}
	if ext.Supplier != "BE Group" || ext.OrderNumber != "B127196" || ext.Charge != "610042" {
		t.Errorf("ext = %+v", ext)
	}
	if ext.Quantity != 6 || ext.Unit != "st" {
		t.Errorf("kvantitet/enhet fel: %+v", ext)
	}
	if ext.DeliveryNoteNumber != "CCF000195" {
		t.Errorf("följesedelsnr = %q", ext.DeliveryNoteNumber)
	}
	if len(ext.BNumbers) != 1 || ext.BNumbers[0] != "B127196" {
		t.Errorf("b_numbers = %v", ext.BNumbers)
	}
}

func TestExtractFromImage_RejectsBadMediaType(t *testing.T) {
	client := anthropic.NewClient(option.WithAPIKey("test"))
	if _, err := ExtractFromImage(context.Background(), nopLogger{}, &client, []byte("x"), "application/pdf"); err == nil {
		t.Error("förväntade fel för icke-bild media type")
	}
}
