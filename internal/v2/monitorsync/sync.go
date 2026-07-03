// Package monitorsync är den dagliga Monitor-refreshen: inköpsorderrader +
// artikeldata blir förstklassiga order_rows, förslagspasset kopplar levande
// cert mot raderna, och AI-parbedömningen dömer material per länk (cachad).
// Monitor och AI sitter bakom portar (ERP/Judge) så allt testas offline.
package monitorsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/monitor"
	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// ERP är porten mot Monitor (read-only — skriv-API:t är inte licensierat och
// V2 gör ingen inleveransregistrering).
type ERP interface {
	GetUpcomingOrderRows(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error)
	GetPurchaseOrder(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error)
	GetSupplier(ctx context.Context, id monitor.ID) (*monitor.Supplier, error)
	GetPartsByIds(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error)
	FindProductRecords(ctx context.Context, charge string) ([]monitor.ProductRecord, error)
}

// Judge är porten mot AI-parbedömningen (nil = ingen API-nyckel, hoppa över).
type Judge interface {
	ClassifyUpcoming(ctx context.Context, in ai.UpcomingClassifyInput) (*ai.UpcomingClassification, error)
}

// Sync är hela refresh-jobbet. All mutation går genom App.
type Sync struct {
	App    *app.App
	ERP    ERP
	Judge  Judge
	Config func() v1store.Config
}

// Refresh kör hela kedjan: hämta fönstret → synka order_rows → förslagspass →
// AI-parbedömning. Returnerar antal rader i fönstret.
func (s *Sync) Refresh(ctx context.Context) (int, error) {
	cfg := s.Config()
	n := s.App.Notify
	now := time.Now()
	from := now.AddDate(0, 0, -cfg.UpcomingBackDays) // bakåt: försenade/överförda
	to := now.AddDate(0, 0, cfg.UpcomingWindowDays)

	windowRows, stats, err := s.ERP.GetUpcomingOrderRows(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("hämta kommande inleveranser: %w", err)
	}
	// Släpp operationsrader utan artikel (PartId 0) — kan inte bära cert.
	rows := make([]monitor.PurchaseOrderRow, 0, len(windowRows))
	for _, r := range windowRows {
		if r.PartId != 0 {
			rows = append(rows, r)
		}
	}
	n.Logf("📦 Monitor: %d öppna rader (datum %s–%s) → %d i fönstret → %d med artikel",
		stats.Fetched, dashIfEmpty(stats.MinDate), dashIfEmpty(stats.MaxDate), len(windowRows), len(rows))

	orders := s.resolveOrders(ctx, rows)
	parts := s.fetchMissingParts(ctx, rows)

	out := make([]*domain.OrderRow, 0, len(rows))
	for _, row := range rows {
		if ctx.Err() != nil {
			return len(out), ctx.Err()
		}
		out = append(out, buildOrderRow(row, orders, parts))
	}
	if err := s.App.SyncOrderRows(ctx, out); err != nil {
		return len(out), fmt.Errorf("synka order_rows: %w", err)
	}

	if err := s.SuggestAll(ctx); err != nil {
		n.Logf("⚠️  förslagspass: %v", err)
	}
	if err := s.JudgeAll(ctx); err != nil {
		n.Logf("⚠️  parbedömning: %v", err)
	}

	s.App.SetState(ctx, "last_sync", time.Now().UTC().Format(time.RFC3339))
	return len(out), nil
}

type orderInfo struct {
	OrderNumber  string
	SupplierName string
}

// resolveOrders hämtar ordernummer + leverantör per unik order (en gång).
func (s *Sync) resolveOrders(ctx context.Context, rows []monitor.PurchaseOrderRow) map[monitor.ID]orderInfo {
	infos := map[monitor.ID]orderInfo{}
	for _, row := range rows {
		id := row.ParentOrderId
		if id == 0 {
			continue
		}
		if _, ok := infos[id]; ok {
			continue
		}
		if ctx.Err() != nil {
			return infos
		}
		info := orderInfo{}
		po, err := s.ERP.GetPurchaseOrder(ctx, id)
		if err != nil {
			s.App.Notify.Logf("⚠️  kunde inte hämta order %d: %v", id, err)
		} else if po != nil {
			info.OrderNumber = po.OrderNumber
			if po.BusinessContactId != 0 {
				if sup, serr := s.ERP.GetSupplier(ctx, po.BusinessContactId); serr == nil && sup != nil {
					info.SupplierName = supplierDisplay(sup)
				}
			}
		}
		infos[id] = info
	}
	return infos
}

