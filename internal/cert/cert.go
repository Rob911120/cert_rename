// Package cert innehåller domäntyper för EN 10204 3.1-stålcertifikat
// samt rena beräkningsfunktioner som validate och filnamnsbyggande.
// Inga IO-deps — paketet är "leaf" i beroendegrafen.
package cert

import (
	"fmt"
	"strings"
)

type Extraction struct {
	IsEN10204_3_1     bool     `json:"is_en10204_3_1"`
	CertType          string   `json:"cert_type"`
	Charge            string   `json:"charge"`
	Material          string   `json:"material"`
	EnStandardPresent bool     `json:"en_standard_present"`
	ProductForm       string   `json:"product_form"`
	Dimensions        string   `json:"dimensions"`
	CountryOfOrigin   string   `json:"country_of_origin"`
	Confidence        string   `json:"confidence"`
	Issues            []string `json:"issues"`
}

// restrictedOrigins är ursprungsländer materialet aldrig får komma från.
var restrictedOrigins = []string{"ryssland", "russia", "belarus", "vitryssland"}

// asciiFold mappar svenska/europeiska accenter till ASCII för filnamn —
// Windows/Outlook/Jeeves-kompatibilitet. Används bara i BuildFilename;
// metadatans råa form bevaras med åäö.
var asciiFold = strings.NewReplacer(
	"å", "a", "Å", "A",
	"ä", "a", "Ä", "A",
	"ö", "o", "Ö", "O",
	"é", "e", "É", "E",
	"è", "e", "È", "E",
)

// slashToDash ersätter snedstreck med bindestreck, enligt regeln att t.ex.
// charge/batch/lot-värden som innehåller "/" ska skrivas med "-" i filnamnet.
var slashToDash = strings.NewReplacer("/", "-")

// pathSafe ersätter Windows-reserverade tecken med underscore. Snedstreck
// hanteras separat via slashToDash innan detta körs.
var pathSafe = strings.NewReplacer(
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	`"`, "_",
	"<", "_",
	">", "_",
	"|", "_",
)

type Classification struct {
	IsCertMail bool   `json:"is_cert_mail"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
}

type Verification struct {
	AnyIsCert bool   `json:"any_is_cert"`
	Reason    string `json:"reason"`
}

// Validate returnerar en lista med valideringsfel, eller tom slice om OK.
func Validate(ext *Extraction, bNums []string) []string {
	var fails []string
	if !ext.IsEN10204_3_1 {
		fails = append(fails, fmt.Sprintf("inte ett 3.1-cert (cert_type=%q)", ext.CertType))
	}
	if strings.TrimSpace(ext.Charge) == "" {
		fails = append(fails, "saknar charge")
	}
	if strings.TrimSpace(ext.Material) == "" {
		fails = append(fails, "saknar material")
	}
	if !ext.EnStandardPresent {
		fails = append(fails, "saknar fullständig EN-norm (t.ex. EN 10025-2)")
	}
	if strings.TrimSpace(ext.Dimensions) == "" {
		fails = append(fails, "saknar dimensioner")
	}
	if len(bNums) == 0 {
		fails = append(fails, "saknar B-nummer")
	}
	if strings.EqualFold(ext.Confidence, "low") {
		fails = append(fails, "låg confidence från Claude")
	}
	origin := strings.ToLower(strings.TrimSpace(ext.CountryOfOrigin))
	for _, restricted := range restrictedOrigins {
		if origin == restricted || strings.Contains(origin, restricted) {
			fails = append(fails, "ursprungsland ej tillåtet (Ryssland/Belarus)")
			break
		}
	}
	return fails
}

// BuildFilename bygger PDF-filnamn enligt mönstret
// <charge>-[form]-<dimensions>-<material>-<bNums>.pdf, vilket motsvarar
// CH-TYP-Storlek-Kvalitet-Beställningsnummer enligt "Att spara certifikat".
// Form-segmentet utelämnas om ProductForm är tomt eller "okänt", och
// ASCII-foldas annars för Windows/Outlook/Jeeves-kompatibilitet.
// Dimensions kan vara "16" (platta) eller "20x2"/"30x30x3" (rör/profil) —
// whitespace tas bort och X normaliseras till lowercase x.
func BuildFilename(ext *Extraction, bNums []string) string {
	dims := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(ext.Dimensions), " ", ""))
	parts := []string{ext.Charge}
	form := strings.TrimSpace(ext.ProductForm)
	if form != "" && !strings.EqualFold(form, "okänt") {
		parts = append(parts, asciiFold.Replace(form))
	}
	parts = append(parts, dims, ext.Material)
	parts = append(parts, bNums...)
	name := slashToDash.Replace(strings.Join(parts, "-"))
	return pathSafe.Replace(name) + ".pdf"
}
