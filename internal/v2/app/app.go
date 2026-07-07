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
// stället för att dubblera. deliveryRowID=0 = fri koppling på enbart B-nummer
// (raden finns inte i Monitor-fönstret än); anges rad hämtas ordernumret
// auktoritativt från raden.
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
			orderNumber = row.OrderNumber
		}
		if orderNumber == "" {
			return fmt.Errorf("varken orderrad eller B-nummer angivet")
		}
		existing, err := q.GetLinkByKey(ctx, certID, deliveryRowID, orderNumber)
		switch {
		case err == nil:
			if existing.Status == domain.LinkBekraftad {
				link = existing // no-op: redan bekräftad
				return nil
			}
			if !domain.LinkCanTransition(existing.Status, domain.LinkBekraftad) {
				return domain.ErrTransition
			}
			if err := q.UpdateLinkStatus(ctx, existing.ID, domain.LinkBekraftad, source, a.ts()); err != nil {
				return err
			}
			existing.Status = domain.LinkBekraftad
			existing.MatchSource = source
			link = existing
			return nil
		case err == domain.ErrNotFound:
			now := a.ts()
			link = &domain.Link{
				CertID: certID, DeliveryRowID: deliveryRowID, OrderNumber: orderNumber,
				Status: domain.LinkBekraftad, MatchSource: source,
				CreatedAt: now, UpdatedAt: now,
			}
			_, err := q.InsertLink(ctx, link)
			return err
		default:
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	a.Notify.Logf("🔗 cert %d ↔ %s bekräftad (%s)", certID, orderNumber, source)
	a.Notify.OverviewChanged()
	return link, nil
}

// SuggestLink lägger ett FÖRSLAG (foreslagen) från intag/sync. Rör aldrig en
// befintlig länk — Rob:s beslut (bekraftad/avfardad) skrivs inte över av
// automatiken. Returnerar nil, nil om länken redan fanns.
func (a *App) SuggestLink(ctx context.Context, certID, deliveryRowID int64, orderNumber, source string) (*domain.Link, error) {
	orderNumber = normalizeOrderNumber(orderNumber)
	var link *domain.Link
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
	if link != nil {
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
