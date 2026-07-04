package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Typade fel — mappas till HTTP-status på ETT ställe (server-paketets
// felhelper). Ingen felprosa i handlers eller verktyg.
var (
	// ErrNotFound: raden finns inte (→ 404).
	ErrNotFound = errors.New("hittas inte")
	// ErrFrozen: certet är sparat och därmed fryst (→ 409).
	ErrFrozen = errors.New("certet är sparat och kan inte ändras")
	// ErrTransition: otillåten statusövergång (→ 409).
	ErrTransition = errors.New("otillåten statusövergång")
)

// ValidationWarnings är Spara-flödets mjuka valideringsfel (→ 422): Rob får
// överstyra med confirm=true, så de är varningar, inte stopp.
type ValidationWarnings []string

func (w ValidationWarnings) Error() string {
	return fmt.Sprintf("valideringsvarningar: %s", strings.Join(w, "; "))
}

// ApplyCorrection sätter ett rättelsefält på ett levande cert och lägger en
// post i den append-only rättelseloggen. Fältnamnen är API-/verktygsytan
// (samma strängar i HTTP och Sickan-verktyg). b_numbers-värdet tolkas som
// kommaseparerad lista.
func ApplyCorrection(c *Cert, field, value, who, ts string) error {
	if !c.Living() {
		return ErrFrozen
	}
	value = strings.TrimSpace(value)
	var old string
	switch field {
	case "charge":
		old, c.CorrectedCharge = c.EffectiveCharge(), value
	case "material":
		old, c.CorrectedMaterial = c.EffectiveMaterial(), value
	case "product_form":
		old, c.CorrectedProductForm = c.EffectiveProductForm(), value
	case "product_code":
		old, c.ProductCode = c.ProductCode, value // direktredigering — ingen corrected-tvilling
	case "dimensions":
		old, c.CorrectedDimensions = c.EffectiveDimensions(), value
	case "cert_type":
		old, c.CorrectedCertType = c.EffectiveCertType(), value
	case "b_numbers":
		old = strings.Join(c.EffectiveBNumbers(), ",")
		c.CorrectedBNumbers = splitList(value)
	default:
		return fmt.Errorf("okänt rättelsefält %q", field)
	}
	c.CorrectionLog = append(c.CorrectionLog, Correction{TS: ts, Who: who, Field: field, Old: old, New: value})
	return nil
}

// splitList delar en kommaseparerad (eller whitespace-separerad) lista.
// Tom sträng → tom (icke-nil) slice = "rättad till inga".
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
