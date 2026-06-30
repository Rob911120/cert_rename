package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cert-renamer/internal/cert"
	"cert-renamer/internal/eml"
)

// UniquePath returnerar dir/name eller dir/name_N.ext om filen redan finns.
func UniquePath(dir, name string) string {
	full := filepath.Join(dir, name)
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return full
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 100; i++ {
		try := filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i, ext))
		if _, err := os.Stat(try); os.IsNotExist(err) {
			return try
		}
	}
	return full
}

// WriteUniqueFile skriver data atomiskt till dir/name (eller dir/name_N.ext
// vid kollision) via O_EXCL. Två parallella anrop kan aldrig välja samma
// path. Returnerar slutgiltig path. Fel om alla suffix _2..._99 är upptagna.
func WriteUniqueFile(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := filepath.Join(dir, name)
	for i := 1; i < 100; i++ {
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				_ = os.Remove(candidate)
				return "", werr
			}
			if cerr != nil {
				_ = os.Remove(candidate)
				return "", cerr
			}
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i+1, ext))
	}
	return "", fmt.Errorf("alla suffix upptagna för %s i %s", name, dir)
}

// EmailRawText returnerar email-body med header-prefix, trunkerad till MaxBodyBytes.
func EmailRawText(c *eml.Content) string {
	if c == nil {
		return ""
	}
	body := c.Body
	if len(body) > eml.MaxBodyBytes {
		body = body[:eml.MaxBodyBytes] + "\n[trunkerad]"
	}
	return fmt.Sprintf("Subject: %s\nFrom: %s\nDate: %s\n\n%s", c.Subject, c.From, c.Date, body)
}

// MoveToReview kopierar emlPath + (om ej nil) bilagan till review/<base>/ och
// skriver _reason.txt. Om ext ges bäddas extraktions-fält in i PDF-metadatan.
func MoveToReview(cfg Config, emlPath string, content *eml.Content, att *eml.Attachment, ext *cert.Extraction, bNums []string, reason string) {
	base := strings.TrimSuffix(filepath.Base(emlPath), filepath.Ext(emlPath))
	dir := filepath.Join(ReviewDir(cfg), base)
	_ = os.MkdirAll(dir, 0755)
	if data, err := os.ReadFile(emlPath); err == nil {
		_ = os.WriteFile(filepath.Join(dir, filepath.Base(emlPath)), data, 0644)
	}
	if att != nil {
		pdfPath := filepath.Join(dir, att.Filename)
		if err := os.WriteFile(pdfPath, att.Data, 0644); err == nil {
			meta := PdfMeta{
				BNumbers:         bNums,
				OriginalFilename: att.Filename,
				ExtractedAt:      time.Now().Format(time.RFC3339),
				Schema:           5,
				EmailSubject:     content.Subject,
				EmailFrom:        content.From,
				EmailDate:        content.Date,
				EmailBody:        content.Body,
				Verdict:          reason,
				Status:           "review",
			}
			if ext != nil {
				meta.Charge = ext.Charge
				meta.Material = ext.Material
				meta.EnStandardPresent = ext.EnStandardPresent
				meta.Dimensions = ext.Dimensions
				meta.CountryOfOrigin = ext.CountryOfOrigin
				meta.Confidence = ext.Confidence
				meta.Issues = ext.Issues
			}
			if err := EmbedMetadata(pdfPath, meta); err != nil {
				log.Printf("⚠️  kunde inte bädda in metadata i review-PDF %s: %v", pdfPath, err)
			}
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "_reason.txt"), []byte(reason+"\n"), 0644)
}

// ApproveQueueItem flyttar queue/<filename> till approved/, raderar sidecar-JSON
// och returnerar destinationssökvägen.
func ApproveQueueItem(cfg Config, filename string) (string, error) {
	src := filepath.Join(QueueDir(cfg), filename)
	dst := UniquePath(ApprovedDir(cfg), filename)
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	_ = os.Remove(src + ".json")
	return dst, nil
}

// ArchiveQueueItem flyttar queue/<filename> till arkiverat/, raderar
// sidecar-JSON och returnerar destinationssökvägen. Används för dubbletter
// och felaktiga poster som inte ska godkännas.
func ArchiveQueueItem(cfg Config, filename string) (string, error) {
	if err := os.MkdirAll(ArkiveratDir(cfg), 0755); err != nil {
		return "", err
	}
	src := filepath.Join(QueueDir(cfg), filename)
	dst := UniquePath(ArkiveratDir(cfg), filename)
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	_ = os.Rename(src+".json", dst+".json")
	_ = os.Remove(src + ".json")
	return dst, nil
}

