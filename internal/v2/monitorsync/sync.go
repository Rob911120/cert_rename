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
	"strconv"
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
// V2 gör ingen inleveransregistrering). Både bas- och *Full-varianterna ligger
// på porten: V2 anropar de cert-rika Full-varianterna men faller tillbaka till
// bas-queryerna om Monitor avvisar den overifierade expanden (se fallback-wrappers
// nedan). Bas-varianterna delas byte-identiskt med V1.
type ERP interface {
	GetUpcomingOrderRows(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error)
	GetUpcomingOrderRowsFull(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error)
	GetPurchaseOrder(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error)
	GetPurchaseOrderFull(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error)
	GetSupplier(ctx context.Context, id monitor.ID) (*monitor.Supplier, error)
	GetPartsByIds(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error)
	GetPartsByIdsFull(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error)
	FindProductRecords(ctx context.Context, charge string) ([]monitor.ProductRecord, error)
}

// Judge är porten mot AI (nil = ingen API-nyckel, hoppa över). Bär både
// parbedömningen (cert↔rad) och kravtolkningen (rad → strukturerade krav).
type Judge interface {
	ClassifyUpcoming(ctx context.Context, in ai.UpcomingClassifyInput) (*ai.UpcomingClassification, error)
	ParseRequirements(ctx context.Context, in ai.RequirementsInput) (*ai.ArticleRequirements, error)
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

	windowRows, stats, err := s.upcomingOrderRows(ctx, from, to)
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
	if err := s.ParseAllRequirements(ctx); err != nil {
		n.Logf("⚠️  kravtolkning: %v", err)
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

	// Cert-bärande orderfält (Task 6) — se buildOrderRow.
	GoodsLabel                 string
	ExternalComment            string
	BusinessContactOrderNumber string
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
		po, err := s.purchaseOrder(ctx, id)
		if err != nil {
			s.App.Notify.Logf("⚠️  kunde inte hämta order %d: %v", id, err)
		} else if po != nil {
			info.OrderNumber = po.OrderNumber
			info.GoodsLabel = po.GoodsLabel
			info.BusinessContactOrderNumber = po.BusinessContactOrderNumber
			info.ExternalComment = commentText(po.ExternalComment)
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
	parts, err := s.partsByIds(ctx, missing)
	if err != nil {
		s.App.Notify.Logf("⚠️  kunde inte hämta artiklar för %d rader: %v", len(missing), err)
		return nil
	}
	return parts
}

// ---------------------------------------------------------------------------
// Fallback-wrappers: V2 anropar de cert-rika Full-varianterna men får ALDRIG
// hard-faila pga en overifierad expand. Om Full-anropet felar loggas en varning
// och bas-queryn körs i stället — de nya cert-fälten blir då tomma men order-
// och radidentiteten (ordernummer, leverantör, artikel) bevaras.
// ---------------------------------------------------------------------------

func (s *Sync) upcomingOrderRows(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error) {
	rows, stats, err := s.ERP.GetUpcomingOrderRowsFull(ctx, from, to)
	if err != nil {
		s.App.Notify.Logf("⚠️  expanderad Monitor-query (orderrader) avvisades — faller tillbaka till bas-query; nya fält blir tomma: %v", err)
		return s.ERP.GetUpcomingOrderRows(ctx, from, to)
	}
	return rows, stats, nil
}

func (s *Sync) purchaseOrder(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error) {
	po, err := s.ERP.GetPurchaseOrderFull(ctx, id)
	if err != nil {
		s.App.Notify.Logf("⚠️  expanderad Monitor-query (order %d) avvisades — faller tillbaka till bas-query; nya fält blir tomma: %v", id, err)
		return s.ERP.GetPurchaseOrder(ctx, id)
	}
	return po, nil
}

func (s *Sync) partsByIds(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error) {
	parts, err := s.ERP.GetPartsByIdsFull(ctx, ids)
	if err != nil {
		s.App.Notify.Logf("⚠️  expanderad Monitor-query (artiklar) avvisades — faller tillbaka till bas-query; nya fält blir tomma: %v", err)
		return s.ERP.GetPartsByIds(ctx, ids)
	}
	return parts, nil
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

		// Cert-bärande radfält (Task 6): rå kravtext, nil-säker för
		// Comment-pekarna. Skalära fält kommer utan $expand.
		ReceivingMessage:               commentText(row.ReceivingMessage),
		ReceivingInspectionInstruction: commentText(row.ReceivingInspectionInstruction),
		RowGoodsLabel:                  row.RowsGoodsLabel,
		RowNotes:                       row.RowNotes,
		SupplierDrawingNumber:          row.SupplierDrawingNumber,
		SupplierRevisionNumber:         row.SupplierRevisionNumber,
		FreeText:                       row.FreeText,

		// Cert-bärande orderfält (Task 6), redan uppslagna en gång per
		// order i resolveOrders.
		OrderGoodsLabel:            info.GoodsLabel,
		ExternalComment:            info.ExternalComment,
		BusinessContactOrderNumber: info.BusinessContactOrderNumber,
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

		// Cert-bärande artikelfält (Task 6). PartCode finns inte i
		// monitor.Part — medvetet utelämnad, se Task 6-briefen.
		if part.CurrentAlloy != nil {
			r.AlloyCode = part.CurrentAlloy.Code
			r.AlloyDescription = part.CurrentAlloy.Description
		}
		r.PartReceivingInstruction = commentText(part.ReceivingInstruction)
		r.PartPurchaseComment = commentText(part.PurchaseComment)
		r.PartComment = commentText(part.Comment)
		r.PartLength = part.Length
		r.PartWidth = part.Width
		r.PartHeight = part.Height
		r.WeightPerUnit = part.WeightPerUnit
		r.GoodsType = part.GoodsType
		r.CategoryString = part.CategoryString
		if part.ExtraFields != nil {
			r.ExtraFieldsRaw = string(part.ExtraFields)
		}
		r.Hyperlinks = buildHyperlinks(part.HyperLinks)
		r.DrawingNumbers = joinDrawingNumbers(part.Drawings)
	}
	return r
}

// commentText läser RawText nil-säkert — Comment-referenser (godsmeddelande,
// mottagningsinstruktion m.fl.) kommer bara med om $expand:ades.
func commentText(c *monitor.Comment) string {
	if c == nil {
		return ""
	}
	return c.RawText
}

func buildHyperlinks(links []monitor.HyperLink) []domain.Hyperlink {
	if len(links) == 0 {
		return nil
	}
	out := make([]domain.Hyperlink, len(links))
	for i, l := range links {
		out[i] = domain.Hyperlink{Link: l.Link, Description: l.Description}
	}
	return out
}

// joinDrawingNumbers slår ihop artikelns ritningsnummer, kommaseparerat
// (visas som text intill ritningslänken i UI:t).
func joinDrawingNumbers(drawings []monitor.Drawing) string {
	if len(drawings) == 0 {
		return ""
	}
	nums := make([]string, 0, len(drawings))
	for _, d := range drawings {
		nums = append(nums, d.DrawingNumber)
	}
	return strings.Join(nums, ", ")
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

		// Task 9: parsade krav (row.Req) + certets nya kolumner. Alla ingår
		// också i matchCacheKey nedan — ändrar de sig faller cachen.
		ReqMaterial:    row.Req.Material,
		ReqEnNorm:      row.Req.EnNorm,
		ReqCertType:    row.Req.CertType,
		ReqProductForm: row.Req.ProductForm,
		ReqDimensions:  row.Req.Dimensions,
		ReqImpact:      row.Req.Impact,

		CertNormSystem:        c.NormSystem,
		CertNormEdition:       c.NormEdition,
		CertImpactTempC:       c.ImpactTempC,
		CertImpactEnergyJ:     c.ImpactEnergyJ,
		CertIsEnglish:         c.IsEnglish,
		CertDeliveryCondition: c.DeliveryCondition,
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
// nyckel och en färsk dom. Task 9: MÅSTE täcka ALLA nya AI-inputfält (parsade
// krav + certets nya kolumner) — annars serveras stale verdicts när kraven
// eller certkolumnerna ändras.
func matchCacheKey(row *domain.OrderRow, c *domain.Cert) string {
	raw := fmt.Sprintf("part:%d|%s|cert:%d|%s|%s|%s|%s|%t"+
		"|req:%s|%s|%s|%s|%s|%s"+
		"|certcol:%s|%s|%s|%s|%s|%t",
		row.PartID, row.ExtraDescription,
		c.ID, c.EffectiveMaterial(), c.EffectiveDimensions(), c.EffectiveProductForm(),
		c.EffectiveCertType(), row.CertRequired,
		row.Req.Material, row.Req.EnNorm, row.Req.CertType,
		row.Req.ProductForm, row.Req.Dimensions, row.Req.Impact,
		c.NormSystem, c.NormEdition, fmtPtr(c.ImpactTempC),
		fmtFloat(c.ImpactEnergyJ), c.DeliveryCondition, c.IsEnglish)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// fmtPtr serialiserar ett *float64 stabilt för cachenyckeln: "nil" när ej satt,
// annars talet (så nil skiljs från 0).
func fmtPtr(f *float64) string {
	if f == nil {
		return "nil"
	}
	return fmtFloat(*f)
}

func fmtFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ---------------------------------------------------------------------------
// Kravtolkning per rad, cachad (Task 8). Artikelns kravtexter blir persisterad
// strukturerad sanning på raden — oförändrade artiklar omparsas ALDRIG.
// ---------------------------------------------------------------------------

// ParseAllRequirements tolkar kravtexterna för varje rad som kräver cert eller
// bär någon kravtext. Cache-träff → applicera utan AI-anrop; cache-miss → AI +
// cache-skrivning. Per-rad-AI-fel loggas och hoppas över (samma tolerans som
// JudgeAll). All skrivning går via App.SetRowRequirements.
func (s *Sync) ParseAllRequirements(ctx context.Context) error {
	rows, err := s.App.Repo.ListOrderRows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !requirementsCandidate(row) {
			continue
		}
		in := buildRequirementsInput(row)
		key := requirementsCacheKey(row.PartID, in)

		// Cache-träff: applicera bara vid diff (raden saknar kraven eller de
		// skiljer sig) — undvik onödiga skrivningar. INGET AI-anrop.
		if cached, err := s.App.Repo.GetRequirementsCache(ctx, key); err == nil {
			if row.Req != *cached {
				if err := s.App.SetRowRequirements(ctx, row.DeliveryRowID, *cached); err != nil {
					return err
				}
			}
			continue
		}

		// Cache-miss: kräver AI-porten. Utan nyckel fylls kraven i när den finns.
		if s.Judge == nil {
			continue
		}
		ar, err := s.Judge.ParseRequirements(ctx, in)
		if err != nil {
			s.App.Notify.Logf("⚠️  kravtolkning %s: %v", row.PartNumber, err)
			continue
		}
		req := requirementsFromAI(ar)
		if err := s.App.SetRowRequirements(ctx, row.DeliveryRowID, req); err != nil {
			return err
		}
		if err := s.App.Repo.PutRequirementsCache(ctx, key, &req, time.Now().UTC().Format(time.RFC3339)); err != nil {
			s.App.Notify.Logf("⚠️  krav-cache-skrivning: %v", err)
		}
	}
	return nil
}

// requirementsCandidate: raden behöver tolkas om den kräver cert eller bär
// någon kravtext (annars finns inget att tolka).
func requirementsCandidate(r *domain.OrderRow) bool {
	if r.CertRequired {
		return true
	}
	return strings.TrimSpace(r.ExtraDescription) != "" ||
		strings.TrimSpace(r.ReceivingMessage) != "" ||
		strings.TrimSpace(r.ReceivingInspectionInstruction) != "" ||
		strings.TrimSpace(r.PartReceivingInstruction) != "" ||
		strings.TrimSpace(r.ExternalComment) != "" ||
		strings.TrimSpace(r.FreeText) != ""
}

// buildRequirementsInput samlar radens alla kravbärande texter/mått till
// AI-inputen. Samma uppsättning ligger till grund för cachenyckeln nedan.
func buildRequirementsInput(r *domain.OrderRow) ai.RequirementsInput {
	return ai.RequirementsInput{
		Description:              r.Description,
		ExtraDescription:         r.ExtraDescription,
		ReceivingMessage:         r.ReceivingMessage,
		RowInspectionInstruction: r.ReceivingInspectionInstruction,
		PartReceivingInstruction: r.PartReceivingInstruction,
		RowGoodsLabel:            r.RowGoodsLabel,
		OrderGoodsLabel:          r.OrderGoodsLabel,
		RowNotes:                 r.RowNotes,
		FreeText:                 r.FreeText,
		ExternalComment:          r.ExternalComment,
		AlloyCode:                r.AlloyCode,
		AlloyDescription:         r.AlloyDescription,
		PartLength:               r.PartLength,
		PartWidth:                r.PartWidth,
		PartHeight:               r.PartHeight,
	}
}

// requirementsCacheKey hashar artikeln + ALLA fält som skickas till AI:n (i
// samma ordning som buildRequirementsInput bygger). Ändras någon text/mått →
// ny nyckel → färsk tolkning (aldrig stale krav från en gammal beställningstext).
func requirementsCacheKey(partID int64, in ai.RequirementsInput) string {
	raw := fmt.Sprintf("requirements|part:%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%g|%g|%g",
		partID, in.Description, in.ExtraDescription, in.ReceivingMessage,
		in.RowInspectionInstruction, in.PartReceivingInstruction, in.RowGoodsLabel,
		in.OrderGoodsLabel, in.RowNotes, in.FreeText, in.ExternalComment,
		in.AlloyCode, in.AlloyDescription, in.PartLength, in.PartWidth, in.PartHeight)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// requirementsFromAI mappar AI-svaret till domänens rena krav-struct.
func requirementsFromAI(ar *ai.ArticleRequirements) domain.RowRequirements {
	return domain.RowRequirements{
		Material:    ar.RequiredMaterial,
		EnNorm:      ar.RequiredEnNorm,
		CertType:    ar.RequiredCertType,
		English:     ar.RequiresEnglish,
		ProductForm: ar.RequiredProductForm,
		Dimensions:  ar.RequiredDimensions,
		Impact:      ar.RequiredImpact,
		Notes:       ar.Notes,
	}
}
