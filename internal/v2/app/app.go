// Package app är V2:s ENDA skrivväg. HTTP-handlers, Sickan-verktyg och
// bakgrundsjobb anropar App — aldrig repository direkt. Varje mutator följer
// samma form: guard (domain-tillståndsmaskinen) → transaktion → mutation →
// auditlogg → notifiering. Det gör V1:s buggklass "två divergerande
// skrivvägar" omöjlig.
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// Notifier är app-lagrets enda sidokanal utåt: logg + "översikten har ändrats"
// (SSE-ping i drift, räknare i test).
type Notifier interface {
	Logf(format string, args ...any)
	OverviewChanged()
}

// NopNotifier används i tester och verktyg som inte bryr sig.
type NopNotifier struct{}

func (NopNotifier) Logf(string, ...any) {}
func (NopNotifier) OverviewChanged()    {}

// App bär beroendena. Config och Now är injicerade funktioner så att tester
// kan frysa både konfiguration och klocka.
type App struct {
	Repo   *store.Repository
	Config func() store.Config
	Now    func() time.Time
	Notify Notifier
}

func New(repo *store.Repository, cfg func() store.Config, now func() time.Time, notify Notifier) *App {
	if now == nil {
		now = time.Now
	}
	if notify == nil {
		notify = NopNotifier{}
	}
	return &App{Repo: repo, Config: cfg, Now: now, Notify: notify}
}

