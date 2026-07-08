package domain

import (
	"errors"
	"reflect"
	"testing"
)

func TestCertCanTransition(t *testing.T) {
	cases := []struct {
		from, to CertStatus
		want     bool
	}{
		{CertMottagen, CertSparad, true},
		{CertMottagen, CertArkiverad, true},
		{CertArkiverad, CertMottagen, true},
		{CertSparad, CertMottagen, false}, // sparad är terminal
		{CertSparad, CertArkiverad, false},
		{CertArkiverad, CertSparad, false}, // måste ångras till mottagen först
		{CertMottagen, CertMottagen, false},
	}
	for _, tc := range cases {
		if got := CertCanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CertCanTransition(%s→%s) = %v, vill ha %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestLinkCanTransition(t *testing.T) {
	cases := []struct {
		from, to LinkStatus
		want     bool
	}{
		{LinkForeslagen, LinkBekraftad, true},
		{LinkForeslagen, LinkAvfardad, true},
		{LinkBekraftad, LinkAvfardad, true},
		{LinkAvfardad, LinkBekraftad, true}, // återuppliva
		{LinkBekraftad, LinkForeslagen, false},
		{LinkAvfardad, LinkForeslagen, false},
	}
	for _, tc := range cases {
		if got := LinkCanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("LinkCanTransition(%s→%s) = %v, vill ha %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestEffectiveFallbacks(t *testing.T) {
	c := &Cert{Material: "S355J2+N", Charge: "43136", BNumbers: []string{"B128293"}}
	if got := c.EffectiveMaterial(); got != "S355J2+N" {
		t.Errorf("orättad: EffectiveMaterial = %q", got)
	}
	c.CorrectedMaterial = "S690QL"
	if got := c.EffectiveMaterial(); got != "S690QL" {
		t.Errorf("rättad: EffectiveMaterial = %q", got)
	}
	if got := c.EffectiveBNumbers(); !reflect.DeepEqual(got, []string{"B128293"}) {
		t.Errorf("orättade B-nummer = %v", got)
	}
	c.CorrectedBNumbers = []string{} // rättad till "inga"
	if got := c.EffectiveBNumbers(); len(got) != 0 {
		t.Errorf("rättad-till-inga B-nummer = %v", got)
	}
}

func TestProposedFilenamePrecedence(t *testing.T) {
	base := func() *Cert {
		return &Cert{
			Status:     CertMottagen,
			CertType:   "3.1",
			Charge:     "70703",
			Material:   "S690QL",
			Dimensions: "16",
			BNumbers:   []string{"B127562"},
		}
	}

	t.Run("rått: extraherade B-nummer", func(t *testing.T) {
		got := ProposedFilename(base(), nil)
		want := "70703-16-S690QL-B127562.pdf"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("bekräftade länkar vinner över rått", func(t *testing.T) {
		got := ProposedFilename(base(), []string{"B999999", "B111111", "B999999"})
		want := "70703-16-S690QL-B111111-B999999.pdf" // sorterat + dedupat
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("corrected_b_numbers vinner över länkar", func(t *testing.T) {
		c := base()
		c.CorrectedBNumbers = []string{"B555555"}
		got := ProposedFilename(c, []string{"B999999"})
		want := "70703-16-S690QL-B555555.pdf"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("override vinner över allt och saniteras", func(t *testing.T) {
		c := base()
		c.CorrectedBNumbers = []string{"B555555"}
		c.NameOverride = `70703/A:B<test>`
		got := ProposedFilename(c, []string{"B999999"})
		want := "70703-A_B_test_.pdf"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("override med .pdf dubbleras inte", func(t *testing.T) {
		c := base()
		c.NameOverride = "mitt-namn.PDF"
		if got := ProposedFilename(c, nil); got != "mitt-namn.PDF" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("rättade fält flyttar namnet", func(t *testing.T) {
		c := base()
		c.CorrectedMaterial = "S355J2+N"
		c.CorrectedDimensions = "60"
		got := ProposedFilename(c, nil)
		want := "70703-60-S355J2+N-B127562.pdf"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("product_code bygger formsegmentet", func(t *testing.T) {
		c := base()
		c.ProductForm = "rundstång"
		c.ProductCode = "RS"
		got := ProposedFilename(c, nil)
		want := "70703-RS-16-S690QL-B127562.pdf"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestApplyCorrection(t *testing.T) {
	c := &Cert{Status: CertMottagen, Material: "S355J2+N"}

	if err := ApplyCorrection(c, "material", "S690QL", "sickan", "2026-07-03T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if c.CorrectedMaterial != "S690QL" {
		t.Errorf("CorrectedMaterial = %q", c.CorrectedMaterial)
	}
	if len(c.CorrectionLog) != 1 {
		t.Fatalf("CorrectionLog längd = %d", len(c.CorrectionLog))
	}
	entry := c.CorrectionLog[0]
	if entry.Who != "sickan" || entry.Field != "material" || entry.Old != "S355J2+N" || entry.New != "S690QL" {
		t.Errorf("loggpost = %+v", entry)
	}

	// Andra rättelsen: old = föregående effektiva värde, loggen växer
	if err := ApplyCorrection(c, "material", "S700MC", "rob", "2026-07-03T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if len(c.CorrectionLog) != 2 || c.CorrectionLog[1].Old != "S690QL" {
		t.Errorf("append-only-logg bruten: %+v", c.CorrectionLog)
	}

	// b_numbers som kommaseparerad lista
	if err := ApplyCorrection(c, "b_numbers", "B111111, B222222", "rob", "t"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.CorrectedBNumbers, []string{"B111111", "B222222"}) {
		t.Errorf("CorrectedBNumbers = %v", c.CorrectedBNumbers)
	}

	// Tomt värde RENSAR b_numbers-rättelsen (nil = tillbaka till rå
	// extraktion), samma semantik som skalärfälten.
	if err := ApplyCorrection(c, "b_numbers", "", "rob", "t"); err != nil {
		t.Fatal(err)
	}
	if c.CorrectedBNumbers != nil {
		t.Errorf("tomt värde ska rensa rättelsen, fick %#v", c.CorrectedBNumbers)
	}

	// product_code redigeras direkt (ingen corrected-tvilling) men loggas ändå
	c.ProductCode = "RS"
	if err := ApplyCorrection(c, "product_code", "PL", "rob", "t2"); err != nil {
		t.Fatal(err)
	}
	if c.ProductCode != "PL" {
		t.Errorf("ProductCode = %q, vill ha PL", c.ProductCode)
	}
	last := c.CorrectionLog[len(c.CorrectionLog)-1]
	if last.Field != "product_code" || last.Old != "RS" || last.New != "PL" {
		t.Errorf("product_code-loggpost = %+v", last)
	}

	// Okänt fält avvisas
	if err := ApplyCorrection(c, "hemligt", "x", "rob", "t"); err == nil {
		t.Error("okänt fält accepterades")
	}

	// Fryst cert avvisas med ErrFrozen
	c.Status = CertSparad
	if err := ApplyCorrection(c, "material", "X", "rob", "t"); !errors.Is(err, ErrFrozen) {
		t.Errorf("fryst cert: err = %v, vill ha ErrFrozen", err)
	}
}

func TestEffectiveExtraction(t *testing.T) {
	c := &Cert{
		Status: CertMottagen, CertType: "2.2", Charge: "111", Material: "X",
		EnStandardPresent: true, IsEnglish: true,
	}
	if c.EffectiveExtraction().IsEN10204_3_1 {
		t.Error("2.2 klassades som 3.1")
	}
	c.CorrectedCertType = "3.1"
	ext := c.EffectiveExtraction()
	if !ext.IsEN10204_3_1 || ext.CertType != "3.1" {
		t.Errorf("rättad cert-typ: %+v", ext)
	}
}
