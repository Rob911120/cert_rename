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

// TestParseRequirements_FullResponse verifierar att ParseRequirements avkodar
// ett komplett tool_use-svar korrekt och att requesten begär verktyget
// submit_requirements.
func TestParseRequirements_FullResponse(t *testing.T) {
	var sawTool string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Avkoda requesten och läs tool_choice.name — en enkel Contains träffar
		// även verktygsdefinitionen/prompttexten och kan därför aldrig faila.
		var req struct {
			ToolChoice struct {
				Name string `json:"name"`
			} `json:"tool_choice"`
		}
		_ = json.Unmarshal(body, &req)
		sawTool = req.ToolChoice.Name
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-haiku-4-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "submit_requirements",
				"input": {
					"required_material": "S355J2+N",
					"required_en_norm": "EN 10025-2",
					"required_cert_type": "3.1",
					"requires_english": true,
					"required_product_form": "rundstång",
					"required_dimensions": "20",
					"required_impact": "27J/-20°C",
					"notes": "Två stålsorter nämns i texterna, valde den mest specifika"
				}
			}],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 80, "output_tokens": 15}
		}`))
	}))
	defer stub.Close()

	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	in := RequirementsInput{
		Description:      "Rundstång S355J2+N EN 10025-2",
		ExtraDescription: "Cert EN 10204 3.1 in English krävs",
		AlloyCode:        "1.0570",
	}
	got, err := ParseRequirements(context.Background(), nopLogger{}, &client, in)
	if err != nil {
		t.Fatalf("ParseRequirements: %v", err)
	}

	if sawTool != "submit_requirements" {
		t.Errorf("requesten begärde inte submit_requirements (sawTool=%q)", sawTool)
	}

	if got.RequiredMaterial != "S355J2+N" || got.RequiredEnNorm != "EN 10025-2" || got.RequiredCertType != "3.1" {
		t.Errorf("material/norm/cert fel: %+v", got)
	}
	if !got.RequiresEnglish {
		t.Errorf("requires_english skulle vara true, var %v", got.RequiresEnglish)
	}
	if got.RequiredProductForm != "rundstång" || got.RequiredDimensions != "20" {
		t.Errorf("form/dim fel: %+v", got)
	}
	if got.RequiredImpact != "27J/-20°C" {
		t.Errorf("required_impact = %q", got.RequiredImpact)
	}
	if got.Notes == "" {
		t.Errorf("notes borde finnas när något är tvetydigt")
	}
}

// TestParseRequirements_EmptyResponse verifierar att när modellen inte hittar
// några krav (allt tomt/false) så blir det zero-values, inget fel — "gissa
// aldrig"-principen syns som tomma strängar, inte fel.
func TestParseRequirements_EmptyResponse(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-haiku-4-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "submit_requirements",
				"input": {
					"required_material": "",
					"required_en_norm": "",
					"required_cert_type": "",
					"requires_english": false,
					"required_product_form": "",
					"required_dimensions": "",
					"required_impact": "",
					"notes": ""
				}
			}],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 40, "output_tokens": 10}
		}`))
	}))
	defer stub.Close()

	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	in := RequirementsInput{Description: "Ospecificerad artikel"}
	got, err := ParseRequirements(context.Background(), nopLogger{}, &client, in)
	if err != nil {
		t.Fatalf("ParseRequirements: %v", err)
	}

	if got.RequiredMaterial != "" || got.RequiredEnNorm != "" || got.RequiredCertType != "" {
		t.Errorf("strängfält skulle vara tomma: %+v", got)
	}
	if got.RequiresEnglish {
		t.Errorf("requires_english skulle vara false, var %v", got.RequiresEnglish)
	}
	if got.RequiredProductForm != "" || got.RequiredDimensions != "" || got.RequiredImpact != "" || got.Notes != "" {
		t.Errorf("övriga fält skulle vara tomma: %+v", got)
	}
}

// TestParseRequirements_UserMessage verifierar att user-meddelandet bara
// innehåller rubricerade sektioner för icke-tomma inputfält (mindre brus åt
// modellen) — rubriker för tomma fält/mått ska INTE finnas med.
func TestParseRequirements_UserMessage(t *testing.T) {
	var reqBody string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-haiku-4-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "submit_requirements",
				"input": {
					"required_material": "",
					"required_en_norm": "",
					"required_cert_type": "",
					"requires_english": false,
					"required_product_form": "",
					"required_dimensions": "",
					"required_impact": "",
					"notes": ""
				}
			}],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 40, "output_tokens": 10}
		}`))
	}))
	defer stub.Close()

	client := anthropic.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(stub.URL),
		option.WithMaxRetries(0),
	)
	// Bara Description och ExtraDescription satta. Resten (inkl. alla tre
	// måtten) tomma/0 — deras rubriker ska inte synas i requesten.
	in := RequirementsInput{
		Description:      "Rundstång 20mm",
		ExtraDescription: "S355J2+N, cert EN 10204 3.1 in English krävs",
		PartLength:       0,
		PartWidth:        0,
		PartHeight:       0,
	}
	if _, err := ParseRequirements(context.Background(), nopLogger{}, &client, in); err != nil {
		t.Fatalf("ParseRequirements: %v", err)
	}

	if !strings.Contains(reqBody, "submit_requirements") {
		t.Errorf("requesten begärde inte submit_requirements")
	}
	if !strings.Contains(reqBody, "Rundstång 20mm") {
		t.Errorf("user-meddelandet innehöll inte den icke-tomma beskrivningen")
	}
	if !strings.Contains(reqBody, "S355J2+N, cert EN 10204 3.1 in English kr") {
		t.Errorf("user-meddelandet innehöll inte den icke-tomma extra beskrivningen")
	}

	// Rubriker för tomma fält ska INTE finnas.
	emptyHeadings := []string{
		"GODSMEDDELANDE",
		"KONTROLLINSTRUKTION",
		"MOTTAGNINGSINSTRUKTION",
		"GODSMÄRKE",
		"ANTECKNINGAR",
		"FRITEXT",
		"EXTERN KOMMENTAR",
		"MATERIALKVALITETSREGISTER",
		"MÅTT",
	}
	for _, h := range emptyHeadings {
		if strings.Contains(reqBody, h) {
			t.Errorf("user-meddelandet innehöll rubriken %q trots att fältet var tomt", h)
		}
	}
}
