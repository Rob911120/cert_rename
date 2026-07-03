package app

import (
	"context"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// SyncOrderRows ersätter Monitor-snapshotet atomiskt: alla rader markeras
// osedda, sedan upsertas fönstrets rader (in_monitor=1). Rader utanför
// fönstret RADERAS ALDRIG — de behåller in_monitor=0 så länkar överlever
// tills certet sparats. delivered och first_seen bevaras av upserten.
func (a *App) SyncOrderRows(ctx context.Context, rows []*domain.OrderRow) error {
	now := a.ts()
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		if err := q.MarkAllRowsUnseen(ctx); err != nil {
			return err
		}
		for _, r := range rows {
			if err := q.UpsertOrderRow(ctx, r, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.Notify.Logf("📦 orderrader synkade: %d i fönstret", len(rows))
	a.Notify.OverviewChanged()
	return nil
}

// SetLinkVerdict skriver AI-parbedömningen på en länk. No-op om domen är
// oförändrad (undviker SSE-brus varje sync).
func (a *App) SetLinkVerdict(ctx context.Context, linkID int64, v *store.MatchVerdict) error {
	changed := false
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		l, err := q.GetLink(ctx, linkID)
		if err != nil {
			return err
		}
		if l.RequiredMaterial == v.RequiredMaterial && l.RequiredCert == v.RequiredCert &&
			l.OurMaterial == v.OurMaterial && l.MaterialOK == v.MaterialOK &&
			l.RequiredProductForm == v.RequiredProductForm && l.ProductFormOK == v.ProductFormOK &&
			l.AINotes == v.Notes {
			return nil
		}
		changed = true
		l.RequiredMaterial, l.RequiredCert = v.RequiredMaterial, v.RequiredCert
		l.OurMaterial, l.MaterialOK = v.OurMaterial, v.MaterialOK
		l.RequiredProductForm, l.ProductFormOK = v.RequiredProductForm, v.ProductFormOK
		l.AINotes = v.Notes
		l.UpdatedAt = a.ts()
		return q.UpdateLinkVerdict(ctx, l)
	})
	if err != nil {
		return err
	}
	if changed {
		a.Notify.OverviewChanged()
	}
	return nil
}

// SetRowRequirements skriver de AI-tolkade artikelkraven (Task 8) på en orderrad
// och stämplar tolkningstiden via klock-porten. Enda skrivvägen för kravfälten.
// No-op om kraven är oförändrade (undviker SSE-brus varje sync), samma mönster
// som SetLinkVerdict. Kravfälten röres aldrig av sync-upserten.
func (a *App) SetRowRequirements(ctx context.Context, rowID int64, r domain.RowRequirements) error {
	changed := false
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		row, err := q.GetOrderRow(ctx, rowID)
		if err != nil {
			return err
		}
		if row.Req == r {
			return nil // oförändrat — ingen skrivning, ingen omstämpling
		}
		changed = true
		return q.SetOrderRowRequirements(ctx, rowID, r, a.ts())
	})
	if err != nil {
		return err
	}
	if changed {
		a.Notify.OverviewChanged()
	}
	return nil
}

// State/SetState är tunna genomstick till app_state (last_sync m.m.).
func (a *App) State(ctx context.Context, key string) string {
	v, err := a.Repo.GetState(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (a *App) SetState(ctx context.Context, key, value string) {
	if err := a.Repo.SetState(ctx, key, value); err != nil {
		a.Notify.Logf("⚠️  DB (app_state %s): %v", key, err)
	}
}
