package monitorsync

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cert-renamer/internal/v2/ai"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/monitor"
	"cert-renamer/internal/v2/store"
)

// fakeERP serverar ett fast Monitor-fönster.
type fakeERP struct {
	rows    []monitor.PurchaseOrderRow
	orders  map[monitor.ID]*monitor.PurchaseOrder
	sups    map[monitor.ID]*monitor.Supplier
	records map[string][]monitor.ProductRecord // charge → records

	// fullErr != nil ⇒ alla *Full-varianter felar, så sync-lagret måste falla
	// tillbaka till bas-queryerna. Räknarna bevisar att fallbacken skedde.
	fullErr        error
	plainRowsCalls int
	plainPOCalls   int
	plainPartCalls int
}

// Bas-varianterna (delas med V1) — räknas så fallback-testet kan bevisa att de anropas.
func (f *fakeERP) GetUpcomingOrderRows(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error) {
	f.plainRowsCalls++
	return f.rows, monitor.UpcomingFetchStats{Fetched: len(f.rows)}, nil
}
func (f *fakeERP) GetPurchaseOrder(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error) {
	f.plainPOCalls++
	return f.orders[id], nil
}
func (f *fakeERP) GetSupplier(ctx context.Context, id monitor.ID) (*monitor.Supplier, error) {
	return f.sups[id], nil
}
func (f *fakeERP) GetPartsByIds(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error) {
	f.plainPartCalls++
	return nil, nil
}
func (f *fakeERP) FindProductRecords(ctx context.Context, charge string) ([]monitor.ProductRecord, error) {
	return f.records[charge], nil
}

// *Full-varianterna: felar om fullErr satt (annars samma data som bas-varianten).
func (f *fakeERP) GetUpcomingOrderRowsFull(ctx context.Context, from, to time.Time) ([]monitor.PurchaseOrderRow, monitor.UpcomingFetchStats, error) {
	if f.fullErr != nil {
		return nil, monitor.UpcomingFetchStats{}, f.fullErr
	}
	return f.rows, monitor.UpcomingFetchStats{Fetched: len(f.rows)}, nil
}
func (f *fakeERP) GetPurchaseOrderFull(ctx context.Context, id monitor.ID) (*monitor.PurchaseOrder, error) {
	if f.fullErr != nil {
		return nil, f.fullErr
	}
	return f.orders[id], nil
}
func (f *fakeERP) GetPartsByIdsFull(ctx context.Context, ids []monitor.ID) (map[monitor.ID]monitor.Part, error) {
	if f.fullErr != nil {
		return nil, f.fullErr
	}
	return nil, nil
}

// fakeJudge räknar anrop och svarar med fast dom.
type fakeJudge struct {
	calls      int // ClassifyUpcoming-anrop
	ok         string
	parseCalls int // ParseRequirements-anrop
}

func (f *fakeJudge) ClassifyUpcoming(ctx context.Context, in ai.UpcomingClassifyInput) (*ai.UpcomingClassification, error) {
	f.calls++
	return &ai.UpcomingClassification{
		RequiredMaterial: "S690QL", RequiredCert: "3.1",
		OurMaterial: in.CertMaterial, MaterialOK: f.ok,
		RequiredProductForm: "plåt", ProductFormOK: "ok", Notes: "test",
	}, nil
}

// ParseRequirements räknar anrop och ekar radens ExtraDescription i Notes så
// testerna kan se att en ändrad text faktiskt gav en färsk tolkning (inte bara
// en ny anropsräkning). Övriga fält är fasta.
func (f *fakeJudge) ParseRequirements(ctx context.Context, in ai.RequirementsInput) (*ai.ArticleRequirements, error) {
	f.parseCalls++
	return &ai.ArticleRequirements{
		RequiredMaterial: "S355J2+N", RequiredEnNorm: "EN 10025-2",
		RequiredCertType: "3.1", RequiresEnglish: true,
		RequiredProductForm: "plåt", RequiredDimensions: "16",
		RequiredImpact: "27J/-20°C", Notes: in.ExtraDescription,
	}, nil
}

