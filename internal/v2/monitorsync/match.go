package monitorsync

import (
	"context"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/monitor"
)

// SuggestAll kör förslagspasset för alla levande cert. Grundmatchningen är
// B-nummer → orderrader; när samma B-nummer träffar flera rader (olika
// artiklar på ordern) förfinas valet via charge → ProductRecords → PartId.
// Kan tvetydigheten inte lösas lämnas ALLA kandidater som förslag åt Rob —
// aldrig V1:s tysta "ta första träffen". Befintliga länkar röres aldrig.
func (s *Sync) SuggestAll(ctx context.Context) error {
	certs, err := s.App.Repo.ListCerts(ctx, domain.CertMottagen)
	if err != nil {
		return err
	}
	for _, c := range certs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.suggestForCert(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sync) suggestForCert(ctx context.Context, c *domain.Cert) error {
	for _, bn := range c.EffectiveBNumbers() {
		rows, err := s.App.Repo.RowsByOrderNumber(ctx, bn)
		if err != nil {
			return err
		}
		candidates := rows
		source := "auto_b_number"
		if len(rows) > 1 {
			if refined := s.refineByCharge(ctx, c, rows); len(refined) > 0 {
				candidates = refined
				source = "auto_charge_part"
			}
		}
		for _, r := range candidates {
			if _, err := s.App.SuggestLink(ctx, c.ID, r.DeliveryRowID, r.OrderNumber, source); err != nil {
				return err
			}
		}
	}
	return nil
}

// refineByCharge slår upp certets charge i Monitors ProductRecords och
// behåller de rader vars artikel förekommer där. Tom slice = kunde inte
// förfinas (behåll alla kandidater).
func (s *Sync) refineByCharge(ctx context.Context, c *domain.Cert, rows []*domain.OrderRow) []*domain.OrderRow {
	charge := c.EffectiveCharge()
	if charge == "" {
		return nil
	}
	recs, err := s.ERP.FindProductRecords(ctx, charge)
	if err != nil || len(recs) == 0 {
		return nil
	}
	partIDs := make(map[monitor.ID]bool, len(recs))
	for _, r := range recs {
		partIDs[r.PartId] = true
	}
	var matched []*domain.OrderRow
	for _, row := range rows {
		if partIDs[monitor.ID(row.PartID)] {
			matched = append(matched, row)
		}
	}
	return matched
}
