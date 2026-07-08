package app

import (
	"context"
	"os"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// DeliveryNoteInput är allt intaget vet om en vision-läst följesedel-bild.
// Plana fält (inte ai.DeliveryNoteExtraction) så att app-lagret slipper bero på
// ai-paketet — intaget mappar över.
type DeliveryNoteInput struct {
	OriginalFilename   string
	Data               []byte
	Supplier           string
	DeliveryDate       string
	OrderNumber        string
	Charge             string
	Material           string
	Quantity           float64
	Unit               string
	DeliveryNoteNumber string
	BNumbers           []string
	Confidence         string
}

// DeliveryMatch är resultatet av en lokal matchning: kandidatrader + huruvida
// exakt en träff auto-sattes.
type DeliveryMatch struct {
	Candidates []*domain.OrderRow
	Matched    bool
	Row        *domain.OrderRow // satt när Matched
}

// IngestDeliveryNote tar emot en vision-läst följesedel: skriver bilden EN gång
// till följesedel-lagret under stabilt namn (döps aldrig om) och lägger den
// levande DB-raden. Ingen sidecar (kortlivad; vision är billig att köra om).
// Dedupe på image_hash: samma foto två gånger är en no-op. Fil först, DB sist.
func (a *App) IngestDeliveryNote(ctx context.Context, in DeliveryNoteInput) (*domain.DeliveryNote, bool, error) {
	hash := store.HashImage(in.Data)
	if existing, err := a.Repo.GetDeliveryNoteByHash(ctx, hash); err == nil {
		return existing, true, nil
	} else if err != domain.ErrNotFound {
		return nil, false, err
	}

	cfg := a.Config()
	storedName := store.StoredName(hash, in.OriginalFilename)
	imgPath := store.DeliveryNoteImagePath(cfg, storedName)
	reuse := false
	if b, err := os.ReadFile(imgPath); err == nil && store.HashImage(b) == hash {
		reuse = true // bild från tidigare avbrutet intag — återanvänd
	}
	if !reuse {
		p, err := store.WriteDeliveryNoteImage(cfg, storedName, in.Data)
		if err != nil {
			return nil, false, err
		}
		storedName = lastPathSegment(p)
	}

	d := &domain.DeliveryNote{
		ImageHash:          hash,
		ImageFilename:      storedName,
		Supplier:           in.Supplier,
		DeliveryDate:       in.DeliveryDate,
		OrderNumber:        normalizeOrderNumber(in.OrderNumber),
		Charge:             in.Charge,
		Material:           in.Material,
		Quantity:           in.Quantity,
		Unit:               in.Unit,
		DeliveryNoteNumber: in.DeliveryNoteNumber,
		BNumbers:           in.BNumbers,
		Confidence:         in.Confidence,
		Status:             domain.DNMottagen,
		CreatedAt:          a.ts(),
	}
	if _, err := a.Repo.InsertDeliveryNote(ctx, d); err != nil {
		if existing, gerr := a.Repo.GetDeliveryNoteByHash(ctx, hash); gerr == nil {
			return existing, true, nil
		}
		return nil, false, err
	}
	a.Notify.Logf("📥 följesedel mottagen: %s (order %s, charge %s)", in.OriginalFilename, d.OrderNumber, d.Charge)
	a.Notify.OverviewChanged()
	return d, false, nil
}

// MatchDeliveryNoteLocal matchar en följesedel mot lokalt synkade order_rows —
// HELT lokalt, inga live-Monitor-anrop (order_rows hålls färsk av monitorsync).
// Kandidatuppslag på följesedelns ordernummer + B-nummer. Exakt en rad → sätt
// matchnings-datafälten; 0 eller flera → returnera kandidater för manuellt val.
func (a *App) MatchDeliveryNoteLocal(ctx context.Context, id int64) (*DeliveryMatch, error) {
	d, err := a.Repo.GetDeliveryNote(ctx, id)
	if err != nil {
		return nil, err
	}
	nums := orderNumberCandidates(d)
	seen := map[int64]bool{}
	var candidates []*domain.OrderRow
	for _, num := range nums {
		rows, err := a.Repo.RowsByOrderNumber(ctx, num)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !seen[r.DeliveryRowID] {
				seen[r.DeliveryRowID] = true
				candidates = append(candidates, r)
			}
		}
	}
	res := &DeliveryMatch{Candidates: candidates}
	if len(candidates) == 1 {
		row := candidates[0]
		if err := a.Repo.UpdateDeliveryNoteMatch(ctx, id, row.PurchaseOrderID, row.DeliveryRowID, d.Quantity); err != nil {
			return nil, err
		}
		res.Matched = true
		res.Row = row
		a.Notify.Logf("🔗 följesedel %d matchad → order %s rad %d", id, row.OrderNumber, row.DeliveryRowID)
		a.Notify.OverviewChanged()
	}
	return res, nil
}

