package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// StoreDir är certlagret: stabila, aldrig omdöpta original.
func StoreDir(cfg Config) string {
	if cfg.StoreDirV2 != "" {
		return cfg.StoreDirV2
	}
	return filepath.Join(cfg.InboxDir, "v2", "store")
}

// OutputDir är Spara-utmappen: omdöpta kopior med inbäddad metadata.
func OutputDir(cfg Config) string {
	if cfg.OutputDirV2 != "" {
		return cfg.OutputDirV2
	}
	return filepath.Join(cfg.InboxDir, "v2", "out")
}

// EnsureDirs skapar certlager + utmapp om de saknas.
func EnsureDirs(cfg Config) error {
	for _, d := range []string{StoreDir(cfg), OutputDir(cfg)} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}

// HashPDF är sha256-hex över PDF-bytes — dedupe-nyckeln (certs.pdf_hash).
func HashPDF(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// storedNameSanitizer gör ett originalfilnamn säkert som del av lagernamnet.
var storedNameSanitizer = strings.NewReplacer(
	"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
	`"`, "_", "<", "_", ">", "_", "|", "_",
)

// StoredName bygger certlagrets stabila filnamn: <hash[:12]>__<original>.
// Hash-prefixet garanterar unikhet och dedupe-spårbarhet; originalnamnet
// behålls för grep-barhet på disk (det förhandsvisar ofta charge/heat).
func StoredName(pdfHash, originalFilename string) string {
	base := storedNameSanitizer.Replace(filepath.Base(originalFilename))
	if base == "" || base == "." {
		base = "cert.pdf"
	}
	// Håll lagernamnet hanterligt även för absurt långa mailbilagenamn.
	// Klipp på UTF-8-säker gräns (å/ä/ö får inte delas), och behandla en
	// "ändelse" längre än halva budgeten som icke-ändelse — annars blir
	// byteräkningen negativ och slajsningen panikar.
	const maxBase = 120
	if len(base) > maxBase {
		ext := filepath.Ext(base)
		if len(ext) > maxBase/2 {
			ext = ""
		}
		base = truncateUTF8(strings.TrimSuffix(base, ext), maxBase-len(ext)) + ext
	}
	prefix := pdfHash
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return prefix + "__" + base
}

// truncateUTF8 klipper s till högst max byte utan att dela en UTF-8-sekvens.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// WriteStoreFile skriver PDF:en till certlagret under sitt stabila namn.
// Kollisionssäkert via WriteUniqueFile (hash-prefixet gör i praktiken namnet
// unikt redan). Returnerar den faktiska sökvägen.
func WriteStoreFile(cfg Config, storedName string, data []byte) (string, error) {
	if err := os.MkdirAll(StoreDir(cfg), 0755); err != nil {
		return "", err
	}
	return WriteUniqueFile(StoreDir(cfg), storedName, data)
}

// StorePath är den förväntade sökvägen för ett lagrat cert.
func StorePath(cfg Config, storedName string) string {
	return filepath.Join(StoreDir(cfg), storedName)
}
