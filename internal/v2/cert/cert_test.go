package cert

import (
	"strings"
	"testing"
)

func Test_BuildFilename_HappyPath(t *testing.T) {
	ext := &Extraction{
		Charge:      "610042",
		Material:    "1.4307",
		ProductForm: "rundstång",
		Dimensions:  "5",
	}
	got := BuildFilename(ext, []string{"B127196"})
	want := "610042-rundstang-5-1.4307-B127196.pdf"
	if got != want {
		t.Errorf("BuildFilename = %q, vill ha %q", got, want)
	}
}

func Test_BuildFilename_UsesProductCodeOverForm(t *testing.T) {
	// När product_code (förkortningen) är satt ska den ersätta den råa
	// product_form-texten i formsegmentet.
	ext := &Extraction{
		Charge:      "610042",
		Material:    "1.4307",
		ProductForm: "rundstång",
		ProductCode: "RS",
		Dimensions:  "5",
	}
	got := BuildFilename(ext, []string{"B127196"})
	want := "610042-RS-5-1.4307-B127196.pdf"
	if got != want {
		t.Errorf("BuildFilename = %q, vill ha %q", got, want)
	}
}

func Test_BuildFilename_FallsBackToFormWhenNoCode(t *testing.T) {
	// Tom product_code → formsegmentet faller tillbaka på foldad product_form
	// (bakåtkompat för gamla/importerade cert utan kod).
	ext := &Extraction{
		Charge:      "610042",
		Material:    "1.4307",
		ProductForm: "rundstång",
		ProductCode: "",
		Dimensions:  "5",
	}
	got := BuildFilename(ext, []string{"B127196"})
	want := "610042-rundstang-5-1.4307-B127196.pdf"
	if got != want {
		t.Errorf("BuildFilename = %q, vill ha %q", got, want)
	}
}

func Test_BuildFilename_FoldsSwedishCode(t *testing.T) {
	// Koder med svenska tecken (RÄ=räls, ÄR=ämnesrör, ÖS=övrig stång) ska
	// ASCII-foldas i filnamnet precis som formtexten (Windows/Outlook-kompat).
	ext := &Extraction{Charge: "C1", Material: "S355", ProductCode: "RÄ", Dimensions: "60"}
	got := BuildFilename(ext, []string{"B2"})
	want := "C1-RA-60-S355-B2.pdf"
	if got != want {
		t.Errorf("BuildFilename = %q, vill ha %q", got, want)
	}
}

func Test_BuildFilename_SanitizesSlashInCharge(t *testing.T) {
	// Reproduktion: Claude extraherade en gång charge="01/0002423/9" som
	// fick os.Create att försöka skriva till en sub-sub-katalog. Charge
	// ska saniteras så att path-separatorer ersätts med underscore.
	ext := &Extraction{
		Charge:      "01/0002423/9",
		Material:    "AW5754",
		ProductForm: "plåt",
		Dimensions:  "5",
	}
	got := BuildFilename(ext, []string{"B127215"})
	want := "01-0002423-9-plat-5-AW5754-B127215.pdf"
	if got != want {
		t.Errorf("BuildFilename = %q, vill ha %q", got, want)
	}
}

func Test_BuildFilename_SanitizesAllUnsafeChars(t *testing.T) {
	// Sanity: alla Windows-osäkra tecken (`< > : " / \ | ? *`) ska bort
	// från filnamnet så vi aldrig kan kollidera med path-separatorer eller
	// med Windows-reserverade tecken.
	ext := &Extraction{
		Charge:      `a/b\c:d*e?f"g<h>i|j`,
		Material:    "S355",
		ProductForm: "rundstång",
		Dimensions:  "10",
	}
	got := BuildFilename(ext, []string{"B1"})
	if strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Errorf("filnamn innehåller osäkra tecken: %q", got)
	}
}
