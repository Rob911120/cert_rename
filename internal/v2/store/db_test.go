package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"cert-renamer/internal/v2/domain"

	_ "modernc.org/sqlite"
)

// Test_Migrate_AddsCertExtractionFieldsToExistingDB speglar en verklig
// cert-renamer-v2.db skapad före Task 2: certs-tabellen saknar de nya
// kolumnkalibrerade extraktionsfälten (is_legible m.fl.). migrate() ska lägga
// till dem med neutrala defaults utan att tappa den befintliga raden.
// Bygger det gamla schemat genom att köra migrationslistan MINUS det sista
// steget (Task 2:s ALTER TABLE) — så testet inte tappar synk med db.go.
func Test_Migrate_AddsCertExtractionFieldsToExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	oldVersion := len(migrations) - 1
	for i := 0; i < oldVersion; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("gammalt schema, migration %d: %v", i, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", oldVersion)); err != nil {
		t.Fatalf("stämpla user_version: %v", err)
	}

	// Befintlig rad, skapad före Task 2 — de nya kolumnerna finns inte än.
	if _, err := db.Exec(`INSERT INTO certs (pdf_hash, original_filename, stored_name, received_at)
		VALUES ('gammal-hash', 'gammal.pdf', 'gammal-hash__gammal.pdf', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert gammal rad: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate (andra körningen ska vara no-op): %v", err)
	}

	repo := NewRepository(db)
	got, err := repo.GetCertByHash(context.Background(), "gammal-hash")
	if err != nil {
		t.Fatalf("GetCertByHash efter migration: %v", err)
	}
	if !got.IsLegible || !got.IsUnaltered {
		t.Errorf("is_legible/is_unaltered ska defaulta till true (inget problem upptäckt), fick IsLegible=%v IsUnaltered=%v",
			got.IsLegible, got.IsUnaltered)
	}
	if got.HasBendTest || got.HasIntergranularTest || got.HasStampPhoto {
		t.Errorf("övriga booleans ska defaulta till false: %+v", got)
	}
	if got.ImpactEnergyJ != 0 {
		t.Errorf("impact_energy_j default = %v, vill ha 0", got.ImpactEnergyJ)
	}
	if got.ImpactTempC != nil {
		t.Errorf("impact_temp_c ska defaulta till NULL/nil, fick %v", *got.ImpactTempC)
	}
	if got.Cev != nil || got.CarbonPct != nil || got.PPct != nil || got.SPct != nil || got.MinTemperatureC != nil {
		t.Errorf("nullable-tal ska defaulta till nil: %+v", got)
	}
	if got.NormSystem != "" || got.NormEdition != "" || got.PedDirective != "" || got.DeliveryCondition != "" {
		t.Errorf("strängfält ska defaulta till tomt: %+v", got)
	}
}

// Test_Migrate_AddsOrderRowCertFieldsToExistingDB speglar en verklig
// cert-renamer-v2.db skapad före Task 6: order_rows saknar de nya
// cert-bärande fälten (receiving_message m.fl.) som internal/monitor nu
// hämtar. migrate() ska lägga till dem med neutrala defaults utan att tappa
// den befintliga raden. Samma "bygg gammalt schema minus sista steget"-mönster
// som cert-testet ovan.
func Test_Migrate_AddsOrderRowCertFieldsToExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-rows.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	oldVersion := len(migrations) - 1
	for i := 0; i < oldVersion; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("gammalt schema, migration %d: %v", i, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", oldVersion)); err != nil {
		t.Fatalf("stämpla user_version: %v", err)
	}

	// Befintlig rad, skapad före Task 6 — de nya kolumnerna finns inte än.
	if _, err := db.Exec(`INSERT INTO order_rows
		(delivery_row_id, purchase_order_id, order_number, part_id, first_seen, last_seen)
		VALUES (42, 7, 'B127575', 11, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert gammal rad: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate (andra körningen ska vara no-op): %v", err)
	}

	repo := NewRepository(db)
	got, err := repo.GetOrderRow(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetOrderRow efter migration: %v", err)
	}
	if got.OrderNumber != "B127575" || got.PartID != 11 {
		t.Errorf("befintlig rad tappad: %+v", got)
	}
	if got.ReceivingMessage != "" || got.ReceivingInspectionInstruction != "" || got.RowGoodsLabel != "" ||
		got.RowNotes != "" || got.SupplierDrawingNumber != "" || got.SupplierRevisionNumber != "" ||
		got.FreeText != "" || got.OrderGoodsLabel != "" || got.ExternalComment != "" ||
		got.BusinessContactOrderNumber != "" || got.AlloyCode != "" || got.AlloyDescription != "" ||
		got.PartReceivingInstruction != "" || got.PartPurchaseComment != "" || got.PartComment != "" ||
		got.GoodsType != "" || got.CategoryString != "" || got.ExtraFieldsRaw != "" || got.DrawingNumbers != "" {
		t.Errorf("strängfält ska defaulta till tomt: %+v", got)
	}
	if got.PartLength != 0 || got.PartWidth != 0 || got.PartHeight != 0 || got.WeightPerUnit != 0 {
		t.Errorf("talfält ska defaulta till 0: %+v", got)
	}
	if len(got.Hyperlinks) != 0 {
		t.Errorf("hyperlinks ska defaulta till tom lista: %+v", got.Hyperlinks)
	}
}

// Test_Migrate_AddsOrderRowRequirementFieldsToExistingDB speglar en verklig
// cert-renamer-v2.db skapad före Task 8: order_rows saknar de AI-tolkade
// kravfälten (req_*). migrate() ska lägga till dem med neutrala defaults (tomma
// krav + zero ReqParsedAt) utan att tappa den befintliga raden. Samma "bygg
// gammalt schema minus sista steget"-mönster som testerna ovan.
func Test_Migrate_AddsOrderRowRequirementFieldsToExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-req.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	oldVersion := len(migrations) - 1
	for i := 0; i < oldVersion; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("gammalt schema, migration %d: %v", i, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", oldVersion)); err != nil {
		t.Fatalf("stämpla user_version: %v", err)
	}

	// Befintlig rad, skapad före Task 8 — kravkolumnerna finns inte än.
	if _, err := db.Exec(`INSERT INTO order_rows
		(delivery_row_id, purchase_order_id, order_number, part_id, extra_description, first_seen, last_seen)
		VALUES (42, 7, 'B127575', 11, 'Plåt S355J2+N', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert gammal rad: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate (andra körningen ska vara no-op): %v", err)
	}

	repo := NewRepository(db)
	got, err := repo.GetOrderRow(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetOrderRow efter migration: %v", err)
	}
	if got.OrderNumber != "B127575" || got.ExtraDescription != "Plåt S355J2+N" {
		t.Errorf("befintlig rad tappad: %+v", got)
	}
	if got.Req != (domain.RowRequirements{}) {
		t.Errorf("kraven ska defaulta till tomma: %+v", got.Req)
	}
	if !got.ReqParsedAt.IsZero() {
		t.Errorf("req_parsed_at ska defaulta till zero (aldrig parsad), fick %v", got.ReqParsedAt)
	}

	// Cachetabellen ska finnas och vara tom (miss → ErrNotFound).
	if _, err := repo.GetRequirementsCache(context.Background(), "vadsomhelst"); err != domain.ErrNotFound {
		t.Errorf("tom krav-cache ska ge ErrNotFound, fick %v", err)
	}
}