func part(id monitor.ID, num, extra string) *monitor.Part {
	p := monitor.Part{PartNumber: num, Description: "PL " + num, ExtraDescription: extra}
	p.ID = id
	// RequiresCert styrs av rå-fält vi inte sätter här; CertRequired sätts
	// via raw i testerna genom att bygga Part med ReceivingInspectionType.
	return &p
}

func mkRow(rowID, orderID, partID monitor.ID, p *monitor.Part, date string) monitor.PurchaseOrderRow {
	r := monitor.PurchaseOrderRow{ParentOrderId: orderID, PartId: partID, Part: p,
		DeliveryDate: date, RestQuantity: 6, Raw: json.RawMessage(`{}`)}
	r.ID = rowID
	return r
}

func testSync(t *testing.T, erp ERP, judge Judge) *Sync {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := store.Config{InboxDir: dir, UpcomingWindowDays: 14, UpcomingBackDays: 365, UpcomingTime: "16:30"}
	a := app.New(store.NewRepository(db), func() store.Config { return cfg },
		func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) }, nil)
	return &Sync{App: a, ERP: erp, Judge: judge, Config: func() store.Config { return cfg }}
}

func seedLivingCert(t *testing.T, a *app.App, charge string, bNums []string) *domain.Cert {
	t.Helper()
	c := &domain.Cert{
		PdfHash: "hash-" + charge, OriginalFilename: "x.pdf", StoredName: "s.pdf",
		CertType: "3.1", Charge: charge, Material: "S690QL",
		EnStandardPresent: true, IsEnglish: true, ProductForm: "plåt", Dimensions: "60",
		BNumbers: bNums, ReceivedAt: "t",
	}
	if _, err := a.Repo.InsertCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRefreshUpsertsAndPreservesBookkeeping(t *testing.T) {
	erp := &fakeERP{
		rows: []monitor.PurchaseOrderRow{
			mkRow(101, 1, 11, part(11, "30-101241-001", "Plåt t=60 S690QL EN 10025-6"), "2026-07-10"),
			mkRow(103, 1, 0, nil, "2026-07-11"), // operationsrad utan artikel — släpps
		},
		orders: map[monitor.ID]*monitor.PurchaseOrder{1: {OrderNumber: "B127575", BusinessContactId: 5}},
		sups:   map[monitor.ID]*monitor.Supplier{5: {Name: "SSAB"}},
	}
	s := testSync(t, erp, nil)
	ctx := context.Background()

	n, err := s.Refresh(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Refresh: n=%d err=%v", n, err)
	}
	row, err := s.App.Repo.GetOrderRow(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if row.OrderNumber != "B127575" || row.SupplierName != "SSAB" ||
		row.ExtraDescription != "Plåt t=60 S690QL EN 10025-6" || row.DeliveryDate != "2026-07-10" {
		t.Errorf("rad: %+v", row)
	}

	// Lokal bokföring + andra sync-cykeln
	if err := s.App.MarkDelivered(ctx, []int64{101}, true); err != nil {
		t.Fatal(err)
	}
	first, _ := s.App.Repo.GetOrderRow(ctx, 101)

	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = s.App.Repo.GetOrderRow(ctx, 101)
	if !row.Delivered || row.FirstSeen != first.FirstSeen {
		t.Error("delivered/first_seen ska överleva refresher")
	}

	// Rad som försvinner ur fönstret: kvar med in_monitor=0
	erp.rows = nil
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.App.Repo.GetOrderRow(ctx, 101)
	if err != nil {
		t.Fatal("rad utanför fönstret raderades")
	}
	if row.InMonitor {
		t.Error("in_monitor ska vara 0 när raden inte återkom")
	}
}

// FIX 1: när de cert-rika *Full-varianterna felar (Monitor avvisar den overifierade
// expanden) MÅSTE sync falla tillbaka till bas-queryerna — refreshen får aldrig
// hard-faila, raden synkas ändå och ordernummer/leverantör bevaras (nollställs inte).
func TestRefreshFallsBackWhenFullQueriesFail(t *testing.T) {
	erp := &fakeERP{
		rows: []monitor.PurchaseOrderRow{
			mkRow(201, 2, 22, part(22, "40-202-002", "Plåt S355"), "2026-07-10"),
		},
		orders:  map[monitor.ID]*monitor.PurchaseOrder{2: {OrderNumber: "B999", BusinessContactId: 9}},
		sups:    map[monitor.ID]*monitor.Supplier{9: {Name: "BE Group"}},
		fullErr: errors.New("400 Bad Request: expand rejected"),
	}
	s := testSync(t, erp, nil)
	ctx := context.Background()

	n, err := s.Refresh(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Refresh med Full-fel ska inte hard-faila: n=%d err=%v", n, err)
	}
	// Bas-queryerna måste ha anropats som fallback (en gång vardera).
	if erp.plainRowsCalls != 1 {
		t.Errorf("bas GetUpcomingOrderRows-anrop = %d, vill ha 1 (fallback)", erp.plainRowsCalls)
	}
	if erp.plainPOCalls != 1 {
		t.Errorf("bas GetPurchaseOrder-anrop = %d, vill ha 1 (fallback)", erp.plainPOCalls)
	}
	row, err := s.App.Repo.GetOrderRow(ctx, 201)
	if err != nil {
		t.Fatal(err)
	}
	// Ordern får ALDRIG tyst nollställas när bara expanden är problemet.
	if row.OrderNumber != "B999" || row.SupplierName != "BE Group" {
		t.Errorf("orderfält nollställdes vid Full-fel: %+v", row)
	}
	if row.PartNumber != "40-202-002" || row.ExtraDescription != "Plåt S355" {
		t.Errorf("artikelidentiteten tappades vid fallback: %+v", row)
	}
}

// TestBuildOrderRowMapsCertBearingFields bevisar att Task 6:s cert-bärande
// fält (radens/orderns/artikelns kravtexter + legering + hyperlinks/ritningar)
// landar korrekt i domain.OrderRow — nested Comment-pekare, Alloy, HyperLinks
// och Drawings inkluderade. Ett andra fall bevisar nil-säkerheten när inget
// av detta är expanderat/satt.
func TestBuildOrderRowMapsCertBearingFields(t *testing.T) {
	orders := map[monitor.ID]orderInfo{
		1: {
			OrderNumber: "B127575", SupplierName: "SSAB",
			GoodsLabel: "ordergodsmärke", ExternalComment: "extern kommentar",
			BusinessContactOrderNumber: "LEV-9",
		},
	}
	p := monitor.Part{
		PartNumber: "30-101241-001", Description: "PL 060 S690QL",
		CurrentAlloy:         &monitor.Alloy{Code: "S690QL", Description: "Höghållfast stål"},
		ReceivingInstruction: &monitor.Comment{RawText: "mottagningsinstruktion"},
		PurchaseComment:      &monitor.Comment{RawText: "inköpskommentar"},
		Comment:              &monitor.Comment{RawText: "artikelkommentar"},
		Length:               6, Width: 2, Height: 0.06, WeightPerUnit: 850,
		GoodsType: "Plåt", CategoryString: "Stål",
		HyperLinks:  []monitor.HyperLink{{Link: "file://server/ritning.pdf", Description: "Ritning"}},
		Drawings:    []monitor.Drawing{{DrawingNumber: "D-100"}, {DrawingNumber: "D-101"}},
		ExtraFields: json.RawMessage(`[{"Type":1}]`),
	}
	p.ID = 11
	row := monitor.PurchaseOrderRow{
		ParentOrderId: 1, PartId: 11, Part: &p,
		DeliveryDate: "2026-07-10", RestQuantity: 6, Raw: json.RawMessage(`{}`),
		RowsGoodsLabel: "radgodsmärke", RowNotes: "radnotering",
		SupplierDrawingNumber: "SUP-D1", SupplierRevisionNumber: "A",
		FreeText:                       "fritext",
		ReceivingMessage:               &monitor.Comment{RawText: "godsmeddelande"},
		ReceivingInspectionInstruction: &monitor.Comment{RawText: "mottagningskontroll"},
	}
	row.ID = 101

	got := buildOrderRow(row, orders, nil)

	if got.ReceivingMessage != "godsmeddelande" || got.ReceivingInspectionInstruction != "mottagningskontroll" {
		t.Errorf("radkommentarer: %+v", got)
	}
	if got.RowGoodsLabel != "radgodsmärke" || got.RowNotes != "radnotering" ||
		got.SupplierDrawingNumber != "SUP-D1" || got.SupplierRevisionNumber != "A" || got.FreeText != "fritext" {
		t.Errorf("radfält: %+v", got)
	}
	if got.OrderGoodsLabel != "ordergodsmärke" || got.ExternalComment != "extern kommentar" ||
		got.BusinessContactOrderNumber != "LEV-9" {
		t.Errorf("orderfält: %+v", got)
	}
	if got.AlloyCode != "S690QL" || got.AlloyDescription != "Höghållfast stål" {
		t.Errorf("legering: %+v", got)
	}
	if got.PartReceivingInstruction != "mottagningsinstruktion" || got.PartPurchaseComment != "inköpskommentar" ||
		got.PartComment != "artikelkommentar" {
		t.Errorf("artikelkommentarer: %+v", got)
	}
	if got.PartLength != 6 || got.PartWidth != 2 || got.PartHeight != 0.06 || got.WeightPerUnit != 850 {
		t.Errorf("dimensioner: %+v", got)
	}
	if got.GoodsType != "Plåt" || got.CategoryString != "Stål" {
		t.Errorf("godsslag/kategori: %+v", got)
	}
	if got.ExtraFieldsRaw != `[{"Type":1}]` {
		t.Errorf("ExtraFieldsRaw = %q", got.ExtraFieldsRaw)
	}
	if len(got.Hyperlinks) != 1 || got.Hyperlinks[0].Link != "file://server/ritning.pdf" || got.Hyperlinks[0].Description != "Ritning" {
		t.Errorf("Hyperlinks: %+v", got.Hyperlinks)
	}
	if got.DrawingNumbers != "D-100, D-101" {
		t.Errorf("DrawingNumbers = %q", got.DrawingNumbers)
	}

	// Nil-säkert: rad utan artikel/kommentarer ska inte panika och lämna
	// de nya fälten tomma.
	bare := monitor.PurchaseOrderRow{ParentOrderId: 1, PartId: 0, Raw: json.RawMessage(`{}`)}
	bare.ID = 102
	gotBare := buildOrderRow(bare, orders, nil)
	if gotBare.ReceivingMessage != "" || gotBare.ReceivingInspectionInstruction != "" ||
		gotBare.AlloyCode != "" || gotBare.PartReceivingInstruction != "" ||
		len(gotBare.Hyperlinks) != 0 || gotBare.DrawingNumbers != "" {
		t.Errorf("nil-säkert fall ska lämna cert-bärande fält tomma: %+v", gotBare)
	}
	// Orderfälten hämtas oavsett artikel (de kommer från PurchaseOrder).
	if gotBare.OrderGoodsLabel != "ordergodsmärke" {
		t.Errorf("orderfält ska sättas även utan artikel: %+v", gotBare)
	}
}

func TestSuggestRefinesByChargeWithoutSilentFallback(t *testing.T) {
	// Två rader på samma order (olika artiklar). Certets charge pekar via
	// ProductRecords på artikel 22 → bara den raden föreslås, källa charge_part.
	pA, pB := part(11, "ART-A", ""), part(22, "ART-B", "")
	erp := &fakeERP{
		rows: []monitor.PurchaseOrderRow{
			mkRow(201, 1, 11, pA, "2026-07-10"),
			mkRow(202, 1, 22, pB, "2026-07-10"),
		},
		orders:  map[monitor.ID]*monitor.PurchaseOrder{1: {OrderNumber: "B128293"}},
		records: map[string][]monitor.ProductRecord{"43136": {{PartId: 22}}},
	}
	s := testSync(t, erp, nil)
	ctx := context.Background()
	c := seedLivingCert(t, s.App, "43136", []string{"B128293"})

	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	links, _ := s.App.Repo.ListLinksForCert(ctx, c.ID)
	if len(links) != 1 || links[0].DeliveryRowID != 202 || links[0].MatchSource != "auto_charge_part" {
		t.Errorf("förfinad matchning: %+v", links)
	}

	// Utan charge-träff: BÅDA raderna föreslås (ingen tyst första-träffen)
	c2 := seedLivingCert(t, s.App, "99999", []string{"B128293"})
	if err := s.SuggestAll(ctx); err != nil {
		t.Fatal(err)
	}
	links2, _ := s.App.Repo.ListLinksForCert(ctx, c2.ID)
	if len(links2) != 2 {
		t.Errorf("olöst tvetydighet ska ge alla kandidater: %+v", links2)
	}
}

func TestJudgeCachedWithWidenedKey(t *testing.T) {
	// CertRequired kräver rå-data; bygg raden med part_raw som ger RequiresCert.
	// Enklast: skriv orderraden direkt med CertRequired=true.
	s := testSync(t, &fakeERP{}, nil)
	judge := &fakeJudge{ok: "mismatch"}
	s.Judge = judge
	ctx := context.Background()

	if err := s.App.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 301, OrderNumber: "B128293", PartID: 11, PartNumber: "30-101241-001",
		ExtraDescription: "Plåt t=60 S690QL", CertRequired: true,
	}, "t"); err != nil {
		t.Fatal(err)
	}
	c := seedLivingCert(t, s.App, "43136", []string{"B128293"})
	if _, err := s.App.SuggestLink(ctx, c.ID, 301, "B128293", "auto_b_number"); err != nil {
		t.Fatal(err)
	}

	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 {
		t.Fatalf("judge-anrop = %d", judge.calls)
	}
	links, _ := s.App.Repo.ListLinksForCert(ctx, c.ID)
	if links[0].MaterialOK != "mismatch" || links[0].RequiredMaterial != "S690QL" {
		t.Errorf("dom: %+v", links[0])
	}

	// Samma data → cache-träff, inga nya anrop
	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 {
		t.Errorf("cachen missade: %d anrop", judge.calls)
	}

	// BREDDAD NYCKEL: dimensionsrättelse ska invalidera cachen (V1-buggen)
	if _, err := s.App.UpdateCertField(ctx, c.ID, "dimensions", "80", "rob"); err != nil {
		t.Fatal(err)
	}
	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 {
		t.Errorf("dimensionsrättelse invaliderade inte cachen: %d anrop", judge.calls)
	}
}

