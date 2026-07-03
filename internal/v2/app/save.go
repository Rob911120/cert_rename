package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"cert-renamer/internal/cert"
	v1store "cert-renamer/internal/store"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// SaveResult är Spara-aktens utfall.
type SaveResult struct {
	FinalFilename string   `json:"final_filename"`
	OutputPath    string   `json:"output_path"`
	Warnings      []string `json:"warnings,omitempty"`
	AlreadySaved  bool     `json:"already_saved,omitempty"`
}

// SaveCert är den terminala akten: räkna slutnamnet från effektiv data, skriv
// den omdöpta kopian till utmappen med inbäddad metadata och frys certet.
//
// Ordningen är crash-säker (arkitekturregel 4): utfil + inbäddning FÖRST,
// DB-commit SIST. Kraschar processen däremellan detekterar nästa anrop den
// redan skrivna utfilen via hash/metadata och återanvänder den — aldrig
// _2-dubbletter. Anrop på ett redan sparat cert är en idempotent no-op.
//
// Valideringsfel (cert.Validate) är VARNINGAR: utan confirm returneras de som
// domain.ValidationWarnings (→ 422) så Rob kan välja "Spara ändå".
func (a *App) SaveCert(ctx context.Context, certID int64, confirm bool) (*SaveResult, error) {
	c, err := a.Repo.GetCert(ctx, certID)
	if err != nil {
		return nil, err
	}
	if c.Status == domain.CertSparad {
		return &SaveResult{FinalFilename: c.FinalFilename, OutputPath: c.OutputPath, AlreadySaved: true}, nil
	}
	if !domain.CertCanTransition(c.Status, domain.CertSparad) {
		return nil, domain.ErrTransition
	}

	confirmed, err := a.Repo.ConfirmedOrderNumbers(ctx, certID)
	if err != nil {
		return nil, err
	}
	name := domain.ProposedFilename(c, confirmed)
	bNums := domain.NameBNumbers(c, confirmed)

	if warnings := cert.Validate(c.EffectiveExtraction(), bNums); len(warnings) > 0 && !confirm {
		return &SaveResult{FinalFilename: name, Warnings: warnings}, domain.ValidationWarnings(warnings)
	}

	cfg := a.Config()
	data, err := os.ReadFile(store.StorePath(cfg, c.StoredName))
	if err != nil {
		return nil, fmt.Errorf("läsa certlagerfil: %w", err)
	}

	outDir := store.OutputDir(cfg)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, err
	}
	outPath, err := a.placeOutputFile(outDir, name, data, c.PdfHash)
	if err != nil {
		return nil, err
	}

	// Inbäddning vid spar — enda gången metadata skrivs in i en PDF. Fel är
	// inte fatala: EmbedMetadata faller själv tillbaka på sidecar-JSON, och
	// DB förblir sanningen.
	if err := v1store.EmbedMetadata(outPath, a.buildMeta(c, bNums)); err != nil {
		a.Notify.Logf("⚠️  metadata-inbäddning misslyckades för %s: %v", name, err)
	}

	finalName := filepath.Base(outPath)
	savedAt := a.ts()
	err = a.Repo.Tx(ctx, func(q *store.Q) error {
		// Läs om under transaktionen: en parallell Spara kan ha hunnit före.
		fresh, err := q.GetCert(ctx, certID)
		if err != nil {
			return err
		}
		if fresh.Status == domain.CertSparad {
			return nil // andra anropet vann; utfilen är densamma (hash-återanvändning)
		}
		if !domain.CertCanTransition(fresh.Status, domain.CertSparad) {
			return domain.ErrTransition
		}
		return q.UpdateCertSaved(ctx, certID, finalName, outPath, savedAt)
	})
	if err != nil {
		return nil, err
	}

	a.Notify.Logf("💾 cert %d sparat som %s", certID, finalName)
	a.Notify.OverviewChanged()
	return &SaveResult{FinalFilename: finalName, OutputPath: outPath}, nil
}

// placeOutputFile lägger certbytes i utmappen under önskat namn.
// Kraschåterhämtning: finns målet redan och tillhör SAMMA cert (via inbäddad
// metadata-hash eller rå byteshash) återanvänds det; annars får
// WriteUniqueFile lösa äkta namnkollisioner med _2-suffix.
func (a *App) placeOutputFile(outDir, name string, data []byte, pdfHash string) (string, error) {
	target := filepath.Join(outDir, name)
	if _, err := os.Stat(target); err == nil {
		if m, ok := v1store.ReadMetadata(target); ok && m.Hash == pdfHash {
			return target, nil // färdig utfil från tidigare (avbrutet) spar
		}
		if b, err := os.ReadFile(target); err == nil && store.HashPDF(b) == pdfHash {
			return target, nil // kopierad men aldrig inbäddad — återanvänd och bädda om
		}
		// Annat cert med samma namn — äkta kollision, låt suffixen lösa det.
	}
	return v1store.WriteUniqueFile(outDir, name, data)
}

// buildMeta bygger PdfMeta av SLUTLIG effektiv data. Schema 6 = V2-sparad.
func (a *App) buildMeta(c *domain.Cert, bNums []string) v1store.PdfMeta {
	ext := c.EffectiveExtraction()
	return v1store.PdfMeta{
		Charge:            ext.Charge,
		Material:          ext.Material,
		EnStandardPresent: ext.EnStandardPresent,
		IsEnglish:         ext.IsEnglish,
		ProductForm:       ext.ProductForm,
		Dimensions:        ext.Dimensions,
		CountryOfOrigin:   ext.CountryOfOrigin,
		BNumbers:          bNums,
		Confidence:        c.Confidence,
		Issues:            c.Issues,
		EmailSubject:      c.EmailSubject,
		EmailFrom:         c.EmailFrom,
		EmailDate:         c.EmailDate,
		ModelUsed:         c.ModelUsed,
		TokensInput:       c.TokensInput,
		TokensOutput:      c.TokensOutput,
		ProcessingMs:      c.ProcessingMS,
		OriginalFilename:  c.OriginalFilename,
		ExtractedAt:       c.ReceivedAt,
		Hash:              c.PdfHash,
		Schema:            6,
		Status:            "saved",
	}
}
