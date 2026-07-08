package monitorsync

import (
	"context"
	"strings"

	"cert-renamer/internal/v2/domain"
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
		// Normalisera som app-lagret (rättade B-nummer kan vara små bokstäver).
		rows, err := s.App.Repo.RowsByOrderNumber(ctx, strings.ToUpper(strings.TrimSpace(bn)))
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
// behåller de rader vars (order, artikel) förekommer där — matchningen sker
// via (PurchaseOrderId, PartId) precis som ProductRecord-dokumentationen
// säger; enbart PartId skulle godta en artikel-träff från en ANNAN order.
// Records utan PurchaseOrderId matchar på artikel enbart. Tom slice = kunde
// inte förfinas (behåll alla kandidater).
func (s *Sync) refineByCharge(ctx context.Context, c *domain.Cert, rows []*domain.OrderRow) []*domain.OrderRow {
	charge := c.EffectiveCharge()
	if charge == "" {
		return nil
	}
	recs, err := s.ERP.FindProductRecords(ctx, charge)
	if err != nil || len(recs) == 0 {
		return nil
	}
	type key struct{ part, po int64 }
	keys := make(map[key]bool, len(recs))
	for _, r := range recs {
		keys[key{int64(r.PartId), int64(r.PurchaseOrderId)}] = true
	}
	var matched []*domain.OrderRow
	for _, row := range rows {
		if keys[key{row.PartID, row.PurchaseOrderID}] || keys[key{row.PartID, 0}] {
			matched = append(matched, row)
		}
	}
	return matched
}
