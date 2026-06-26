package worker

// Kommande inleveranser: hämta Monitor-rader, matcha mot cert vi har, döm
// materialet med AI (ren AI-dom, men evidens lagras separat) och merge:a in i
// upcoming_deliveries. Plus rena schema-funktioner (NextRun/ShouldCatchUp) som är
// tabelltestbara med injicerad klocka. worker→monitor är ofarligt (leaf-paket).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"cert-renamer/internal/ai"
	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// RefreshUpcoming hämtar kommande inleveranser i fönstret [nu, nu+WindowDays],
// matchar varje rad mot cert vi har, dömer materialet (AI) för cert-krävande
// matchade rader, och merge:ar resultatet. Returnerar antal rader. Avbrytbar via
// ctx (select mellan Monitor-anrop, respekterar Pellys flaky nät).
func RefreshUpcoming(ctx context.Context, mc *monitor.Client, repo *store.Repository, cfg store.Config, n Notifier) (int, error) {
	from := time.Now()
	to := from.AddDate(0, 0, cfg.UpcomingWindowDays)
	rows, err := mc.GetUpcomingDeliveryRows(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("hämta kommande inleveranser: %w", err)
	}
	n.Logf("📦 %d kommande inleveransrader i fönstret %s–%s", len(rows), from.Format("2006-01-02"), to.Format("2006-01-02"))

	// Batcha unika ordrar (ordernummer + leverantör) en gång.
	orders := resolveOrders(ctx, mc, rows, n)

	// Artikeln kommer normalt inline via $expand. För rader där den saknas:
	// batch-hämta dem via GetPartsByIds (faller tillbaka på inline-datan annars).
	parts := fetchMissingParts(ctx, mc, rows, n)

	// AI-klient byggs bara om en API-nyckel finns; används bara när cert matchat.
	var aiClient *anthropic.Client
	if strings.TrimSpace(cfg.ApiKey) != "" {
		c := anthropic.NewClient(option.WithAPIKey(cfg.ApiKey))
		aiClient = &c
	}

	out := make([]store.UpcomingDelivery, 0, len(rows))
	for _, row := range rows {
		select {
		case <-ctx.Done():
			return len(out), ctx.Err()
		default:
		}
		out = append(out, buildUpcomingRow(ctx, mc, repo, aiClient, n, row, orders, parts))
	}

	if err := repo.MergeUpcomingDeliveries(out); err != nil {
		return len(out), fmt.Errorf("merge: %w", err)
	}
	n.Logf("📦 Kommande inleveranser uppdaterade: %d rader", len(out))
	return len(out), nil
}

type orderInfo struct {
	OrderNumber  string
	SupplierName string
}

// resolveOrders hämtar ordernummer + leverantör för varje unik PurchaseOrderId.
func resolveOrders(ctx context.Context, mc *monitor.Client, rows []monitor.PurchaseOrderDeliveryRow, n Notifier) map[monitor.ID]orderInfo {
	infos := map[monitor.ID]orderInfo{}
	for _, row := range rows {
		id := row.PurchaseOrderId
		if id == 0 {
			continue
		}
		if _, ok := infos[id]; ok {
			continue
		}
		select {
		case <-ctx.Done():
			return infos
		default:
		}
		info := orderInfo{}
		po, err := mc.GetPurchaseOrder(ctx, id)
		if err != nil {
			n.Logf("⚠️ kunde inte hämta order %d: %v", id, err)
		} else if po != nil {
			info.OrderNumber = po.OrderNumber
			if po.BusinessContactId != 0 {
				if sup, serr := mc.GetSupplier(ctx, po.BusinessContactId); serr == nil && sup != nil {
					info.SupplierName = supplierDisplay(sup)
				}
			}
		}
		infos[id] = info
	}
	return infos
}