func (a *App) ts() string { return a.Now().UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Läsning
// ---------------------------------------------------------------------------

// CertView är cert + härledd kontext som UI och verktyg alltid vill ha ihop.
type CertView struct {
	Cert             *domain.Cert
	Links            []*domain.Link
	ConfirmedOrders  []string
	ProposedFilename string
}

func (a *App) GetCertView(ctx context.Context, certID int64) (*CertView, error) {
	c, err := a.Repo.GetCert(ctx, certID)
	if err != nil {
		return nil, err
	}
	return a.buildView(ctx, &a.Repo.Q, c)
}

func (a *App) buildView(ctx context.Context, q *store.Q, c *domain.Cert) (*CertView, error) {
	links, err := q.ListLinksForCert(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	confirmed, err := q.ConfirmedOrderNumbers(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return &CertView{
		Cert:             c,
		Links:            links,
		ConfirmedOrders:  confirmed,
		ProposedFilename: domain.ProposedFilename(c, confirmed),
	}, nil
}

// ---------------------------------------------------------------------------
// Mutatorer — cert-fält
// ---------------------------------------------------------------------------

// UpdateCertField sätter en rättelse (corrected_*) på ett levande cert och
// loggar den. Returnerar den uppdaterade vyn (med nytt levande namn).
func (a *App) UpdateCertField(ctx context.Context, certID int64, field, value, who string) (*CertView, error) {
	var view *CertView
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		c, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if err := domain.ApplyCorrection(c, field, value, who, a.ts()); err != nil {
			return err
		}
		if err := q.UpdateCertWork(ctx, c); err != nil {
			return err
		}
		view, err = a.buildView(ctx, q, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.Notify.Logf("✏️  cert %d: %s → %q (%s)", certID, field, value, who)
	// Rättade B-nummer ska matcha om DIREKT — annars syns nya förslag först vid
	// nästa Monitor-sync. Körs utanför Tx:en ovan (SuggestLink öppnar egna
	// transaktioner) och är idempotent (no-op på redan existerande länkar/beslut).
	if field == "b_numbers" {
		if _, serr := a.SuggestLinksByBNumber(ctx, certID); serr != nil {
			a.Notify.Logf("   ⚠️  förslagspass efter B-nummer-rättning: %v", serr)
		}
	}
	a.Notify.OverviewChanged()
	return view, nil
}

// SetNameOverride sätter (eller rensar, med tom sträng) namn-overriden.
func (a *App) SetNameOverride(ctx context.Context, certID int64, name string) (*CertView, error) {
	var view *CertView
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		c, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if !c.Living() {
			return domain.ErrFrozen
		}
		c.NameOverride = strings.TrimSpace(name)
		if err := q.UpdateCertWork(ctx, c); err != nil {
			return err
		}
		view, err = a.buildView(ctx, q, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.Notify.OverviewChanged()
	return view, nil
}

// ---------------------------------------------------------------------------
// Mutatorer — länkar (B-nummer-kopplingar)
// ---------------------------------------------------------------------------

// normalizeOrderNumber städar användar-/Sickan-inmatade B-nummer.
func normalizeOrderNumber(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// ConfirmLink kopplar ett cert till en orderrad eller ett fritt B-nummer som
// BEKRÄFTAD länk. Finns redan en länk på nyckeln återupplivas/bekräftas den i
// stället för att dubblera. deliveryRowID=0 = fri koppling på enbart B-nummer;
// finns B-numrets rader redan i DB bekräftas mot dem i stället (en länk per
// rad) — en fri länk syns inte under någon rad i översikten och certet skulle
// annars försvinna ur UI:t. Anges rad hämtas ordernumret auktoritativt (och
// normaliserat) från raden.
func (a *App) ConfirmLink(ctx context.Context, certID, deliveryRowID int64, orderNumber, source string) (*domain.Link, error) {
	orderNumber = normalizeOrderNumber(orderNumber)
	var link *domain.Link
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		c, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if !c.Living() {
			return domain.ErrFrozen
		}
		if deliveryRowID != 0 {
			row, err := q.GetOrderRow(ctx, deliveryRowID)
			if err != nil {
				return fmt.Errorf("orderrad %d: %w", deliveryRowID, err)
			}
			orderNumber = normalizeOrderNumber(row.OrderNumber)
		}
		if orderNumber == "" {
			return fmt.Errorf("varken orderrad eller B-nummer angivet")
		}
		targets := []int64{deliveryRowID}
		if deliveryRowID == 0 {
			rows, err := q.RowsByOrderNumber(ctx, orderNumber)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				targets = targets[:0]
				for _, r := range rows {
					targets = append(targets, r.DeliveryRowID)
				}
			}
		}
		for _, rowID := range targets {
			if rowID != 0 {
				// En äldre FRI länk på samma B-nummer pekas om till raden i
				// stället för att lämnas osynlig bredvid den nya.
				if _, err := a.upgradeFreeLink(ctx, q, certID, rowID, orderNumber); err != nil {
					return err
				}
			}
			l, err := a.confirmOne(ctx, q, certID, rowID, orderNumber, source)
			if err != nil {
				return err
			}
			if link == nil {
				link = l
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.Notify.Logf("🔗 cert %d ↔ %s bekräftad (%s)", certID, orderNumber, source)
	a.Notify.OverviewChanged()
	return link, nil
}

// confirmOne bekräftar (eller återupplivar) EN länknyckel inom pågående
// transaktion — get-or-create-mönstret som ConfirmLink alltid haft.
func (a *App) confirmOne(ctx context.Context, q *store.Q, certID, deliveryRowID int64, orderNumber, source string) (*domain.Link, error) {
	existing, err := q.GetLinkByKey(ctx, certID, deliveryRowID, orderNumber)
	switch {
	case err == nil:
		if existing.Status == domain.LinkBekraftad {
			return existing, nil // no-op: redan bekräftad
		}
		if !domain.LinkCanTransition(existing.Status, domain.LinkBekraftad) {
			return nil, domain.ErrTransition
		}
		if err := q.UpdateLinkStatus(ctx, existing.ID, domain.LinkBekraftad, source, a.ts()); err != nil {
			return nil, err
		}
		existing.Status = domain.LinkBekraftad
		existing.MatchSource = source
		return existing, nil
	case err == domain.ErrNotFound:
		now := a.ts()
		l := &domain.Link{
			CertID: certID, DeliveryRowID: deliveryRowID, OrderNumber: orderNumber,
			Status: domain.LinkBekraftad, MatchSource: source,
			CreatedAt: now, UpdatedAt: now,
		}
		_, err := q.InsertLink(ctx, l)
		return l, err
	default:
		return nil, err
	}
}

// upgradeFreeLink pekar om en FRI länk (delivery_row_id=0) till en riktig rad
// när raden nu finns i DB — ordernumret bar kopplingen tills dess (se
// domain.Link). Robs beslut (bekräftad/avfärdad) följer med länken oförändrat.
// No-op om ingen fri länk finns eller om målnyckeln redan är upptagen.
func (a *App) upgradeFreeLink(ctx context.Context, q *store.Q, certID, rowID int64, orderNumber string) (bool, error) {
	free, err := q.GetLinkByKey(ctx, certID, 0, orderNumber)
	if err == domain.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := q.GetLinkByKey(ctx, certID, rowID, orderNumber); err == nil {
		return false, nil // radlänk finns redan — låt den fria ligga
	} else if err != domain.ErrNotFound {
		return false, err
	}
	if err := q.UpdateLinkDeliveryRow(ctx, free.ID, rowID, a.ts()); err != nil {
		return false, err
	}
	return true, nil
}

// SuggestLink lägger ett FÖRSLAG (foreslagen) från intag/sync. Rör aldrig en
// befintlig länk — Rob:s beslut (bekraftad/avfardad) skrivs inte över av
// automatiken. Returnerar nil, nil om länken redan fanns.
func (a *App) SuggestLink(ctx context.Context, certID, deliveryRowID int64, orderNumber, source string) (*domain.Link, error) {
	orderNumber = normalizeOrderNumber(orderNumber)
	var link *domain.Link
	var upgradedFree bool
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		c, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if !c.Living() {
			return nil // sparade/arkiverade cert får inga nya förslag; tyst no-op
		}
		if _, err := q.GetLinkByKey(ctx, certID, deliveryRowID, orderNumber); err == nil {
			return nil // finns redan i någon status — rör inte
		} else if err != domain.ErrNotFound {
			return err
		}
		// Raden har dykt upp för ett B-nummer som redan bär en FRI länk →
		// peka om den (Robs beslut följer med) i stället för ett nytt förslag.
		if deliveryRowID != 0 {
			if upgraded, err := a.upgradeFreeLink(ctx, q, certID, deliveryRowID, orderNumber); err != nil {
				return err
			} else if upgraded {
				upgradedFree = true
				return nil
			}
		}
		now := a.ts()
		link = &domain.Link{
			CertID: certID, DeliveryRowID: deliveryRowID, OrderNumber: orderNumber,
			Status: domain.LinkForeslagen, MatchSource: source,
			CreatedAt: now, UpdatedAt: now,
		}
		_, err = q.InsertLink(ctx, link)
		return err
	})
	if err != nil {
		return nil, err
	}
	if upgradedFree {
		a.Notify.Logf("🔗 cert %d: fri koppling %s pekades om till orderraden", certID, orderNumber)
	}
	if link != nil || upgradedFree {
		a.Notify.OverviewChanged()
	}
	return link, nil
}

// RejectLink avfärdar ett förslag eller kopplar loss en bekräftad länk.
func (a *App) RejectLink(ctx context.Context, linkID int64) error {
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		l, err := q.GetLink(ctx, linkID)
		if err != nil {
			return err
		}
		c, err := q.GetCert(ctx, l.CertID)
		if err != nil {
			return err
		}
		if !c.Living() {
			return domain.ErrFrozen
		}
		if !domain.LinkCanTransition(l.Status, domain.LinkAvfardad) {
			return domain.ErrTransition
		}
		return q.UpdateLinkStatus(ctx, linkID, domain.LinkAvfardad, l.MatchSource, a.ts())
	})
	if err != nil {
		return err
	}
	a.Notify.OverviewChanged()
	return nil
}

// ---------------------------------------------------------------------------
// Mutatorer — livscykel (utom Spara, som bor i save.go)
// ---------------------------------------------------------------------------

// ArchiveCert markerar ett cert som ej-cert/irrelevant.
func (a *App) ArchiveCert(ctx context.Context, certID int64) error {
	return a.transitionCert(ctx, certID, domain.CertArkiverad)
}

// UnarchiveCert ångrar en arkivering.
func (a *App) UnarchiveCert(ctx context.Context, certID int64) error {
	return a.transitionCert(ctx, certID, domain.CertMottagen)
}

func (a *App) transitionCert(ctx context.Context, certID int64, to domain.CertStatus) error {
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		c, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if !domain.CertCanTransition(c.Status, to) {
			if c.Status == domain.CertSparad {
				return domain.ErrFrozen
			}
			return domain.ErrTransition
		}
		return q.UpdateCertStatus(ctx, certID, to)
	})
	if err != nil {
		return err
	}
	a.Notify.Logf("📦 cert %d → %s", certID, to)
	a.Notify.OverviewChanged()
	return nil
}

// ---------------------------------------------------------------------------
// Mutatorer — orderrader, noteringar
// ---------------------------------------------------------------------------

// MarkDelivered sätter lokal leveransbokföring.
func (a *App) MarkDelivered(ctx context.Context, deliveryRowIDs []int64, delivered bool) error {
	if err := a.Repo.SetDelivered(ctx, deliveryRowIDs, delivered); err != nil {
		return err
	}
	a.Notify.OverviewChanged()
	return nil
}

// AddNote lägger en notering på en orderrad eller ett cert.
func (a *App) AddNote(ctx context.Context, kind string, refID int64, orderNumber, partNumber, author, text string) (*store.Note, error) {
	if kind != "order_row" && kind != "cert" {
		return nil, fmt.Errorf("okänd noteringstyp %q", kind)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("tom notering")
	}
	n := &store.Note{
		Kind: kind, RefID: refID, OrderNumber: orderNumber, PartNumber: partNumber,
		Author: author, Text: text, CreatedAt: a.ts(),
	}
	if _, err := a.Repo.AddNote(ctx, n); err != nil {
		return nil, err
	}
	a.Notify.OverviewChanged()
	return n, nil
}

func (a *App) DeleteNote(ctx context.Context, id int64) error {
	if err := a.Repo.DeleteNote(ctx, id); err != nil {
		return err
	}
	a.Notify.OverviewChanged()
	return nil
}