// TestJudgeCacheKeyCoversParsedRequirements bevisar Task 9: cachenyckeln täcker
// de nya AI-inputfälten. Samma rad + cert, men ett ändrat parsat krav
// (req_cert_type) måste ge ett NYTT judge-anrop — annars serveras en stale dom
// som fortfarande bygger på det gamla kravet.
func TestJudgeCacheKeyCoversParsedRequirements(t *testing.T) {
	s := testSync(t, &fakeERP{}, nil)
	judge := &fakeJudge{ok: "ok"}
	s.Judge = judge
	ctx := context.Background()

	if err := s.App.Repo.UpsertOrderRow(ctx, &domain.OrderRow{
		DeliveryRowID: 601, OrderNumber: "B128293", PartID: 11, PartNumber: "P-1",
		ExtraDescription: "Plåt S690QL", CertRequired: true,
		Req: domain.RowRequirements{Material: "S690QL", CertType: "3.1"},
	}, "t"); err != nil {
		t.Fatal(err)
	}
	c := seedLivingCert(t, s.App, "43136", []string{"B128293"})
	if _, err := s.App.SuggestLink(ctx, c.ID, 601, "B128293", "auto_b_number"); err != nil {
		t.Fatal(err)
	}

	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 {
		t.Fatalf("första domen: judge-anrop = %d, vill ha 1", judge.calls)
	}

	// Oförändrade krav → cache-träff, inga nya anrop.
	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 {
		t.Errorf("oförändrade krav borde ge cache-träff: %d anrop", judge.calls)
	}

	// Ändrat req_cert_type via SetRowRequirements → ny nyckel → ny dom.
	if err := s.App.SetRowRequirements(ctx, 601, domain.RowRequirements{Material: "S690QL", CertType: "3.2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.JudgeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 {
		t.Errorf("ändrat req_cert_type invaliderade inte judge-cachen: %d anrop", judge.calls)
	}
}

// TestRefreshParsesRequirementsWithCache täcker scenario (a) + (b): en rad med
// kravtexter tolkas och persisteras vid Refresh, och en andra Refresh med
// OFÖRÄNDRADE texter ger cache-träff — noll nya AI-anrop, kraven kvar.
func TestRefreshParsesRequirementsWithCache(t *testing.T) {
	erp := &fakeERP{
		rows: []monitor.PurchaseOrderRow{
			mkRow(401, 1, 11, part(11, "30-101241-001", "Plåt t=16 S355J2+N EN 10025-2 cert 3.1"), "2026-07-10"),
		},
		orders: map[monitor.ID]*monitor.PurchaseOrder{1: {OrderNumber: "B127575"}},
	}
	judge := &fakeJudge{ok: "ok"}
	s := testSync(t, erp, judge)
	ctx := context.Background()

	// (a) första Refresh: tolkar och persisterar kraven på raden.
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.parseCalls != 1 {
		t.Fatalf("parse-anrop efter första Refresh = %d, vill ha 1", judge.parseCalls)
	}
	row, err := s.App.Repo.GetOrderRow(ctx, 401)
	if err != nil {
		t.Fatal(err)
	}
	if row.Req.Material != "S355J2+N" || row.Req.EnNorm != "EN 10025-2" || row.Req.CertType != "3.1" ||
		!row.Req.English || row.Req.ProductForm != "plåt" || row.Req.Dimensions != "16" ||
		row.Req.Impact != "27J/-20°C" {
		t.Errorf("kraven ej persisterade: %+v", row.Req)
	}
	if row.ReqParsedAt.IsZero() {
		t.Error("ReqParsedAt ska stämplas när kraven skrivs")
	}

	// (b) andra Refresh, oförändrade texter: cache-träff → noll nya AI-anrop.
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.parseCalls != 1 {
		t.Errorf("cachen missade: %d parse-anrop efter andra Refresh", judge.parseCalls)
	}
	row2, _ := s.App.Repo.GetOrderRow(ctx, 401)
	if row2.Req.Material != "S355J2+N" {
		t.Errorf("kraven försvann vid re-sync: %+v", row2.Req)
	}
}

// FIX 9(c): en förhandsseeadad cache-post (raden saknar krav) ska appliceras på
// raden UTAN AI-anrop — bevisar apply-on-diff-grenen vid cache-träff.
func TestParseAllRequirementsAppliesCacheHitWithoutAI(t *testing.T) {
	erp := &fakeERP{}
	judge := &fakeJudge{}
	s := testSync(t, erp, judge)
	ctx := context.Background()

	// Synka in en kravkandidat-rad (kravtext i ExtraDescription) utan att köra
	// kravtolkningen — raden får inga krav ännu.
	seed := &domain.OrderRow{
		DeliveryRowID: 601, PurchaseOrderID: 6, OrderNumber: "B600", SupplierName: "SSAB",
		PartID: 66, PartNumber: "60-606-001", Description: "PL 60-606-001",
		ExtraDescription: "Plåt S355 cert 3.1", DeliveryDate: "2026-07-10",
	}
	if err := s.App.SyncOrderRows(ctx, []*domain.OrderRow{seed}); err != nil {
		t.Fatal(err)
	}

	row, err := s.App.Repo.GetOrderRow(ctx, 601)
	if err != nil {
		t.Fatal(err)
	}
	if !requirementsCandidate(row) {
		t.Fatal("raden borde vara kravkandidat (bär ExtraDescription)")
	}
	// Räkna ut den exakta cachenyckeln för raden och seeda cachen.
	key := requirementsCacheKey(row.PartID, buildRequirementsInput(row))
	cachedReq := domain.RowRequirements{
		Material: "S355J2+N", EnNorm: "EN 10025-2", CertType: "3.1",
		English: true, ProductForm: "plåt", Dimensions: "10",
	}
	if err := s.App.PutRequirementsCache(ctx, key, &cachedReq); err != nil {
		t.Fatal(err)
	}

	// Kravtolkningen: cache-träff → applicera utan att röra AI-porten.
	if err := s.ParseAllRequirements(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.parseCalls != 0 {
		t.Errorf("cache-träff ska inte anropa AI:n, fick %d ParseRequirements-anrop", judge.parseCalls)
	}
	row, _ = s.App.Repo.GetOrderRow(ctx, 601)
	if row.Req != cachedReq {
		t.Errorf("cachad krav applicerades inte på raden: %+v (vill ha %+v)", row.Req, cachedReq)
	}
	if row.ReqParsedAt.IsZero() {
		t.Error("ReqParsedAt ska stämplas när kraven appliceras från cache")
	}
}

// TestRequirementsResyncPreservesAndReparses täcker scenario (c): sync-upserten
// nollställer ALDRIG kraven, och en ändrad ExtraDescription ger ny nyckel → ny
// tolkning (nytt AI-anrop + uppdaterade krav).
func TestRequirementsResyncPreservesAndReparses(t *testing.T) {
	erp := &fakeERP{
		rows: []monitor.PurchaseOrderRow{
			mkRow(501, 1, 11, part(11, "P-1", "Plåt S355J2+N"), "2026-07-10"),
		},
		orders: map[monitor.ID]*monitor.PurchaseOrder{1: {OrderNumber: "B127575"}},
	}
	judge := &fakeJudge{ok: "ok"}
	s := testSync(t, erp, judge)
	ctx := context.Background()

	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.parseCalls != 1 {
		t.Fatalf("parse-anrop = %d, vill ha 1", judge.parseCalls)
	}
	row, _ := s.App.Repo.GetOrderRow(ctx, 501)
	if row.Req.Notes != "Plåt S355J2+N" {
		t.Fatalf("kraven ej satta: %+v", row.Req)
	}

	// Re-sync med OFÖRÄNDRAD text: upsert bevarar kraven, ingen ny tolkning.
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ = s.App.Repo.GetOrderRow(ctx, 501)
	if row.Req.Notes != "Plåt S355J2+N" {
		t.Error("upserten nollställde kraven vid re-sync")
	}
	if judge.parseCalls != 1 {
		t.Errorf("onödig re-tolkning vid oförändrad text: %d anrop", judge.parseCalls)
	}

	// ÄNDRAD ExtraDescription → ny nyckel → ny tolkning + uppdaterade krav.
	erp.rows[0] = mkRow(501, 1, 11, part(11, "P-1", "Rundstång S690QL"), "2026-07-10")
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.parseCalls != 2 {
		t.Errorf("ändrad text gav ingen re-tolkning: %d anrop", judge.parseCalls)
	}
	row, _ = s.App.Repo.GetOrderRow(ctx, 501)
	if row.Req.Notes != "Rundstång S690QL" {
		t.Errorf("kraven uppdaterades inte efter ändrad text: %+v", row.Req)
	}
}

func TestScheduleFunctions(t *testing.T) {
	tm := func(h, m int) time.Time { return time.Date(2026, 7, 3, h, m, 0, 0, time.UTC) }

	if got := NextRun(tm(10, 0), "16:30"); !got.Equal(tm(16, 30)) {
		t.Errorf("NextRun före måltid = %v", got)
	}
	if got := NextRun(tm(17, 0), "16:30"); !got.Equal(tm(16, 30).AddDate(0, 0, 1)) {
		t.Errorf("NextRun efter måltid = %v", got)
	}

	if ShouldCatchUp(time.Time{}, tm(10, 0), "16:30") {
		t.Error("före måltid: ingen catch-up")
	}
	if !ShouldCatchUp(time.Time{}, tm(17, 0), "16:30") {
		t.Error("aldrig körd + måltid passerad: catch-up")
	}
	if ShouldCatchUp(tm(16, 45), tm(17, 0), "16:30") {
		t.Error("redan körd efter måltid: ingen catch-up")
	}
	if !ShouldCatchUp(tm(16, 45).AddDate(0, 0, -1), tm(17, 0), "16:30") {
		t.Error("senaste körning igår: catch-up")
	}
}
