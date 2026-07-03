package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

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
