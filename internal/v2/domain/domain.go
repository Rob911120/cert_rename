// Package domain är V2:s rena kärna: typer, tillståndsmaskin, effective-values
// och namnbygge. Noll IO-beroenden — allt här är tabelltestbart utan databas
// eller API-nyckel. Statussträngar jämförs ALDRIG utanför detta paket; all
// övergångslogik går genom CertCanTransition/LinkCanTransition.
package domain

import (
	"strings"

	"cert-renamer/internal/cert"
)

// ---------------------------------------------------------------------------
// Statusar + tillståndsmaskin
// ---------------------------------------------------------------------------

type CertStatus string

const (
	CertMottagen  CertStatus = "mottagen"  // levande: allt utom rå extraktion är muterbart
	CertSparad    CertStatus = "sparad"    // terminal: fryst, alla mutatorer avvisar
	CertArkiverad CertStatus = "arkiverad" // ej-cert/irrelevant; kan ångras
)

type LinkStatus string

const (
	LinkForeslagen LinkStatus = "foreslagen" // auto-förslag från intag/sync
	LinkBekraftad  LinkStatus = "bekraftad"  // Rob/Sickan har bekräftat kopplingen
	LinkAvfardad   LinkStatus = "avfardad"   // avvisad; kan återupplivas till bekräftad
)

// CertCanTransition är den ENDA definitionen av tillåtna certövergångar.
func CertCanTransition(from, to CertStatus) bool {
	switch from {
	case CertMottagen:
		return to == CertSparad || to == CertArkiverad
	case CertArkiverad:
		return to == CertMottagen // ångra arkivering
	default: // CertSparad är terminal
		return false
	}
}

