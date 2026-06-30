package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Ren AI-dom kan inte verifieras deterministiskt (avsiktligt Robs val); testet
// säkrar plumbingen: tvingat verktyg judge_material, thinking avstängt
// (Sonnet 5 avvisar icke-default temperature med 400), och att alla
// dom-/evidens-fält plockas ur svaret.
func TestClassifyUpcoming(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if tc, ok := req["tool_choice"].(map[string]any); !ok || tc["name"] != "judge_material" {
			t.Errorf("tool_choice = %v, vill ha judge_material", req["tool_choice"])
		}
		if th, ok := req["thinking"].(map[string]any); !ok || th["type"] != "disabled" {
			t.Errorf("thinking = %v, vill ha {type: disabled}", req["thinking"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg","type":"message","role":"assistant","model":"claude-sonnet-4-5",
			"content":[{"type":"tool_use","id":"t","name":"judge_material","input":{
				"required_material":"S355J2",
				"required_cert":"3.1",
				"our_material":"S275JR",
				"material_ok":"mismatch",
				"notes":"Beställt S355J2 men certet är S275JR"
			}}],
			"stop_reason":"tool_use","usage":{"input_tokens":50,"output_tokens":15}
		}`))
	}))
	defer stub.Close()

	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	in := UpcomingClassifyInput{
		PartNumber:       "PL-10",
		Description:      "Plåt 10mm",
		ExtraDescription: "S355J2 +N, cert 3.1",
		CertRequired:     true,
		CertMaterial:     "S275JR",
		CertType:         "3.1",
		CertDimensions:   "10",
	}
	got, err := ClassifyUpcoming(context.Background(), nopLogger{}, &client, in)
	if err != nil {
		t.Fatalf("ClassifyUpcoming: %v", err)
	}
	if got.MaterialOK != "mismatch" {
		t.Errorf("material_ok = %q, vill ha mismatch", got.MaterialOK)
	}
	if got.RequiredMaterial != "S355J2" || got.OurMaterial != "S275JR" || got.RequiredCert != "3.1" {
		t.Errorf("dom-fält fel: %+v", got)
	}
	if got.Notes == "" {
		t.Errorf("notes (evidens-text) borde finnas")
	}
}
