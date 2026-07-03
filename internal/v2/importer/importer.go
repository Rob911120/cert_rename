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

	"cert-renamer/internal/cert"
	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
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
func ImportV1(ctx context.Context, a *app.App, cfg v1store.Config) (Stats, error) {
	var st Stats
	if cfg.InboxDir == "" {
		return st, fmt.Errorf("ingen inbox konfigurerad — V1-mapparna kan inte härledas")
	}
	for _, src := range []struct {
		dir   string
		saved bool
	}{
		{v1store.ApprovedDir(cfg), true},
		{v1store.QueueDir(cfg), false},
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
	meta, ok := v1store.ReadMetadata(path)
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
	in := app.IngestInput{
		OriginalFilename: original,
		Data:             data,
		EmailSubject:     meta.EmailSubject,
		EmailFrom:        meta.EmailFrom,
		EmailDate:        meta.EmailDate,
		EmailBody:        meta.EmailBody,
		Extraction: &cert.Extraction{
			IsEN10204_3_1:     true, // V1 släppte bara igenom validerade 3.1-cert
			CertType:          "3.1",
			Charge:            meta.Charge,
			Material:          meta.Material,
			EnStandardPresent: meta.EnStandardPresent,
			IsEnglish:         meta.IsEnglish,
			ProductForm:       meta.ProductForm,
			Dimensions:        meta.Dimensions,
			CountryOfOrigin:   meta.CountryOfOrigin,
			Confidence:        meta.Confidence,
			Issues:            meta.Issues,
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