// fetchMissingParts batch-hämtar artiklar för de rader vars inline-$expand inte
// gav någon Part. Returnerar en (möjligen tom) karta; fel loggas men stoppar inte.
func fetchMissingParts(ctx context.Context, mc *monitor.Client, rows []monitor.PurchaseOrderDeliveryRow, n Notifier) map[monitor.ID]monitor.Part {
	var missing []monitor.ID
	for _, row := range rows {
		if pr := row.PurchaseOrderRow; pr != nil && pr.Part == nil && pr.PartId != 0 {
			missing = append(missing, pr.PartId)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	parts, err := mc.GetPartsByIds(ctx, missing)
	if err != nil {
		n.Logf("⚠️ kunde inte hämta artiklar för %d rader: %v", len(missing), err)
		return nil
	}
	return parts
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

// buildUpcomingRow bygger en lagrings-rad: rad→Part-join, cert-matchning,
// cert_status-härledning, AI-dom (om cert matchat) och evidens.
func buildUpcomingRow(ctx context.Context, mc *monitor.Client, repo *store.Repository, aiClient *anthropic.Client, n Notifier, row monitor.PurchaseOrderDeliveryRow, orders map[monitor.ID]orderInfo, parts map[monitor.ID]monitor.Part) store.UpcomingDelivery {
	info := orders[row.PurchaseOrderId]
	ud := store.UpcomingDelivery{
		DeliveryRowID:      int64(row.ID),
		PurchaseOrderID:    int64(row.PurchaseOrderId),
		PurchaseOrderRowID: int64(row.PurchaseOrderRowId),
		OrderNumber:        info.OrderNumber,
		SupplierName:       info.SupplierName,
		DeliveryDate:       normalizeDate(row.DeliveryDate),
		DeliveryRaw:        string(row.Raw),
		LocalStatus:        store.UpcomingPending,
		CertStatus:         store.CertNoneRequired,
		MatchBy:            store.MatchByNone,
		MaterialOK:         store.MaterialUnknown,
	}

	var part *monitor.Part
	if pr := row.PurchaseOrderRow; pr != nil {
		ud.PartID = int64(pr.PartId)
		ud.PlannedQty = pr.RestQuantity // VERIFIERA: planerad inkommande mängd
		part = pr.Part
		if part == nil && pr.PartId != 0 { // inline saknades → använd batch-hämtad
			if p, ok := parts[pr.PartId]; ok {
				part = &p
			}
		}
	}
	if part != nil {
		ud.PartNumber = part.PartNumber
		ud.PartRaw = string(part.Raw)
		ud.CertRequired = part.RequiresCert()
	}

	evidence := map[string]any{
		"part_number":       ud.PartNumber,
		"part_description":  partField(part, func(p *monitor.Part) string { return p.Description }),
		"extra_description": partField(part, func(p *monitor.Part) string { return p.ExtraDescription }),
		"order_number":      ud.OrderNumber,
	}

	if ud.CertRequired {
		certs, err := repo.ListCertificatesMatchingOrder(ud.OrderNumber)
		if err != nil {
			n.Logf("⚠️ cert-sökning för %s: %v", ud.OrderNumber, err)
		}
		matched, matchBy := pickCert(ctx, mc, certs, monitor.ID(ud.PartID))
		if matched == nil {
			ud.CertStatus = store.CertMissing // mjuk varning (cert kommer ofta dagen efter godset)
		} else {
			ud.CertStatus = store.CertMatched
			ud.MatchBy = matchBy
			ud.CertFilename = matched.Filename
			ud.OurMaterial = effectiveMaterial(matched)
			ud.Dimensions = effectiveDimensions(matched)
			evidence["cert_filename"] = matched.Filename
			evidence["cert_material"] = effectiveMaterial(matched)
			evidence["cert_charge"] = effectiveCharge(matched)
			evidence["match_by"] = matchBy
			if part != nil && aiClient != nil {
				dom := classifyWithCache(ctx, repo, aiClient, n, part, matched, ud.CertRequired)
				ud.RequiredMaterial = dom.RequiredMaterial
				ud.RequiredCert = dom.RequiredCert
				if strings.TrimSpace(dom.OurMaterial) != "" {
					ud.OurMaterial = dom.OurMaterial
				}
				ud.MaterialOK = dom.MaterialOK
				ud.Notes = dom.Notes
			}
		}
	}
	ud.EvidenceJSON = toJSON(evidence)
	return ud
}

// pickCert väljer certet för en rad. 1 B-nummerträff → den. Flera → förfina via
// charge→ProductRecord→PartId mot radens PartId. Kan inte förfinas → första träffen.
func pickCert(ctx context.Context, mc *monitor.Client, certs []store.Certificate, partID monitor.ID) (*store.Certificate, string) {
	switch len(certs) {
	case 0:
		return nil, ""
	case 1:
		return &certs[0], store.MatchByBNumber
	}
	for i := range certs {
		charge := effectiveCharge(&certs[i])
		if charge == "" {
			continue
		}
		recs, err := mc.FindProductRecords(ctx, charge)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.PartId == partID {
				return &certs[i], store.MatchByChargePart
			}
		}
	}
	return &certs[0], store.MatchByBNumber
}

// classifyWithCache returnerar AI-domen, cachead per innehålls-hash så identiska
// rader inte betalas varje kväll.
func classifyWithCache(ctx context.Context, repo *store.Repository, client *anthropic.Client, n Notifier, part *monitor.Part, cert *store.Certificate, certRequired bool) ai.UpcomingClassification {
	key := classifyCacheKey(part, cert, certRequired)
	if cached, _ := repo.GetUpcomingClassification(key); cached != nil {
		return ai.UpcomingClassification{
			RequiredMaterial: cached.RequiredMaterial,
			RequiredCert:     cached.RequiredCert,
			OurMaterial:      cached.OurMaterial,
			MaterialOK:       cached.MaterialOK,
			Notes:            cached.Notes,
		}
	}
	in := ai.UpcomingClassifyInput{
		PartNumber:       part.PartNumber,
		Description:      part.Description,
		ExtraDescription: part.ExtraDescription,
		CertRequired:     certRequired,
		CertMaterial:     effectiveMaterial(cert),
		CertType:         cert.CertType,
		CertDimensions:   effectiveDimensions(cert),
	}
	dom, err := ai.ClassifyUpcoming(ctx, n, client, in)
	if err != nil {
		n.Logf("⚠️ materialdom misslyckades: %v", err)
		return ai.UpcomingClassification{MaterialOK: store.MaterialUnknown}
	}
	_ = repo.SaveUpcomingClassification(key, store.UpcomingClassificationCache{
		RequiredMaterial: dom.RequiredMaterial,
		RequiredCert:     dom.RequiredCert,
		OurMaterial:      dom.OurMaterial,
		MaterialOK:       dom.MaterialOK,
		Notes:            dom.Notes,
	})
	return *dom
}

func classifyCacheKey(part *monitor.Part, cert *store.Certificate, certRequired bool) string {
	raw := fmt.Sprintf("%d|%s|%s|%s|%s|%t",
		part.ID, part.ExtraDescription, cert.Filename, effectiveMaterial(cert), cert.CertType, certRequired)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// effective* föredrar mänskligt korrigerade fält framför AI-extraherade.
func effectiveMaterial(c *store.Certificate) string {
	if strings.TrimSpace(c.CorrectedMaterial) != "" {
		return c.CorrectedMaterial
	}
	return c.Material
}

func effectiveCharge(c *store.Certificate) string {
	if strings.TrimSpace(c.CorrectedCharge) != "" {
		return c.CorrectedCharge
	}
	return c.Charge
}

func effectiveDimensions(c *store.Certificate) string {
	if strings.TrimSpace(c.CorrectedDimensions) != "" {
		return c.CorrectedDimensions
	}
	return c.Dimensions
}

func partField(p *monitor.Part, get func(*monitor.Part) string) string {
	if p == nil {
		return ""
	}
	return get(p)
}

// normalizeDate trimmar Monitors DeliveryDate till "YYYY-MM-DD" för visning.
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

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// --- Schema (rena funktioner med injicerbar klocka) ---

// NextRun är nästa väggklockstid då det schemalagda upcoming-jobbet ska köra:
// dagens UpcomingTime om den inte passerat, annars morgondagens.
func NextRun(now time.Time, cfg store.Config) time.Time {
	hh, mm := parseHHMM(cfg.UpcomingTime)
	target := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// ShouldCatchUp avgör om en körning ska triggas NU: dagens schemalagda tid har
// passerat och ingen körning har skett sedan dess. Fångar både dagens schemaläge
// och catch-up när appen varit avstängd/sovande över måltiden. lastRun==zero
// (aldrig körd) → kör om tiden passerat.
func ShouldCatchUp(lastRun, now time.Time, cfg store.Config) bool {
	hh, mm := parseHHMM(cfg.UpcomingTime)
	todayTarget := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if now.Before(todayTarget) {
		return false
	}
	return lastRun.Before(todayTarget)
}

func parseHHMM(s string) (int, int) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		t, _ = time.Parse("15:04", store.DefaultUpcomingTime)
	}
	return t.Hour(), t.Minute()
}
