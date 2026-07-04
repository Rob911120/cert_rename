package domain

import (
	"sort"
	"strings"

	"cert-renamer/internal/cert"
)

// sanitizeOverride tillämpar samma filnamnsregler på ett manuellt namn som
// cert.BuildFilename gör på byggda: snedstreck → bindestreck, Windows-
// reserverade tecken → underscore. Medvetet minimal spegel av cert-paketets
// oexporterade replacers (att exportera dem vore en V1-ändring utanför plan).
var overrideSanitizer = strings.NewReplacer(
	"/", "-",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	`"`, "_",
	"<", "_",
	">", "_",
	"|", "_",
)

// ProposedFilename är det LEVANDE föreslagna filnamnet. Det persisteras aldrig
// medan certet lever — varje anrop räknar om från aktuell effektiv data, så
// varje fält- eller länkändring flyttar namnet direkt. Precedens:
//
//  1. NameOverride om satt (saniterad, .pdf-suffix garanterat)
//  2. cert.BuildFilename över effektiva fält, där B-numren tas från
//     CorrectedBNumbers om rättade, annars bekräftade länkars ordernummer
//     (sorterade, dedupade), annars råa extraherade B-nummer.
func ProposedFilename(c *Cert, confirmedOrderNumbers []string) string {
	if o := strings.TrimSpace(c.NameOverride); o != "" {
		name := overrideSanitizer.Replace(o)
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
		return name
	}
	return cert.BuildFilename(c.EffectiveExtraction(), NameBNumbers(c, confirmedOrderNumbers))
}

// NameBNumbers är B-nummer-mängden som ingår i det byggda filnamnet, med
// precedensen ovan. Exponerad separat så Spara-flödet kan validera samma
// mängd som namnet byggs av.
func NameBNumbers(c *Cert, confirmedOrderNumbers []string) []string {
	if c.CorrectedBNumbers != nil {
		return c.CorrectedBNumbers
	}
	if len(confirmedOrderNumbers) > 0 {
		return dedupeSorted(confirmedOrderNumbers)
	}
	return c.BNumbers
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