// LinkCanTransition är den ENDA definitionen av tillåtna länkövergångar.
// avfardad→bekraftad tillåts så att ett felaktigt avvisat förslag kan
// återupplivas manuellt utan att skapa dubblettlänkar.
func LinkCanTransition(from, to LinkStatus) bool {
	switch from {
	case LinkForeslagen:
		return to == LinkBekraftad || to == LinkAvfardad
	case LinkBekraftad:
		return to == LinkAvfardad
	case LinkAvfardad:
		return to == LinkBekraftad
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Typer (speglar DB-raderna; store-lagret sköter (av)serialisering)
// ---------------------------------------------------------------------------

// Correction är en post i certets append-only rättelselogg.
type Correction struct {
	TS    string `json:"ts"`
	Who   string `json:"who"` // "rob" | "sickan"
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// Cert är ett mottaget certifikat. Råfälten (från AI-extraktionen) skrivs
// aldrig över; rättelser bor i Corrected* ('' = ej rättad) och det effektiva
// värdet läses via Effective*-metoderna.
type Cert struct {
	ID               int64
	PdfHash          string
	OriginalFilename string // visas som heat/charge-förhandsvisning i UI
	StoredName       string // stabilt namn i certlagret; döps aldrig om

	EmailSubject string
	EmailFrom    string
	EmailDate    string

	// Rå extraktion
	CertType          string
	Charge            string
	Material          string
	EnStandardPresent bool
	IsEnglish         bool
	ProductForm       string
	Dimensions        string
	CountryOfOrigin   string
	BNumbers          []string
	Confidence        string
	Issues            []string
	ModelUsed         string
	TokensInput       int64
	TokensOutput      int64
	ProcessingMS      int64

	// Kolumnkalibrerad extraktion (Task 1): slag-, kemi- och märkningsdata.
	// Nullable-tal är *float64 (nil = ej angivet/ej tillämpligt i certet).
	// Ingen Corrected*-variant ännu — bara rå extraktion.
	IsLegible            bool
	IsUnaltered          bool
	NormSystem           string
	ImpactTempC          *float64
	ImpactEnergyJ        float64
	NormEdition          string
	PedDirective         string
	Cev                  *float64
	CarbonPct            *float64
	PPct                 *float64
	SPct                 *float64
	HasBendTest          bool
	HasIntergranularTest bool
	HasStampPhoto        bool
	MinTemperatureC      *float64
	DeliveryCondition    string

	// Rättelser (effective-value-mönstret)
	CorrectedCharge      string
	CorrectedMaterial    string
	CorrectedProductForm string
	CorrectedDimensions  string
	CorrectedCertType    string
	CorrectedBNumbers    []string // nil = ej rättad; tom slice = rättad till "inga"
	CorrectionLog        []Correction

	// Levande namn
	NameOverride string // '' = beräkna via ProposedFilename

	// Livscykel
	Status        CertStatus
	FinalFilename string
	OutputPath    string
	SavedAt       string
	ReceivedAt    string
}

// Living rapporterar om certet fortfarande är redigerbart.
func (c *Cert) Living() bool { return c.Status == CertMottagen }

// OrderRow är en inköpsorderrad från Monitor + artikeldata, persisterad lokalt.
type OrderRow struct {
	DeliveryRowID    int64
	PurchaseOrderID  int64
	OrderNumber      string // B-numret
	SupplierName     string
	PartID           int64
	PartNumber       string
	Description      string
	ExtraDescription string // RÅ "extra benämning"
	PlannedQty       float64
	DeliveryDate     string
	CertRequired     bool
	DeliveryRaw      string
	PartRaw          string
	Delivered        bool
	InMonitor        bool
	FirstSeen        string
	LastSeen         string
}

// Link är arbetsläget för ett cert↔orderrad-par. DeliveryRowID kan vara 0 när
// Rob kopplat ett B-nummer som (ännu) inte finns i Monitor-fönstret — då bär
// OrderNumber kopplingen tills raden dyker upp.
type Link struct {
	ID            int64
	CertID        int64
	DeliveryRowID int64
	OrderNumber   string
	Status        LinkStatus
	MatchSource   string // auto_b_number | auto_charge_part | manual | sickan

	// AI-parbedömning (cachad via ai_match_cache)
	RequiredMaterial    string
	RequiredCert        string
	OurMaterial         string
	MaterialOK          string // ok | mismatch | unknown
	RequiredProductForm string
	ProductFormOK       string
	AINotes             string

	CreatedAt string
	UpdatedAt string
}

// ---------------------------------------------------------------------------
// Effective values
// ---------------------------------------------------------------------------

func effective(corrected, raw string) string {
	if corrected != "" {
		return corrected
	}
	return raw
}

func (c *Cert) EffectiveCharge() string      { return effective(c.CorrectedCharge, c.Charge) }
func (c *Cert) EffectiveMaterial() string    { return effective(c.CorrectedMaterial, c.Material) }
func (c *Cert) EffectiveProductForm() string { return effective(c.CorrectedProductForm, c.ProductForm) }
func (c *Cert) EffectiveDimensions() string  { return effective(c.CorrectedDimensions, c.Dimensions) }
func (c *Cert) EffectiveCertType() string    { return effective(c.CorrectedCertType, c.CertType) }

// EffectiveBNumbers: rättade B-nummer om satta, annars råa från extraktionen.
// Länk-härledda B-nummer hanteras i ProposedFilename (kräver länkkontext).
func (c *Cert) EffectiveBNumbers() []string {
	if c.CorrectedBNumbers != nil {
		return c.CorrectedBNumbers
	}
	return c.BNumbers
}

// EffectiveExtraction bygger en V1 cert.Extraction av effektiva fält — bron
// till cert.BuildFilename och cert.Validate (Spara-primitiverna).
func (c *Cert) EffectiveExtraction() *cert.Extraction {
	certType := c.EffectiveCertType()
	return &cert.Extraction{
		IsEN10204_3_1:     strings.Contains(certType, "3.1"),
		CertType:          certType,
		Charge:            c.EffectiveCharge(),
		Material:          c.EffectiveMaterial(),
		EnStandardPresent: c.EnStandardPresent,
		IsEnglish:         c.IsEnglish,
		ProductForm:       c.EffectiveProductForm(),
		Dimensions:        c.EffectiveDimensions(),
		CountryOfOrigin:   c.CountryOfOrigin,
		Confidence:        c.Confidence,
		Issues:            c.Issues,
	}
}