// RenameQueueItem byter namn på en köfil och skriver om inbäddad metadata.
// Förväntar att newName redan är beräknat (t.ex. via cert.BuildFilename).
// Returnerar slutgiltigt nytt filnamn (efter ev. UniquePath-suffix).
func RenameQueueItem(cfg Config, oldName, newName string, meta PdfMeta) (string, error) {
	if oldName == newName {
		if err := EmbedMetadata(filepath.Join(QueueDir(cfg), oldName), meta); err != nil {
			return "", err
		}
		return oldName, nil
	}
	src := filepath.Join(QueueDir(cfg), oldName)
	dstFull := UniquePath(QueueDir(cfg), newName)
	if err := os.Rename(src, dstFull); err != nil {
		return "", err
	}
	_ = os.Rename(src+".json", dstFull+".json")
	if err := EmbedMetadata(dstFull, meta); err != nil {
		return filepath.Base(dstFull), err
	}
	return filepath.Base(dstFull), nil
}

// PromoteReviewToQueue tar en review-post (review/<base>/<pdfFilename>) +
// användar-bekräftade fält och skriver in den i kön i samma format som det
// normala eml-flödet (inbäddad metadata inkl. EmailRaw, namnkonvention,
// Status="queue"). Review-mappen raderas efter lyckad promote.
// Returnerar slutgiltigt filnamn i kön (efter ev. UniquePath-suffix).
func PromoteReviewToQueue(cfg Config, base, pdfFilename string, ext *cert.Extraction, bNums []string) (string, error) {
	if !safeName(base) {
		return "", fmt.Errorf("ogiltig base")
	}
	if !safeName(pdfFilename) {
		return "", fmt.Errorf("ogiltigt pdf-filnamn")
	}
	if !strings.EqualFold(filepath.Ext(pdfFilename), ".pdf") {
		return "", fmt.Errorf("bara PDF stöds")
	}
	if cfg.InboxDir == "" {
		return "", fmt.Errorf("ingen inbox vald")
	}
	reviewItemDir := filepath.Join(ReviewDir(cfg), base)
	srcPdf := filepath.Join(reviewItemDir, pdfFilename)
	data, err := os.ReadFile(srcPdf)
	if err != nil {
		return "", fmt.Errorf("läs pdf: %w", err)
	}
	if fails := cert.Validate(ext, bNums); len(fails) > 0 {
		return "", fmt.Errorf("validering: %s", strings.Join(fails, "; "))
	}

	name := cert.BuildFilename(ext, bNums)
	if err := os.MkdirAll(QueueDir(cfg), 0755); err != nil {
		return "", err
	}
	dst := UniquePath(QueueDir(cfg), name)
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	meta := PdfMeta{
		Charge:            ext.Charge,
		Material:          ext.Material,
		EnStandardPresent: ext.EnStandardPresent,
		ProductForm:       ext.ProductForm,
		Dimensions:        ext.Dimensions,
		CountryOfOrigin:   ext.CountryOfOrigin,
		BNumbers:          bNums,
		Confidence:        ext.Confidence,
		Issues:            ext.Issues,
		OriginalFilename:  pdfFilename,
		ExtractedAt:       time.Now().Format(time.RFC3339),
		Schema:            5,
		Status:            "queue",
	}
	// Försök läsa email-innehåll från .eml-fil om den finns
	if entries, err := os.ReadDir(reviewItemDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
				continue
			}
			if c, perr := eml.Parse(filepath.Join(reviewItemDir, e.Name())); perr == nil {
				meta.EmailSubject = c.Subject
				meta.EmailFrom = c.From
				meta.EmailDate = c.Date
				meta.EmailBody = c.Body
			}
			break
		}
	}
	if err := EmbedMetadata(dst, meta); err != nil {
		log.Printf("⚠️  kunde inte bädda in metadata i %s: %v", dst, err)
	}
	if err := os.RemoveAll(reviewItemDir); err != nil {
		log.Printf("⚠️  kunde inte rensa review-mapp %s: %v", reviewItemDir, err)
	}
	return filepath.Base(dst), nil
}

// safeName avvisar tomma strängar, path-separatorer och ".." för disk-ops
// som tar användarinmatade fil-/mappnamn.
func safeName(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}

// MoveToArchive kopierar emlPath till arkiverat/<base>/ + skriver _reason.txt.
func MoveToArchive(cfg Config, emlPath string, reason string) {
	base := strings.TrimSuffix(filepath.Base(emlPath), filepath.Ext(emlPath))
	dir := filepath.Join(ArkiveratDir(cfg), base)
	_ = os.MkdirAll(dir, 0755)
	if data, err := os.ReadFile(emlPath); err == nil {
		_ = os.WriteFile(filepath.Join(dir, filepath.Base(emlPath)), data, 0644)
	}
	_ = os.WriteFile(filepath.Join(dir, "_reason.txt"), []byte(reason+"\n"), 0644)
}
