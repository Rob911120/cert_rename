package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Regelrätta krav-vs-cert-jämförelser (Task 9). Rena funktioner utan IO: det
// otolkningsbara (engelska, certtyp, slagseghet) döms här i stället för av
// AI:n. De BERÄKNAS FÄRSKT vid rendering och persisteras ALDRIG — ingen
// staleness, ingen cache-invalidering. Verdict-vokabulären är identisk med
// AI-domen/MatchVerdict ("ok"/"mismatch"/"unknown").
// ---------------------------------------------------------------------------

const (
	VerdictOK       = "ok"
	VerdictMismatch = "mismatch"
	VerdictUnknown  = "unknown"
)

// EnglishVerdict jämför artikelns språkkrav mot certets is_english-flagga.
// Inget uttryckligt krav (reqEnglish=false) → "unknown": vi vet inte att
// engelska krävs, alltså ingen dom (samma "gissa aldrig"-princip som kraven).
func EnglishVerdict(reqEnglish, certIsEnglish bool) string {
	if !reqEnglish {
		return VerdictUnknown
	}
	if certIsEnglish {
		return VerdictOK
	}
	return VerdictMismatch
}

// CertTypeVerdict jämför beställd certnivå mot certets typ, normaliserat
// (trim + skiftlägesokänsligt, så "3.1" == "3.1 "). Någon sida tom → "unknown".
// Extraktionens sentinel "unknown" (prompten: "3.1"/"2.2"/"3.2"/"unknown")
// betyder "kunde inte avgöras" och är ingen konkret certtyp — den ska ge
// "unknown"-dom, inte en falsk mismatch.
func CertTypeVerdict(reqCertType, certCertType string) string {
	req := strings.TrimSpace(reqCertType)
	cert := strings.TrimSpace(certCertType)
	if req == "" || cert == "" || strings.EqualFold(req, "unknown") || strings.EqualFold(cert, "unknown") {
		return VerdictUnknown
	}
	if strings.EqualFold(req, cert) {
		return VerdictOK
	}
	return VerdictMismatch
}

// impactRe matchar "27J/-20°C"-formatet: tal+J, valfritt mellanslag, /, tal
// (med eller utan tecken)+°C. Skiftlägesokänsligt; ° är valfritt så "27J/-20C"
// också går, och komma eller punkt godtas som decimaltecken.
var impactRe = regexp.MustCompile(`(?i)^\s*([0-9]+(?:[.,][0-9]+)?)\s*J\s*/\s*([+-]?[0-9]+(?:[.,][0-9]+)?)\s*°?\s*C\s*$`)

// ParseImpact tolkar en slagseghets-kravsträng till (energi J, provtemp °C).
// ok=false när strängen inte matchar formatet.
func ParseImpact(s string) (energyJ, tempC float64, ok bool) {
	m := impactRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	e, err1 := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
	t, err2 := strconv.ParseFloat(strings.Replace(m[2], ",", ".", 1), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return e, t, true
}

// ImpactVerdict jämför beställt slagseghetskrav mot certets slagvärden. "ok"
// kräver att certet dokumenterar BÅDE energi (>0) och provtemp (!=nil) och att
// certet är minst lika segt (energi >=) OCH testat minst lika kallt (temp <=).
// Saknas kravet (oparsebart/tomt) eller certdatan → "unknown".
func ImpactVerdict(reqImpact string, certEnergyJ float64, certTempC *float64) string {
	reqE, reqT, ok := ParseImpact(reqImpact)
	if !ok || certEnergyJ <= 0 || certTempC == nil {
		return VerdictUnknown
	}
	if certEnergyJ >= reqE && *certTempC <= reqT {
		return VerdictOK
	}
	return VerdictMismatch
}
