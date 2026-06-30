package store

import (
	"encoding/json"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// PdfMeta är de fält vi bäddar in i PDF-metadatan ("CertRenamer"-property).
type PdfMeta struct {
	// Certifikatfält
	Charge            string   `json:"charge"`
	Material          string   `json:"material"`
	EnStandardPresent bool     `json:"en_standard_present"`
	ProductForm       string   `json:"product_form,omitempty"`
	Dimensions        string   `json:"dimensions"`
	CountryOfOrigin   string   `json:"country_of_origin,omitempty"`
	BNumbers          []string `json:"b_numbers"`
	Confidence        string   `json:"confidence"`
	Issues            []string `json:"issues"`

	// Email-kontext (separata fält)
	EmailSubject string `json:"email_subject"`
	EmailFrom    string `json:"email_from"`
	EmailDate    string `json:"email_date"`
	EmailBody    string `json:"email_body"`

	// Per-attachment token-räkning
	ModelUsed    string `json:"model_used"`
	TokensInput  int64  `json:"tokens_input"`
	TokensOutput int64  `json:"tokens_output"`
	ProcessingMs int64  `json:"processing_ms"`

	// Befintliga fält
	OriginalFilename string `json:"original_filename"`
	ExtractedAt      string `json:"extracted_at"`
	Hash             string `json:"hash,omitempty"`
	Schema           int    `json:"schema"`
	Status           string `json:"status,omitempty"`
	Verdict          string `json:"verdict,omitempty"`
}

// MetaSidecarPath returnerar sidecar-JSON-sökvägen för en given PDF.
func MetaSidecarPath(pdfPath string) string { return pdfPath + ".json" }

// EmbedMetadata bäddar in meta i PDF-egenskaperna under nyckeln "CertRenamer".
// Vid pdfcpu-fel (t.ex. "wrong type types.Array" på komplexa SSAB-cert) faller
// vi tillbaka på en sidecar-JSON bredvid PDF:en så att flödet inte stannar.
// Sidecar tas bort vid lyckad embed för att undvika att gammal data ligger kvar.
func EmbedMetadata(pdfPath string, meta PdfMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := api.AddPropertiesFile(pdfPath, "", map[string]string{
		"CertRenamer": string(data),
	}, nil); err != nil {
		return os.WriteFile(MetaSidecarPath(pdfPath), data, 0644)
	}
	_ = os.Remove(MetaSidecarPath(pdfPath))
	return nil
}

// ReadMetadata läser tillbaka metan om den finns. Sidecar prioriteras (skrivs
// efter senaste uppdatering) — om den saknas läser vi PDF-properties.
func ReadMetadata(pdfPath string) (*PdfMeta, bool) {
	if data, err := os.ReadFile(MetaSidecarPath(pdfPath)); err == nil {
		var m PdfMeta
		if json.Unmarshal(data, &m) == nil {
			return &m, true
		}
	}
	f, err := os.Open(pdfPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	props, err := api.Properties(f, nil)
	if err != nil {
		return nil, false
	}
	raw, ok := props["CertRenamer"]
	if !ok {
		return nil, false
	}
	var m PdfMeta
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil, false
	}
	return &m, true
}
