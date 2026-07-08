package app

import (
	"context"
	"encoding/json"
	"os"

	"cert-renamer/internal/v2/cert"
	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

// IngestInput är allt intaget vet om en extraherad PDF-bilaga.
type IngestInput struct {
	OriginalFilename string
	Data             []byte
	EmailSubject     string
	EmailFrom        string
	EmailDate        string
	EmailBody        string // bara till sidecar (katastrofskydd), aldrig DB
	Extraction       *cert.Extraction
	BNumbers         []string
	Model            string
	TokensIn         int64
	TokensOut        int64
	ProcessingMS     int64

	// HashOverride används BARA av V1-importen: V1 bäddade in metadata i
	// PDF:erna, så filbytes ≠ ursprungliga bilagebytes. Dedupe-nyckeln måste
	// vara den URSPRUNGLIGA hashen (PdfMeta.Hash) för att framtida ominläsning
	// av samma mail ska träffa rätt. Tom = hasha Data.
	HashOverride string
}

// IngestCert tar emot ett extraherat cert: skriver PDF:en EN gång till
// certlagret under stabilt namn (döps aldrig om), skriver sidecar-JSON som
// katastrofskydd och lägger den levande DB-raden. Ingen valideringsgrind —
// ofullständiga cert blir levande rader med luckorna synliga (validering sker
// vid Spara). Dedupe på pdf_hash: samma PDF två gånger är en no-op.
//
// Crash-säker ordning: fil först, DB sist. Kraschar processen däremellan
// återanvänds lagerfilen vid nästa försök (hash-jämförelse) i stället för att
// dubbleras.
func (a *App) IngestCert(ctx context.Context, in IngestInput) (*domain.Cert, bool, error) {
	hash := in.HashOverride
	if hash == "" {
		hash = store.HashPDF(in.Data)
	}
	if existing, err := a.Repo.GetCertByHash(ctx, hash); err == nil {
		return existing, true, nil
	} else if err != domain.ErrNotFound {
		return nil, false, err
	}

	cfg := a.Config()
	storedName := store.StoredName(hash, in.OriginalFilename)
	storePath := store.StorePath(cfg, storedName)
	reuse := false
	if b, err := os.ReadFile(storePath); err == nil {
		if store.HashPDF(b) == hash {
			reuse = true // lagerfil från tidigare avbrutet intag — återanvänd
		} else if m, ok := store.ReadMetadata(storePath); ok && m.Hash == hash {
			// V1-import (HashOverride): filbytes ≠ originalhash eftersom V1
			// bäddade in metadata — men metadatan bär originalhashen. Utan
			// den här vägen dubbleras lagerfilen (_2) vid varje avbruten
			// importomkörning.
			reuse = true
		}
	}
	if !reuse {
		p, err := store.WriteStoreFile(cfg, storedName, in.Data)
		if err != nil {
			return nil, false, err
		}
		storePath = p
		storedName = lastPathSegment(p)
	}

	// Sidecar: rå extraktion + mailkontext bredvid lagerfilen. Ingen
	// pdfcpu-inbäddning i hot path — DB är sanningen.
	ext := in.Extraction
	sidecar := store.PdfMeta{
		Charge: ext.Charge, Material: ext.Material,
		EnStandardPresent: ext.EnStandardPresent, IsEnglish: ext.IsEnglish,
		ProductForm: ext.ProductForm, Dimensions: ext.Dimensions,
		CountryOfOrigin: ext.CountryOfOrigin, BNumbers: in.BNumbers,
		Confidence: ext.Confidence, Issues: ext.Issues,
		EmailSubject: in.EmailSubject, EmailFrom: in.EmailFrom,
		EmailDate: in.EmailDate, EmailBody: in.EmailBody,
		ModelUsed: in.Model, TokensInput: in.TokensIn, TokensOutput: in.TokensOut,
		ProcessingMs: in.ProcessingMS, OriginalFilename: in.OriginalFilename,
		ExtractedAt: a.ts(), Hash: hash, Schema: 6, Status: "received",

		// Kolumnkalibrerad extraktion (Task 1+2) — sidecaren listar fälten
		// explicit (ingen helstruct-serialisering), så de nya fälten hänger
		// med hit också. Katastrofskydd bara: DB-raden nedan är sanningen.
		IsLegible: ext.IsLegible, IsUnaltered: ext.IsUnaltered,
		NormSystem: ext.NormSystem, ImpactTempC: ext.ImpactTempC, ImpactEnergyJ: ext.ImpactEnergyJ,
		NormEdition: ext.NormEdition, PedDirective: ext.PedDirective,
		Cev: ext.Cev, CarbonPct: ext.CarbonPct, PPct: ext.PPct, SPct: ext.SPct,
		HasBendTest: ext.HasBendTest, HasIntergranularTest: ext.HasIntergranularTest,
		HasStampPhoto: ext.HasStampPhoto, MinTemperatureC: ext.MinTemperatureC,
		DeliveryCondition: ext.DeliveryCondition,
	}
	writeSidecar(storePath, sidecar)

	c := &domain.Cert{
		PdfHash:           hash,
		OriginalFilename:  in.OriginalFilename,
		StoredName:        storedName,
		EmailSubject:      in.EmailSubject,
		EmailFrom:         in.EmailFrom,
		EmailDate:         in.EmailDate,
		CertType:          ext.CertType,
		Charge:            ext.Charge,
		Material:          ext.Material,
		EnStandardPresent: ext.EnStandardPresent,
		IsEnglish:         ext.IsEnglish,
		ProductForm:       ext.ProductForm,
		Dimensions:        ext.Dimensions,
		CountryOfOrigin:   ext.CountryOfOrigin,
		BNumbers:          in.BNumbers,
		Confidence:        ext.Confidence,
		Issues:            ext.Issues,
		ModelUsed:         in.Model,
		TokensInput:       in.TokensIn,
		TokensOutput:      in.TokensOut,
		ProcessingMS:      in.ProcessingMS,

		// Kolumnkalibrerad extraktion (Task 1+2): pekare kopieras som pekare —
		// nil ("ej angivet på certet") förblir nil hela vägen till DB-raden.
		IsLegible:            ext.IsLegible,
		IsUnaltered:          ext.IsUnaltered,
		NormSystem:           ext.NormSystem,
		ImpactTempC:          ext.ImpactTempC,
		ImpactEnergyJ:        ext.ImpactEnergyJ,
		NormEdition:          ext.NormEdition,
		PedDirective:         ext.PedDirective,
		Cev:                  ext.Cev,
		CarbonPct:            ext.CarbonPct,
		PPct:                 ext.PPct,
		SPct:                 ext.SPct,
		HasBendTest:          ext.HasBendTest,
		HasIntergranularTest: ext.HasIntergranularTest,
		HasStampPhoto:        ext.HasStampPhoto,
		MinTemperatureC:      ext.MinTemperatureC,
		DeliveryCondition:    ext.DeliveryCondition,

		Status:     domain.CertMottagen,
		ReceivedAt: a.ts(),
	}
	if _, err := a.Repo.InsertCert(ctx, c); err != nil {
		// Samtidig ingest av samma PDF kan ha hunnit före mellan dedupe-checken
		// ovan och insert:en (UNIQUE på pdf_hash) — behandla som dublett i
		// stället för att bubbla ett rått SQLite-fel.
		if existing, gerr := a.Repo.GetCertByHash(ctx, hash); gerr == nil {
			return existing, true, nil
		}
		return nil, false, err
	}
	a.Notify.Logf("📥 cert mottaget: %s (charge %s, %s)", in.OriginalFilename, ext.Charge, ext.Material)
	a.Notify.OverviewChanged()
	return c, false, nil
}

// writeSidecar skriver katastrofskydds-JSON bredvid lagerfilen (best effort).
func writeSidecar(pdfPath string, meta store.PdfMeta) {
	data, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(store.MetaSidecarPath(pdfPath), data, 0644)
}

// SuggestLinksByBNumber kör förslagspasset för ett cert: varje effektivt
// B-nummer matchas mot kända orderrader → foreslagen-länkar. Rör aldrig
// befintliga länkar (SuggestLink är no-op på beslut). Används av intaget och
// B-nummer-rättningar; Monitor-syncen har en egen variant med charge-förfining
// (monitorsync/match.go suggestForCert) som också går via SuggestLink.
func (a *App) SuggestLinksByBNumber(ctx context.Context, certID int64) (int, error) {
	c, err := a.Repo.GetCert(ctx, certID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, bn := range c.EffectiveBNumbers() {
		rows, err := a.Repo.RowsByOrderNumber(ctx, normalizeOrderNumber(bn))
		if err != nil {
			return created, err
		}
		for _, r := range rows {
			l, err := a.SuggestLink(ctx, certID, r.DeliveryRowID, r.OrderNumber, "auto_b_number")
			if err != nil {
				return created, err
			}
			if l != nil {
				created++
			}
		}
	}
	return created, nil
}

// ---------------------------------------------------------------------------
// Intags-audit (emails) + AI-kostnadslogg — tunna genomstick så att även
// bokföring går genom den enda skrivvägen.
// ---------------------------------------------------------------------------

func (a *App) EmailStarted(ctx context.Context, filename, fileHash string) int64 {
	id, err := a.Repo.InsertEmail(ctx, filename, "", "", "", "", "processing", "", fileHash, a.ts())
	if err != nil {
		a.Notify.Logf("⚠️  DB (email-rad): %v", err)
		return 0
	}
	return id
}

func (a *App) EmailFinished(ctx context.Context, id int64, status, errMsg string) {
	if id <= 0 {
		return
	}
	if err := a.Repo.UpdateEmailStatus(ctx, id, status, errMsg); err != nil {
		a.Notify.Logf("⚠️  DB (email-status): %v", err)
	}
}

// AckEmailErrors kvitterar alla nuvarande intagsfel (✕ på UI-bannern): de
// göms ur overviewn men filerna ligger kvar i inkorgen och skippas
// fortfarande av intaget. Nya fel blir nya rader och syns alltid.
func (a *App) AckEmailErrors(ctx context.Context) error {
	if err := a.Repo.AckEmailErrors(ctx); err != nil {
		return err
	}
	a.Notify.OverviewChanged()
	return nil
}

// LatestEmailStatus används av intaget för att hoppa över .eml-filer som
// redan slutat i fel (de ligger kvar i inkorgen tills Rob agerar) — utan
// mapp-koreografi och utan att bränna AI-anrop i loop. Nyckeln är filnamn +
// innehålls-hash så en NY fil med återanvänt namn inte skippas.
func (a *App) LatestEmailStatus(ctx context.Context, filename, fileHash string) string {
	s, err := a.Repo.LatestEmailStatus(ctx, filename, fileHash)
	if err != nil {
		return ""
	}
	return s
}

func (a *App) RecordAICall(ctx context.Context, certID int64, step, model string,
	tokensIn, tokensOut, durationMS int64, success bool, errMsg string) {
	if err := a.Repo.InsertAICall(ctx, certID, step, model, tokensIn, tokensOut, 0, 0,
		durationMS, success, errMsg, a.ts()); err != nil {
		a.Notify.Logf("⚠️  DB (ai_calls): %v", err)
	}
}

func lastPathSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
