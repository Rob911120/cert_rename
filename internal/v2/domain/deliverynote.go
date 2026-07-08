package domain

// ---------------------------------------------------------------------------
// Följesedel (delivery note) — egen entitet, egen minimal tillståndsmaskin.
//
// En följesedel är en fotad papperssedel (mejlad till sig själv) vars fält
// läses av med vision. Livscykeln är avsiktligt platt: mottagen → inlevererad
// (registrerad i Monitor) eller avfardad (irrelevant, ångra-bar). Matchning mot
// en Monitor-order är INTE ett skede utan ett nullbart datafält (MatchedPOID/
// MatchedRowID, 0 = ej matchad) — "matchad" = "har fått en order", inget mer att
// vakta. Certcykeln (mottagen→sparad) är cert-specifik (frys, filnamn) och delas
// medvetet inte.
// ---------------------------------------------------------------------------

type DeliveryNoteStatus string

const (
	DNMottagen    DeliveryNoteStatus = "mottagen"    // levande: fält muterbara, väntar på inleverans
	DNInlevererad DeliveryNoteStatus = "inlevererad" // terminal: registrerad i Monitor
	DNAvfardad    DeliveryNoteStatus = "avfardad"    // irrelevant; kan återupplivas
)

// DeliveryNoteCanTransition är den ENDA definitionen av tillåtna
// följesedel-övergångar (jfr CertCanTransition).
func DeliveryNoteCanTransition(from, to DeliveryNoteStatus) bool {
	switch from {
	case DNMottagen:
		return to == DNInlevererad || to == DNAvfardad
	case DNAvfardad:
		return to == DNMottagen // ångra avfärdande
	default: // DNInlevererad är terminal
		return false
	}
}

// DeliveryNote är en mottagen följesedel: vision-extraherade fält + ett nullbart
// datafält för Monitor-matchningen + livscykel. Speglar DB-raden; store-lagret
// sköter (av)serialisering (BNumbers som JSON-text, status som sträng).
type DeliveryNote struct {
	ID            int64
	ImageHash     string // sha256-hex över bildbytes — dedupe-nyckeln
	ImageFilename string // stabilt namn i följesedel-lagret; döps aldrig om

	// Vision-extraktion (från ai.DeliveryNoteExtraction)
	Supplier           string
	DeliveryDate       string
	OrderNumber        string
	Charge             string
	Material           string
	Quantity           float64
	Unit               string
	DeliveryNoteNumber string
	WaybillNumber      string
	BNumbers           []string
	Confidence         string

	// Matchning mot Monitor (nullbara datafält, 0 = ej matchad)
	MatchedPOID      int64
	MatchedRowID     int64
	ProposedQuantity float64

	// Livscykel
	Status    DeliveryNoteStatus
	CreatedAt string
}

// Matched rapporterar om följesedeln kopplats till en konkret orderrad.
func (d *DeliveryNote) Matched() bool { return d.MatchedRowID != 0 }

// Living rapporterar om följesedeln fortfarande väntar på åtgärd.
func (d *DeliveryNote) Living() bool { return d.Status == DNMottagen }
