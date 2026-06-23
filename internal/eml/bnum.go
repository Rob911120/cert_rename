package eml

import "regexp"

var bNumRegex = regexp.MustCompile(`\bB\d{6}\b`)

// IsBNumber returnerar true om s ser ut som ett B-nummer (B + 6 siffror).
func IsBNumber(s string) bool {
	return bNumRegex.MatchString(s)
}

// ExtractBNumbers letar efter B-nummer (B + 6 siffror) i alla angivna källor
// och returnerar unika nummer i upptäckts-ordning.
func ExtractBNumbers(sources ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sources {
		for _, m := range bNumRegex.FindAllString(s, -1) {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
