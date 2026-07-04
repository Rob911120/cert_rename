package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"cert-renamer/internal/v2/domain"
)

func testRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepository(db)
}

func TestCertRoundtrip(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	c := &domain.Cert{
		PdfHash: "abc123", OriginalFilename: "SSAB heat 43136.pdf", StoredName: "abc123__SSAB_heat_43136.pdf",
		EmailSubject: "Cert", EmailFrom: "mill@ssab.com", EmailDate: "2026-07-01",
		CertType: "3.1", Charge: "43136", Material: "S355J2+N",
		EnStandardPresent: true, IsEnglish: true,
		ProductForm: "plåt", ProductCode: "PL", Dimensions: "60", CountryOfOrigin: "Sverige",
		BNumbers: []string{"B128293"}, Confidence: "high", Issues: []string{},
		ModelUsed: "claude-sonnet", TokensInput: 1000, TokensOutput: 200, ProcessingMS: 1500,
		ReceivedAt: "2026-07-01T08:00:00Z",
	}
	id, err := repo.InsertCert(ctx, c)
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetCert(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.CertMottagen {
		t.Errorf("defaultstatus = %s", got.Status)
	}
	if got.Material != "S355J2+N" || got.Charge != "43136" || !got.EnStandardPresent {
		t.Errorf("råfält tappade: %+v", got)
	}
	if got.ProductCode != "PL" {
		t.Errorf("ProductCode tappades vid insert→scan: %q", got.ProductCode)
	}
	if !reflect.DeepEqual(got.BNumbers, []string{"B128293"}) {
		t.Errorf("BNumbers = %v", got.BNumbers)
	}
	if got.CorrectedBNumbers != nil {
		t.Errorf("orättad CorrectedBNumbers ska vara nil, är %v", got.CorrectedBNumbers)
	}

	// Rättelser + logg + override persisterar; nil vs tom slice skiljs åt
	got.CorrectedMaterial = "S690QL"
	got.CorrectedBNumbers = []string{}
	got.CorrectionLog = []domain.Correction{{TS: "t", Who: "rob", Field: "material", Old: "S355J2+N", New: "S690QL"}}
	got.NameOverride = "eget-namn.pdf"
	got.ProductCode = "RS" // direktredigerad kod persisteras via UpdateCertWork
	if err := repo.UpdateCertWork(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := repo.GetCert(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if again.CorrectedMaterial != "S690QL" || again.NameOverride != "eget-namn.pdf" {
		t.Errorf("arbetsfält tappade: %+v", again)
	}
	if again.ProductCode != "RS" {
		t.Errorf("redigerad ProductCode persisterade inte: %q", again.ProductCode)
	}
	if again.CorrectedBNumbers == nil || len(again.CorrectedBNumbers) != 0 {
		t.Errorf("rättad-till-inga ska vara tom icke-nil slice, är %#v", again.CorrectedBNumbers)
	}
	if len(again.CorrectionLog) != 1 || again.CorrectionLog[0].Who != "rob" {
		t.Errorf("CorrectionLog = %+v", again.CorrectionLog)
	}

	// Dedupe-uppslag på hash
	if _, err := repo.GetCertByHash(ctx, "abc123"); err != nil {
		t.Errorf("GetCertByHash: %v", err)
	}
	if _, err := repo.GetCertByHash(ctx, "finns-ej"); err != domain.ErrNotFound {
		t.Errorf("saknad hash: err = %v", err)
	}
}

// TestCertExtractionFieldsRoundtrip verifierar de kolumnkalibrerade
// extraktionsfälten från Task 1 (is_legible m.fl.): satta pekarvärden
// överlever en roundtrip, och nil-pekare förblir nil (inte 0).
func TestCertExtractionFieldsRoundtrip(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	impactTemp, cev, carbon, p, s, minTemp := -20.0, 0.43, 0.18, 0.012, 0.004, -40.0

	c := &domain.Cert{
		PdfHash: "ext-1", OriginalFilename: "cert.pdf", StoredName: "ext-1__cert.pdf",
		ReceivedAt: "2026-07-03T08:00:00Z",
		IsLegible:  false, IsUnaltered: false, NormSystem: "EN 10025-2",
		ImpactTempC: &impactTemp, ImpactEnergyJ: 27,
		NormEdition: "2019", PedDirective: "2014/68/EU",
		Cev: &cev, CarbonPct: &carbon, PPct: &p, SPct: &s,
		HasBendTest: true, HasIntergranularTest: true, HasStampPhoto: true,
		MinTemperatureC: &minTemp, DeliveryCondition: "N",
	}
	id, err := repo.InsertCert(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetCert(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsLegible || got.IsUnaltered {
		t.Errorf("IsLegible/IsUnaltered tappade: %+v", got)
	}
	if got.NormSystem != "EN 10025-2" || got.NormEdition != "2019" ||
		got.PedDirective != "2014/68/EU" || got.DeliveryCondition != "N" {
		t.Errorf("strängfält tappade: %+v", got)
	}
	if got.ImpactEnergyJ != 27 {
		t.Errorf("ImpactEnergyJ = %v, vill ha 27", got.ImpactEnergyJ)
	}
	if !got.HasBendTest || !got.HasIntergranularTest || !got.HasStampPhoto {
		t.Errorf("booleans tappade: %+v", got)
	}
	floatChecks := []struct {
		name      string
		want, got *float64
	}{
		{"ImpactTempC", &impactTemp, got.ImpactTempC},
		{"Cev", &cev, got.Cev},
		{"CarbonPct", &carbon, got.CarbonPct},
		{"PPct", &p, got.PPct},
		{"SPct", &s, got.SPct},
		{"MinTemperatureC", &minTemp, got.MinTemperatureC},
	}
	for _, fc := range floatChecks {
		if fc.got == nil {
			t.Errorf("%s = nil, vill ha %v", fc.name, *fc.want)
			continue
		}
		if *fc.got != *fc.want {
			t.Errorf("%s = %v, vill ha %v", fc.name, *fc.got, *fc.want)
		}
	}

	// Nil-pekare (fält aldrig satta) ska förbli nil efter roundtrip, inte 0.
	c2 := &domain.Cert{
		PdfHash: "ext-2", OriginalFilename: "cert2.pdf", StoredName: "ext-2__cert2.pdf",
		ReceivedAt: "2026-07-03T08:00:00Z",
	}
	id2, err := repo.InsertCert(ctx, c2)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := repo.GetCert(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ImpactTempC != nil || got2.Cev != nil || got2.CarbonPct != nil ||
		got2.PPct != nil || got2.SPct != nil || got2.MinTemperatureC != nil {
		t.Errorf("nil-pekare ska förbli nil, inte 0: %+v", got2)
	}
}

func TestOrderRowUpsertPreservesLocalBookkeeping(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	row := &domain.OrderRow{
		DeliveryRowID: 42, PurchaseOrderID: 7, OrderNumber: "B127575",
		PartNumber: "30-101241-001", Description: "PL 060 S690QL",
		ExtraDescription: "Plåt t=60 S690QL EN 10025-6", PlannedQty: 6,
		DeliveryDate: "2026-07-10", CertRequired: true,
	}
	if err := repo.UpsertOrderRow(ctx, row, "2026-07-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	// Lokal bokföring: markera levererad
	if err := repo.SetDelivered(ctx, []int64{42}, true); err != nil {
		t.Fatal(err)
	}

	// Sync-cykel: allt osett → upsert igen med nya Monitor-data
	if err := repo.MarkAllRowsUnseen(ctx); err != nil {
		t.Fatal(err)
	}
	row.PlannedQty = 4 // delleverans kvar
	if err := repo.UpsertOrderRow(ctx, row, "2026-07-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetOrderRow(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Delivered {
		t.Error("delivered skrevs över av sync — ska bevaras")
	}
	if got.FirstSeen != "2026-07-01T00:00:00Z" {
		t.Errorf("first_seen skrevs över: %s", got.FirstSeen)
	}
	if got.LastSeen != "2026-07-02T00:00:00Z" || got.PlannedQty != 4 {
		t.Errorf("Monitor-fält uppdaterades inte: %+v", got)
	}
	if !got.InMonitor {
		t.Error("in_monitor ska vara 1 efter upsert")
	}

	// Rad som INTE återkom i syncen: kvar men in_monitor=0
	other := &domain.OrderRow{DeliveryRowID: 43, OrderNumber: "B111111"}
	if err := repo.UpsertOrderRow(ctx, other, "2026-07-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkAllRowsUnseen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOrderRow(ctx, row, "2026-07-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	gone, err := repo.GetOrderRow(ctx, 43)
	if err != nil {
		t.Fatalf("rad utanför fönstret ska INTE raderas: %v", err)
	}
	if gone.InMonitor {
		t.Error("osedd rad ska ha in_monitor=0")
	}
}

// TestOrderRowCertFieldsRoundtrip täcker Task 6:s nya order_rows-fält (rå
// kravtext + artikeldata från Monitor, inkl. hyperlinks-listan) i detalj: ett
// satt-fall (alla fält ifyllda) och ett tomt-hyperlinks-fall (tom lista ska
// lagras som ” i DB och läsas tillbaka som tom lista, inte nil).
func TestOrderRowCertFieldsRoundtrip(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	links := []domain.Hyperlink{
		{Link: "https://example.com/ritning.pdf", Description: "Ritning A"},
		{Link: `\\fileserver\share\ritning-b.pdf`, Description: "Ritning B (UNC)"},
	}
	row := &domain.OrderRow{
		DeliveryRowID: 50, PurchaseOrderID: 8, OrderNumber: "B128293",
		PartID: 22, PartNumber: "30-101241-002",
		ReceivingMessage: "godsmeddelande", ReceivingInspectionInstruction: "mottagningskontroll",
		RowGoodsLabel: "radgodsmärke", RowNotes: "radnotering",
		SupplierDrawingNumber: "SUP-D1", SupplierRevisionNumber: "A", FreeText: "fritext",
		OrderGoodsLabel: "ordergodsmärke", ExternalComment: "extern kommentar",
		BusinessContactOrderNumber: "LEV-9",
		AlloyCode:                  "S690QL", AlloyDescription: "Höghållfast stål",
		PartReceivingInstruction: "mottagningsinstruktion", PartPurchaseComment: "inköpskommentar",
		PartComment: "artikelkommentar",
		PartLength:  6, PartWidth: 2, PartHeight: 0.06, WeightPerUnit: 850,
		GoodsType: "Plåt", CategoryString: "Stål",
		ExtraFieldsRaw: `[{"Type":1}]`,
		Hyperlinks:     links,
		DrawingNumbers: "D-100, D-101",
	}
	if err := repo.UpsertOrderRow(ctx, row, "2026-07-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetOrderRow(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReceivingMessage != row.ReceivingMessage || got.ReceivingInspectionInstruction != row.ReceivingInspectionInstruction {
		t.Errorf("radkommentarer tappade: %+v", got)
	}
	if got.RowGoodsLabel != row.RowGoodsLabel || got.RowNotes != row.RowNotes ||
		got.SupplierDrawingNumber != row.SupplierDrawingNumber || got.SupplierRevisionNumber != row.SupplierRevisionNumber ||
		got.FreeText != row.FreeText {
		t.Errorf("radfält tappade: %+v", got)
	}
	if got.OrderGoodsLabel != row.OrderGoodsLabel || got.ExternalComment != row.ExternalComment ||
		got.BusinessContactOrderNumber != row.BusinessContactOrderNumber {
		t.Errorf("orderfält tappade: %+v", got)
	}
	if got.AlloyCode != row.AlloyCode || got.AlloyDescription != row.AlloyDescription {
		t.Errorf("legering tappad: %+v", got)
	}
	if got.PartReceivingInstruction != row.PartReceivingInstruction || got.PartPurchaseComment != row.PartPurchaseComment ||
		got.PartComment != row.PartComment {
		t.Errorf("artikelkommentarer tappade: %+v", got)
	}
	if got.PartLength != row.PartLength || got.PartWidth != row.PartWidth || got.PartHeight != row.PartHeight ||
		got.WeightPerUnit != row.WeightPerUnit {
		t.Errorf("dimensioner tappade: %+v", got)
	}
	if got.GoodsType != row.GoodsType || got.CategoryString != row.CategoryString {
		t.Errorf("godsslag/kategori tappade: %+v", got)
	}
	if got.ExtraFieldsRaw != row.ExtraFieldsRaw {
		t.Errorf("ExtraFieldsRaw = %q, vill ha %q", got.ExtraFieldsRaw, row.ExtraFieldsRaw)
	}
	if !reflect.DeepEqual(got.Hyperlinks, links) {
		t.Errorf("Hyperlinks roundtrip = %+v, vill ha %+v", got.Hyperlinks, links)
	}
	if got.DrawingNumbers != row.DrawingNumbers {
		t.Errorf("DrawingNumbers = %q, vill ha %q", got.DrawingNumbers, row.DrawingNumbers)
	}

	// Tom hyperlinks-lista: lagras som '' i DB, läses tillbaka som tom (icke-nil) lista.
	empty := &domain.OrderRow{DeliveryRowID: 51, OrderNumber: "B1", Hyperlinks: []domain.Hyperlink{}}
	if err := repo.UpsertOrderRow(ctx, empty, "t"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := repo.sqldb.QueryRow(`SELECT hyperlinks FROM order_rows WHERE delivery_row_id = ?`, 51).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Errorf("tom hyperlinks-lista ska lagras som '', fick %q", raw)
	}
	gotEmpty, err := repo.GetOrderRow(ctx, 51)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotEmpty.Hyperlinks) != 0 {
		t.Errorf("tom hyperlinks-lista ska läsas tillbaka som tom, fick %+v", gotEmpty.Hyperlinks)
	}
}

func TestLinksAndConfirmedOrderNumbers(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	certID, err := repo.InsertCert(ctx, &domain.Cert{
		PdfHash: "h1", OriginalFilename: "x.pdf", StoredName: "h1__x.pdf", ReceivedAt: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	mk := func(rowID int64, orderNum string, status domain.LinkStatus) *domain.Link {
		l := &domain.Link{CertID: certID, DeliveryRowID: rowID, OrderNumber: orderNum,
			Status: status, MatchSource: "test", CreatedAt: "t", UpdatedAt: "t"}
		if _, err := repo.InsertLink(ctx, l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	mk(1, "B222222", domain.LinkBekraftad)
	mk(2, "B111111", domain.LinkBekraftad)
	mk(3, "B333333", domain.LinkForeslagen) // ej bekräftad — ska inte med
	rejected := mk(4, "B444444", domain.LinkBekraftad)
	if err := repo.UpdateLinkStatus(ctx, rejected.ID, domain.LinkAvfardad, "test", "t2"); err != nil {
		t.Fatal(err)
	}

	confirmed, err := repo.ConfirmedOrderNumbers(ctx, certID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(confirmed, []string{"B111111", "B222222"}) {
		t.Errorf("ConfirmedOrderNumbers = %v", confirmed)
	}

	// Nyckeluppslag + verdict-skrivning
	l, err := repo.GetLinkByKey(ctx, certID, 3, "B333333")
	if err != nil {
		t.Fatal(err)
	}
	l.MaterialOK = "mismatch"
	l.RequiredMaterial = "S690QL"
	l.UpdatedAt = "t3"
	if err := repo.UpdateLinkVerdict(ctx, l); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetLink(ctx, l.ID)
	if got.MaterialOK != "mismatch" || got.RequiredMaterial != "S690QL" {
		t.Errorf("verdict tappad: %+v", got)
	}
}

func TestStoredName(t *testing.T) {
	// filepath.Base körs före sanering: allt före "/" faller bort.
	name := StoredName("abcdef0123456789", `heat/43136: "final".pdf`)
	if name != `abcdef012345__43136_ _final_.pdf` {
		t.Errorf("StoredName = %q", name)
	}
	if got := StoredName("ab", ""); got != "ab__cert.pdf" {
		t.Errorf("tomt originalnamn: %q", got)
	}
}
