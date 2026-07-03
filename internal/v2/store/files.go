package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	v1store "cert-renamer/internal/store"
)

// StoreDir är certlagret: stabila, aldrig omdöpta original.
func StoreDir(cfg v1store.Config) string {
	if cfg.StoreDirV2 != "" {
		return cfg.StoreDirV2
	}
	return filepath.Join(cfg.InboxDir, "v2", "store")
}

// OutputDir är Spara-utmappen: omdöpta kopior med inbäddad metadata.
func OutputDir(cfg v1store.Config) string {
	if cfg.OutputDirV2 != "" {
		return cfg.OutputDirV2
	}
	return filepath.Join(cfg.InboxDir, "v2", "out")
}

// EnsureDirs skapar certlager + utmapp om de saknas.
func EnsureDirs(cfg v1store.Config) error {
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
	if len(base) > 120 {
		ext := filepath.Ext(base)
		base = base[:120-len(ext)] + ext
	}
	prefix := pdfHash
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return prefix + "__" + base
}

// WriteStoreFile skriver PDF:en till certlagret under sitt stabila namn.
// Kollisionssäkert via WriteUniqueFile (hash-prefixet gör i praktiken namnet
// unikt redan). Returnerar den faktiska sökvägen.
func WriteStoreFile(cfg v1store.Config, storedName string, data []byte) (string, error) {
	if err := os.MkdirAll(StoreDir(cfg), 0755); err != nil {
		return "", err
	}
	return v1store.WriteUniqueFile(StoreDir(cfg), storedName, data)
}

// StorePath är den förväntade sökvägen för ett lagrat cert.
func StorePath(cfg v1store.Config, storedName string) string {
	return filepath.Join(StoreDir(cfg), storedName)
}
