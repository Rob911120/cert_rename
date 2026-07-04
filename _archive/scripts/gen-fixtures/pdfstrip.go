package main

import (
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// stripCertRenamerMeta kopierar src till dst med "CertRenamer"-property
// borttagen ur Info-dict. Om dst redan finns och är nyare än src så är
// den en cache-träff och vi gör inget.
func stripCertRenamerMeta(src, dst string) error {
	if cached(src, dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return api.RemovePropertiesFile(src, dst, []string{"CertRenamer"}, nil)
}

func cached(src, dst string) bool {
	si, err := os.Stat(src)
	if err != nil {
		return false
	}
	di, err := os.Stat(dst)
	if err != nil {
		return false
	}
	return di.ModTime().After(si.ModTime())
}
