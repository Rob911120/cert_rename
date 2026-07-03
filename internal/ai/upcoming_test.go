package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// userTextOf plockar ut user-meddelandets text ur en fångad request-body, så
// asserterna gäller PROMPTEN till modellen (inte systemtexten, som numera
// nämner PARSADE KRAV i den additiva instruktionen).
func userTextOf(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("avkoda request-body: %v", err)
	}
	if len(req.Messages) == 0 || len(req.Messages[0].Content) == 0 {
		t.Fatalf("ingen user-text i bodyn: %s", body)
	}
	return req.Messages[0].Content[0].Text
}

// stubUpcoming svarar med ett fast judge_material och fångar request-bodyn.
func stubUpcoming(t *testing.T, capture *string) *anthropic.Client {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*capture = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg","type":"message","role":"assistant","model":"claude-sonnet-4-5",
			"content":[{"type":"tool_use","id":"t","name":"judge_material","input":{
				"required_material":"S355J2","required_cert":"3.1","our_material":"S355J2",
				"material_ok":"ok","required_product_form":"plåt","product_form_ok":"ok","notes":"x"
			}}],
			"stop_reason":"tool_use","usage":{"input_tokens":50,"output_tokens":15}
		}`))
	}))
	t.Cleanup(stub.Close)
	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	return &client
}

// TestClassifyUpcoming_NewInputFieldsReachRequest säkrar att Task 9:s additiva
// inputfält (parsade krav + certkolumner) faktiskt hamnar i user-meddelandet
// när de är satta — annars kan cachenyckeln och prompten glida isär.
func TestClassifyUpcoming_NewInputFieldsReachRequest(t *testing.T) {
	var body string
	client := stubUpcoming(t, &body)
	temp := -20.0
	in := UpcomingClassifyInput{
		PartNumber: "PL-10", Description: "Plåt 10mm", CertRequired: true,
		CertMaterial: "S355J2", CertType: "3.1",
		ReqMaterial: "S355J2+N", ReqEnNorm: "EN 10025-2", ReqCertType: "3.1",
		ReqProductForm: "plåt", ReqDimensions: "10", ReqImpact: "27J/-20°C",
		CertNormSystem: "EN", CertNormEdition: "2019", CertDeliveryCondition: "+N",
		CertImpactTempC: &temp, CertImpactEnergyJ: 32, CertIsEnglish: true,
	}
	if _, err := ClassifyUpcoming(context.Background(), nopLogger{}, client, in); err != nil {
		t.Fatalf("ClassifyUpcoming: %v", err)
	}
	userText := userTextOf(t, body)
	for _, want := range []string{"PARSADE KRAV", "S355J2+N", "EN 10025-2", "27J/-20", "CERTETS KOLUMNER", "+N", "2019"} {
		if !strings.Contains(userText, want) {
			t.Errorf("user-meddelandet saknar %q", want)
		}
	}
}

// TestClassifyUpcoming_V1EmptyFieldsOmitSections bevisar V1-kompatibiliteten:
// när de additiva fälten är tomma (som V1 alltid skickar) finns varken PARSADE
// KRAV- eller CERTETS KOLUMNER-avsnittet med — samma meddelande som förr.
func TestClassifyUpcoming_V1EmptyFieldsOmitSections(t *testing.T) {
	var body string
	client := stubUpcoming(t, &body)
	in := UpcomingClassifyInput{ // exakt V1:s uppsättning
		PartNumber: "PL-10", Description: "Plåt 10mm", ExtraDescription: "S355J2 +N",
		CertRequired: true, CertMaterial: "S355J2", CertType: "3.1", CertDimensions: "10",
	}
	if _, err := ClassifyUpcoming(context.Background(), nopLogger{}, client, in); err != nil {
		t.Fatalf("ClassifyUpcoming: %v", err)
	}
	userText := userTextOf(t, body)
	if strings.Contains(userText, "PARSADE KRAV") || strings.Contains(userText, "CERTETS KOLUMNER") {
		t.Errorf("tomma additiva fält gav ändå ett tilläggsavsnitt i user-meddelandet: %s", userText)
	}
}
