// Package importer är den valfria engångs-bootstrappen: läser V1:s approved/
// och queue/-mappar och tar in certen i V2 via deras inbäddade metadata
// (PdfMeta i PDF-properties eller sidecar-JSON). Syftet är matchningshistorik:
// gamla cert ska fortsätta matcha framtida orderrader. V1:s mappar röres inte.
package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/cert"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// Stats är importens utfall.
type Stats struct {
	Imported int // nya rader
	Skipped  int // dubbletter (fanns redan i V2)
	NoMeta   int // PDF utan läsbar metadata — hoppas över
	Errors   int
}

// ImportV1 går igenom V1:s approved/ (→ sparade, frysta) och queue/
// (→ levande) och tar in dem i V2. PDF:erna KOPIERAS till V2:s certlager.
func ImportV1(ctx context.Context, a *app.App, cfg store.Config) (Stats, error) {
	var st Stats
	if cfg.InboxDir == "" {
		return st, fmt.Errorf("ingen inbox konfigurerad — V1-mapparna kan inte härledas")
	}
	for _, src := range []struct {
		dir   string
		saved bool
	}{
		{store.ApprovedDir(cfg), true},
		{store.QueueDir(cfg), false},
	} {
		if err := importDir(ctx, a, src.dir, src.saved, &st); err != nil {
			return st, err
		}
	}
	a.Notify.Logf("📦 V1-import klar: %d importerade, %d dubbletter, %d utan metadata, %d fel",
		st.Imported, st.Skipped, st.NoMeta, st.Errors)
	return st, nil
}

func importDir(ctx context.Context, a *app.App, dir string, saved bool, st *Stats) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil // mappen finns inte — inget att importera
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			continue
		}
		if err := importOne(ctx, a, filepath.Join(dir, e.Name()), saved, st); err != nil {
			a.Notify.Logf("⚠️  V1-import %s: %v", e.Name(), err)
			st.Errors++
		}
	}
	return nil
}

func importOne(ctx context.Context, a *app.App, path string, saved bool, st *Stats) error {
	meta, ok := store.ReadMetadata(path)
	if !ok {
		st.NoMeta++
		a.Notify.Logf("   ⏭  %s: ingen läsbar metadata — hoppar över", filepath.Base(path))
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	original := meta.OriginalFilename
	if original == "" {
		original = filepath.Base(path)
	}
	// Bara approved/ var validerad av V1 — queue/-cert kan vara vad som helst
	// (2.2, avvisade, ogranskade) och får INTE stämplas som verifierade
	// 3.1-cert. De importeras med tom certtyp ("gissa aldrig"); Rob rättar.
	certType := ""
	if saved {
		certType = "3.1"
	}
	in := app.IngestInput{
		OriginalFilename: original,
		Data:             data,
		EmailSubject:     meta.EmailSubject,
		EmailFrom:        meta.EmailFrom,
		EmailDate:        meta.EmailDate,
		EmailBody:        meta.EmailBody,
		Extraction: &cert.Extraction{
			IsEN10204_3_1:     saved, // V1 validerade bara approved/ som 3.1
			CertType:          certType,
			Charge:            meta.Charge,
			Material:          meta.Material,
			EnStandardPresent: meta.EnStandardPresent,
			IsEnglish:         meta.IsEnglish,
			ProductForm:       meta.ProductForm,
			Dimensions:        meta.Dimensions,
			CountryOfOrigin:   meta.CountryOfOrigin,
			Confidence:        meta.Confidence,
			Issues:            meta.Issues,

			// V1 hade aldrig kolumnkalibrerad extraktion (Task 1+2) — utan
			// dessa defaults skulle gamla, redan godkända cert se ut att ha
			// "problem upptäckt" (oläsligt/ändrat). Övriga nya fält: zero-value
			// (tom sträng/nil/false) = "ej angivet", vilket är rätt för V1-cert.
			IsLegible:   true,
			IsUnaltered: true,
		},
		BNumbers:     meta.BNumbers,
		Model:        meta.ModelUsed,
		TokensIn:     meta.TokensInput,
		TokensOut:    meta.TokensOutput,
		ProcessingMS: meta.ProcessingMs,
		HashOverride: meta.Hash, // ursprunglig bilage-hash — dedupe mot framtida mail
	}
	c, dup, err := a.IngestCert(ctx, in)
	if err != nil {
		return err
	}
	if dup {
		// Reparation vid omkörning: kraschade en tidigare import mellan
		// IngestCert och MarkImportedSaved ligger ett approved-cert kvar som
		// levande — stämpla det nu (idempotent via övergångsguarden).
		if saved && c.Living() {
			if err := a.MarkImportedSaved(ctx, c.ID, filepath.Base(path), path, meta.ExtractedAt); err != nil {
				return fmt.Errorf("reparera sparad-stämpel: %w", err)
			}
			a.Notify.Logf("   🔧 %s: fanns som levande — stämplad som sparad", filepath.Base(path))
		}
		st.Skipped++
		return nil
	}
	if saved {
		savedAt := meta.ExtractedAt
		if err := a.MarkImportedSaved(ctx, c.ID, filepath.Base(path), path, savedAt); err != nil {
			return fmt.Errorf("stämpla som sparad: %w", err)
		}
	} else if _, err := a.SuggestLinksByBNumber(ctx, c.ID); err != nil {
		a.Notify.Logf("   ⚠️  förslagspass %s: %v", filepath.Base(path), err)
	}
	st.Imported++
	a.Notify.Logf("   ✅ %s (%s)", filepath.Base(path), statusLabel(saved))
	return nil
}

func statusLabel(saved bool) domain.CertStatus {
	if saved {
		return domain.CertSparad
	}
	return domain.CertMottagen
}