// fetchMissingParts batch-hämtar artiklar för rader där inline-$expand inte
// gav någon Part.
func (s *Sync) fetchMissingParts(ctx context.Context, rows []monitor.PurchaseOrderRow) map[monitor.ID]monitor.Part {
	var missing []monitor.ID
	for _, row := range rows {
		if row.Part == nil && row.PartId != 0 {
			missing = append(missing, row.PartId)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	parts, err := s.ERP.GetPartsByIds(ctx, missing)
	if err != nil {
		s.App.Notify.Logf("⚠️  kunde inte hämta artiklar för %d rader: %v", len(missing), err)
		return nil
	}
	return parts
}

func buildOrderRow(row monitor.PurchaseOrderRow, orders map[monitor.ID]orderInfo, parts map[monitor.ID]monitor.Part) *domain.OrderRow {
	info := orders[row.ParentOrderId]
	r := &domain.OrderRow{
		DeliveryRowID:   int64(row.ID),
		PurchaseOrderID: int64(row.ParentOrderId),
		OrderNumber:     info.OrderNumber,
		SupplierName:    info.SupplierName,
		PartID:          int64(row.PartId),
		PlannedQty:      row.RestQuantity, // kvarvarande ej levererat
		DeliveryDate:    normalizeDate(row.DeliveryDate),
		DeliveryRaw:     string(row.Raw),
	}
	part := row.Part
	if part == nil && row.PartId != 0 {
		if p, ok := parts[row.PartId]; ok {
			part = &p
		}
	}
	if part != nil {
		r.PartNumber = part.PartNumber
		r.Description = part.Description
		r.ExtraDescription = part.ExtraDescription // RÅ extra benämning, persisteras
		r.PartRaw = string(part.Raw)
		r.CertRequired = part.RequiresCert()
	}
	return r
}

func supplierDisplay(s *monitor.Supplier) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	if strings.TrimSpace(s.AlternativeName) != "" {
		return s.AlternativeName
	}
	return s.SupplierCode
}

// normalizeDate trimmar Monitors DeliveryDate till "YYYY-MM-DD".
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------------------------------------------------------------------------
// AI-parbedömning per länk, cachad med BREDDAD nyckel (fixar V1-buggen att
// dimensions-/form-rättelser inte invaliderade cachen).
// ---------------------------------------------------------------------------

// JudgeAll dömer material för varje aktiv länk (foreslagen/bekraftad) vars
// cert lever och vars orderrad kräver cert.
func (s *Sync) JudgeAll(ctx context.Context) error {
	if s.Judge == nil {
		return nil // ingen API-nyckel — domen fylls i när nyckeln finns
	}
	links, err := s.App.Repo.ListLinks(ctx)
	if err != nil {
		return err
	}
	for _, l := range links {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if l.Status != domain.LinkForeslagen && l.Status != domain.LinkBekraftad {
			continue
		}
		if l.DeliveryRowID == 0 {
			continue // fri B-nummerkoppling utan rad — ingen artikel att döma mot
		}
		c, err := s.App.Repo.GetCert(ctx, l.CertID)
		if err != nil || !c.Living() {
			continue
		}
		row, err := s.App.Repo.GetOrderRow(ctx, l.DeliveryRowID)
		if err != nil || !row.CertRequired {
			continue
		}
		v, err := s.judgeWithCache(ctx, row, c)
		if err != nil {
			s.App.Notify.Logf("⚠️  materialdom %s: %v", row.PartNumber, err)
			continue
		}
		if err := s.App.SetLinkVerdict(ctx, l.ID, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sync) judgeWithCache(ctx context.Context, row *domain.OrderRow, c *domain.Cert) (*store.MatchVerdict, error) {
	key := matchCacheKey(row, c)
	if cached, err := s.App.Repo.GetMatchCache(ctx, key); err == nil {
		return cached, nil
	}
	dom, err := s.Judge.ClassifyUpcoming(ctx, ai.UpcomingClassifyInput{
		PartNumber:       row.PartNumber,
		Description:      row.Description,
		ExtraDescription: row.ExtraDescription,
		CertRequired:     row.CertRequired,
		CertMaterial:     c.EffectiveMaterial(),
		CertType:         c.EffectiveCertType(),
		CertDimensions:   c.EffectiveDimensions(),
		CertProductForm:  c.EffectiveProductForm(),
	})
	if err != nil {
		return nil, err
	}
	v := &store.MatchVerdict{
		RequiredMaterial:    dom.RequiredMaterial,
		RequiredCert:        dom.RequiredCert,
		OurMaterial:         dom.OurMaterial,
		MaterialOK:          dom.MaterialOK,
		RequiredProductForm: dom.RequiredProductForm,
		ProductFormOK:       dom.ProductFormOK,
		Notes:               dom.Notes,
	}
	if err := s.App.Repo.PutMatchCache(ctx, key, v, time.Now().UTC().Format(time.RFC3339)); err != nil {
		s.App.Notify.Logf("⚠️  cache-skrivning: %v", err)
	}
	return v, nil
}

// matchCacheKey är BREDDAD mot V1: alla effektiva certfält som påverkar domen
// ingår, så varje rättelse (material, dimensioner, form, cert-typ) ger en ny
// nyckel och en färsk dom.
func matchCacheKey(row *domain.OrderRow, c *domain.Cert) string {
	raw := fmt.Sprintf("part:%d|%s|cert:%d|%s|%s|%s|%s|%t",
		row.PartID, row.ExtraDescription,
		c.ID, c.EffectiveMaterial(), c.EffectiveDimensions(), c.EffectiveProductForm(),
		c.EffectiveCertType(), row.CertRequired)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
