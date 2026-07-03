package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestExtractV2_FullResponse verifierar att ExtractV2 avkodar ett komplett
// tool_use-svar korrekt, inklusive alla nya nullable-fält (pekare derefererade)
// och booleans. Bekräftar också att requesten begär verktyget submit_extraction_v2.
func TestExtractV2_FullResponse(t *testing.T) {
	var sawTool string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "submit_extraction_v2") {
			sawTool = "submit_extraction_v2"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "submit_extraction_v2",
				"input": {
					"is_en10204_3_1": true,
					"cert_type": "3.1",
					"charge": "68667E3",
					"material": "SA-516-70",
					"en_standard_present": true,
					"is_english": true,
					"is_legible": false,
					"is_unaltered": true,
					"product_form": "plåt",
					"dimensions": "20",
					"country_of_origin": "Sweden",
					"norm_system": "ASME",
					"norm_edition": "2019",
					"ped_directive": "2014/68/EU",
					"impact_temp_c": -20,
					"impact_energy_j": 27,
					"cev": 0.45,
					"carbon_pct": 0.12,
					"p_pct": 0.01,
					"s_pct": 0.002,
					"has_bend_test": true,
					"has_intergranular_test": true,
					"has_stamp_photo": true,
					"min_temperature_c": -40,
					"delivery_condition": "+N",
					"confidence": "high",
					"issues": ["OBS: avskuret parti"]
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
	ext, err := ExtractV2(context.Background(), nopLogger{}, &client, []byte("fakepdfbytes"), "Cert S355", "brödtext", "S355-20-68667E3.pdf")
	if err != nil {
		t.Fatalf("ExtractV2: %v", err)
	}

	if sawTool != "submit_extraction_v2" {
		t.Errorf("requesten begärde inte submit_extraction_v2 (sawTool=%q)", sawTool)
	}

	// V1-fält
	if !ext.IsEN10204_3_1 || ext.CertType != "3.1" || ext.Charge != "68667E3" || ext.Material != "SA-516-70" {
		t.Errorf("V1-fält fel: %+v", ext)
	}
	if !ext.EnStandardPresent || !ext.IsEnglish {
		t.Errorf("en_standard_present/is_english fel: %+v", ext)
	}
	if ext.ProductForm != "plåt" || ext.Dimensions != "20" || ext.CountryOfOrigin != "Sweden" || ext.Confidence != "high" {
		t.Errorf("form/dim/land/conf fel: %+v", ext)
	}
	if len(ext.Issues) != 1 || ext.Issues[0] != "OBS: avskuret parti" {
		t.Errorf("issues fel: %v", ext.Issues)
	}

	// Nya booleans
	if ext.IsLegible {
		t.Errorf("is_legible skulle vara false, var %v", ext.IsLegible)
	}
	if !ext.IsUnaltered || !ext.HasBendTest || !ext.HasIntergranularTest || !ext.HasStampPhoto {
		t.Errorf("nya booleans fel: %+v", ext)
	}

	// Nya strängar
	if ext.NormSystem != "ASME" || ext.NormEdition != "2019" || ext.PedDirective != "2014/68/EU" || ext.DeliveryCondition != "+N" {
		t.Errorf("nya strängar fel: %+v", ext)
	}

	// impact_energy_j är vanlig float64
	if ext.ImpactEnergyJ != 27 {
		t.Errorf("impact_energy_j = %v, ville ha 27", ext.ImpactEnergyJ)
	}

	// Pekartal — derefererade värden
	if ext.ImpactTempC == nil || *ext.ImpactTempC != -20 {
		t.Errorf("impact_temp_c = %v, ville ha -20", ext.ImpactTempC)
	}
	if ext.Cev == nil || *ext.Cev != 0.45 {
		t.Errorf("cev = %v, ville ha 0.45", ext.Cev)
	}
	if ext.CarbonPct == nil || *ext.CarbonPct != 0.12 {
		t.Errorf("carbon_pct = %v, ville ha 0.12", ext.CarbonPct)
	}
	if ext.PPct == nil || *ext.PPct != 0.01 {
		t.Errorf("p_pct = %v, ville ha 0.01", ext.PPct)
	}
	if ext.SPct == nil || *ext.SPct != 0.002 {
		t.Errorf("s_pct = %v, ville ha 0.002", ext.SPct)
	}
	if ext.MinTemperatureC == nil || *ext.MinTemperatureC != -40 {
		t.Errorf("min_temperature_c = %v, ville ha -40", ext.MinTemperatureC)
	}
}

// TestExtractV2_MinimalResponse verifierar att när modellen bara returnerar
// V1-fälten så blir pekartalen nil (ej angivet ≠ 0), impact_energy_j==0,
// nya strängar tomma och nya booleans false.
func TestExtractV2_MinimalResponse(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-5",
			"content": [{
				"type": "tool_use",
				"id": "toolu_test",
				"name": "submit_extraction_v2",
				"input": {
					"is_en10204_3_1": true,
					"cert_type": "3.1",
					"charge": "610042",
					"material": "S355J2+N EN 10025-2",
					"en_standard_present": true,
					"is_english": true,
					"product_form": "plåt",
					"dimensions": "16",
					"country_of_origin": "",
					"confidence": "medium",
					"issues": []
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
	ext, err := ExtractV2(context.Background(), nopLogger{}, &client, []byte("fakepdfbytes"), "Cert", "brödtext", "cert.pdf")
	if err != nil {
		t.Fatalf("ExtractV2: %v", err)
	}

	// Pekartalen ska vara nil när fälten utelämnas ("ej angivet" ≠ 0)
	if ext.ImpactTempC != nil {
		t.Errorf("impact_temp_c skulle vara nil, var %v", *ext.ImpactTempC)
	}
	if ext.Cev != nil {
		t.Errorf("cev skulle vara nil, var %v", *ext.Cev)
	}
	if ext.CarbonPct != nil {
		t.Errorf("carbon_pct skulle vara nil, var %v", *ext.CarbonPct)
	}
	if ext.PPct != nil {
		t.Errorf("p_pct skulle vara nil, var %v", *ext.PPct)
	}
	if ext.SPct != nil {
		t.Errorf("s_pct skulle vara nil, var %v", *ext.SPct)
	}
	if ext.MinTemperatureC != nil {
		t.Errorf("min_temperature_c skulle vara nil, var %v", *ext.MinTemperatureC)
	}

	// impact_energy_j är vanlig float64: utelämnad → 0
	if ext.ImpactEnergyJ != 0 {
		t.Errorf("impact_energy_j = %v, ville ha 0", ext.ImpactEnergyJ)
	}

	// Nya strängar tomma
	if ext.NormSystem != "" || ext.NormEdition != "" || ext.PedDirective != "" || ext.DeliveryCondition != "" {
		t.Errorf("nya strängar skulle vara tomma: %+v", ext)
	}

	// Nya booleans false
	if ext.IsLegible || ext.IsUnaltered || ext.HasBendTest || ext.HasIntergranularTest || ext.HasStampPhoto {
		t.Errorf("nya booleans skulle vara false: %+v", ext)
	}
}
