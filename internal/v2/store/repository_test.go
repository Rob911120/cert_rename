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
		ProductForm: "plåt", Dimensions: "60", CountryOfOrigin: "Sverige",
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
