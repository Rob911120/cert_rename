package eml

import (
	"regexp"
	"strings"
)

// Skiftlägesokänslig: leverantörer skriver ibland "b127575" i löptext, och
// resten av appen behandlar ordernummer versaliserat.
var bNumRegex = regexp.MustCompile(`(?i)\bB\d{6}\b`)

// ExtractBNumbers letar efter B-nummer (B + 6 siffror) i alla angivna källor
// och returnerar unika nummer, versaliserade, i upptäckts-ordning.
func ExtractBNumbers(sources ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sources {
		for _, m := range bNumRegex.FindAllString(s, -1) {
			m = strings.ToUpper(m)
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