// orderNumberCandidates samlar de normaliserade ordernummer en följesedel kan
// matchas på: dess ordernummer först, sedan eventuella B-nummer.
func orderNumberCandidates(d *domain.DeliveryNote) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = normalizeOrderNumber(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(d.OrderNumber)
	for _, bn := range d.BNumbers {
		add(bn)
	}
	return out
}

// SetDeliveryNoteMatch pekar en följesedel mot en manuellt vald orderrad
// (används när auto-matchningen gav 0/flera kandidater). Ordernumret/PO hämtas
// auktoritativt från raden.
func (a *App) SetDeliveryNoteMatch(ctx context.Context, id, deliveryRowID int64) error {
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		d, err := q.GetDeliveryNote(ctx, id)
		if err != nil {
			return err
		}
		row, err := q.GetOrderRow(ctx, deliveryRowID)
		if err != nil {
			return err
		}
		return q.UpdateDeliveryNoteMatch(ctx, id, row.PurchaseOrderID, row.DeliveryRowID, d.Quantity)
	})
	if err != nil {
		return err
	}
	a.Notify.Logf("🔗 följesedel %d manuellt matchad → rad %d", id, deliveryRowID)
	a.Notify.OverviewChanged()
	return nil
}

// MarkDeliveryNoteRegistered flyttar en följesedel till 'inlevererad' (terminal).
// Fas 1: manuell markering efter att Rob registrerat i Monitor. Fas 2: anropas
// efter att Monitor-klienten drivits (se app.RegisterDeliveryNote).
func (a *App) MarkDeliveryNoteRegistered(ctx context.Context, id int64) error {
	return a.transitionDeliveryNote(ctx, id, domain.DNInlevererad)
}

// RejectDeliveryNote avfärdar en följesedel (irrelevant); ångra med Unreject.
func (a *App) RejectDeliveryNote(ctx context.Context, id int64) error {
	return a.transitionDeliveryNote(ctx, id, domain.DNAvfardad)
}

// UnrejectDeliveryNote återupplivar en avfärdad följesedel.
func (a *App) UnrejectDeliveryNote(ctx context.Context, id int64) error {
	return a.transitionDeliveryNote(ctx, id, domain.DNMottagen)
}

func (a *App) transitionDeliveryNote(ctx context.Context, id int64, to domain.DeliveryNoteStatus) error {
	err := a.Repo.Tx(ctx, func(q *store.Q) error {
		d, err := q.GetDeliveryNote(ctx, id)
		if err != nil {
			return err
		}
		if !domain.DeliveryNoteCanTransition(d.Status, to) {
			return domain.ErrTransition
		}
		return q.UpdateDeliveryNoteStatus(ctx, id, to)
	})
	if err != nil {
		return err
	}
	a.Notify.Logf("📦 följesedel %d → %s", id, to)
	a.Notify.OverviewChanged()
	return nil
}

// ListDeliveryNotes är ett tunt genomstick för läsning (UI/overview).
func (a *App) ListDeliveryNotes(ctx context.Context, status domain.DeliveryNoteStatus) ([]*domain.DeliveryNote, error) {
	return a.Repo.ListDeliveryNotes(ctx, status)
}

// GetDeliveryNote är ett tunt genomstick för läsning.
func (a *App) GetDeliveryNote(ctx context.Context, id int64) (*domain.DeliveryNote, error) {
	return a.Repo.GetDeliveryNote(ctx, id)
}
