package app

import (
	"context"
	"testing"

	"cert-renamer/internal/cert"
)

func f64(v float64) *float64 { return &v }

// TestIngestCertPersistsV2Fields bevisar att Task 1+2:s 16 nya extraktionsfält
// (kolumnkalibrerad extraktion: slag-, kemi- och märkningsdata) flödar från
// cert.Extraction hela vägen till DB-raden via IngestCert. Blandning av satta
// och nil-pekare, samt is_legible=false, bevisar att falskt/nil INTE tvingas
// om till true/något-värde på vägen.
func TestIngestCertPersistsV2Fields(t *testing.T) {
	a, _, _ := testApp(t)
	ctx := context.Background()

	ext := &cert.Extraction{
		IsEN10204_3_1: true, CertType: "3.1", Charge: "70703", Material: "S355J2+N",
		EnStandardPresent: true, IsEnglish: true, ProductForm: "plåt", Dimensions: "16",
		Confidence: "high",

		IsLegible:   false, // ska överleva — inte tvingas till true
		IsUnaltered: true,  // skiljer sig från zero-value (false) — bevisar wiring

		NormSystem:    "Charpy",
		ImpactTempC:   f64(-20),
		ImpactEnergyJ: 27.5,
		NormEdition:   "2019",
		PedDirective:  "2014/68/EU",

		Cev:       f64(0.43),
		CarbonPct: nil, // ej angivet på certet — ska förbli nil
		PPct:      f64(0.012),
		SPct:      nil, // ej angivet — ska förbli nil

		HasBendTest:          true,
		HasIntergranularTest: false,
		HasStampPhoto:        true,
		MinTemperatureC:      f64(-40),
		DeliveryCondition:    "normaliserad",
	}

	c, dup, err := a.IngestCert(ctx, IngestInput{
		OriginalFilename: "test.pdf",
		Data:             []byte("%PDF-1.4 v2fields\n"),
		Extraction:       ext,
		BNumbers:         []string{"B999999"},
		Model:            "fake-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("ska inte vara dubblett")
	}

	got, err := a.Repo.GetCert(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.IsLegible != false {
		t.Errorf("IsLegible = %v, vill ha false", got.IsLegible)
	}
	if got.IsUnaltered != true {
		t.Errorf("IsUnaltered = %v, vill ha true", got.IsUnaltered)
	}
	if got.NormSystem != "Charpy" {
		t.Errorf("NormSystem = %q, vill ha %q", got.NormSystem, "Charpy")
	}
	if got.ImpactTempC == nil || *got.ImpactTempC != -20 {
		t.Errorf("ImpactTempC = %v, vill ha -20", got.ImpactTempC)
	}
	if got.ImpactEnergyJ != 27.5 {
		t.Errorf("ImpactEnergyJ = %v, vill ha 27.5", got.ImpactEnergyJ)
	}
	if got.NormEdition != "2019" {
		t.Errorf("NormEdition = %q, vill ha %q", got.NormEdition, "2019")
	}
	if got.PedDirective != "2014/68/EU" {
		t.Errorf("PedDirective = %q, vill ha %q", got.PedDirective, "2014/68/EU")
	}
	if got.Cev == nil || *got.Cev != 0.43 {
		t.Errorf("Cev = %v, vill ha 0.43", got.Cev)
	}
	if got.CarbonPct != nil {
		t.Errorf("CarbonPct = %v, vill ha nil", got.CarbonPct)
	}
	if got.PPct == nil || *got.PPct != 0.012 {
		t.Errorf("PPct = %v, vill ha 0.012", got.PPct)
	}
	if got.SPct != nil {
		t.Errorf("SPct = %v, vill ha nil", got.SPct)
	}
	if !got.HasBendTest {
		t.Error("HasBendTest ska vara true")
	}
	if got.HasIntergranularTest {
		t.Error("HasIntergranularTest ska vara false")
	}
	if !got.HasStampPhoto {
		t.Error("HasStampPhoto ska vara true")
	}
	if got.MinTemperatureC == nil || *got.MinTemperatureC != -40 {
		t.Errorf("MinTemperatureC = %v, vill ha -40", got.MinTemperatureC)
	}
	if got.DeliveryCondition != "normaliserad" {
		t.Errorf("DeliveryCondition = %q, vill ha %q", got.DeliveryCondition, "normaliserad")
	}
}
