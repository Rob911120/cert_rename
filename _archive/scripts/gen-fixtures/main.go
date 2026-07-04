// gen-fixtures bygger 20 syntetiska .eml-filer i testdata/synthetic-eml/
// från scenario-tabellen i scenarios.go. PDF:er hämtas från
// inbox/approved/ och strippas på "CertRenamer"-property innan de
// bäddas in.
//
// Användning: go run ./scripts/gen-fixtures
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	approvedDir = "inbox/approved"
	outDir      = "testdata/synthetic-eml"
	pdfCacheDir = "testdata/synthetic-eml/_pdfs"
)

func main() {
	log.SetFlags(0)
	if _, err := os.Stat("go.mod"); err != nil {
		log.Fatal("kör från projekt-roten ~/Projects/cert-renamer")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	// Strippa alla approved-PDF:er en gång till cachen.
	stripped := make([]string, len(approvedPdfs))
	for i, base := range approvedPdfs {
		src := filepath.Join(approvedDir, base)
		dst := filepath.Join(pdfCacheDir, base)
		if err := stripCertRenamerMeta(src, dst); err != nil {
			log.Fatalf("strip %s: %v", base, err)
		}
		stripped[i] = dst
	}

	for i, sc := range scenarios() {
		path := filepath.Join(outDir, sc.Name+".eml")
		body, err := buildEml(sc, stripped)
		if err != nil {
			log.Fatalf("scenario %d (%s): %v", i+1, sc.Name, err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("✓ %s (%d bytes)\n", sc.Name, len(body))
	}
	fmt.Printf("\n%d fixturer skrivna till %s\n", len(scenarios()), outDir)
}

func buildEml(sc Scenario, strippedPdfs []string) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	rootCT := "multipart/mixed"
	if sc.AltOnly {
		rootCT = "multipart/alternative"
	}

	// === Top-level headers ===
	subj := sc.Subject
	if sc.SubjectEncode {
		subj = mime.BEncoding.Encode("utf-8", subj)
	}
	headers := []string{
		"MIME-Version: 1.0",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"From: " + sc.From,
		"To: " + sc.To,
		"Subject: " + subj,
		fmt.Sprintf(`Content-Type: %s; boundary="%s"`, rootCT, mw.Boundary()),
		"", "",
	}
	out := []byte(strings.Join(headers, "\r\n"))

	// === Body part(s) ===
	switch {
	case sc.AltOnly:
		// text/plain + text/html. PDF-bilagor ignoreras (det är poängen).
		if err := writePart(mw, "text/plain; charset=utf-8", "", "", []byte(sc.Body)); err != nil {
			return nil, err
		}
		html := fmt.Sprintf("<html><body><p>%s</p></body></html>", sc.Body)
		if err := writePart(mw, "text/html; charset=utf-8", "", "", []byte(html)); err != nil {
			return nil, err
		}
	default:
		body := sc.Body
		if sc.LargeBodyPad > 0 {
			body += strings.Repeat(loremLine, sc.LargeBodyPad*1024/len(loremLine)+1)
		}
		if err := writePart(mw, "text/plain; charset=utf-8", "", "", []byte(body)); err != nil {
			return nil, err
		}
		if sc.TextAttach != "" {
			if err := writePart(mw, "text/plain; charset=utf-8", "leveransspec.txt", "attachment", []byte(sc.TextAttach)); err != nil {
				return nil, err
			}
		}
		for _, p := range sc.PDFs {
			data, err := pdfBytes(p, strippedPdfs)
			if err != nil {
				return nil, err
			}
			if err := writeBase64Part(mw, "application/pdf", p.attachName, "attachment", data); err != nil {
				return nil, err
			}
		}
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}
	out = append(out, buf.Bytes()...)
	return out, nil
}

func pdfBytes(p pdfRef, stripped []string) ([]byte, error) {
	switch {
	case p.srcIdx >= 0 && p.srcIdx < len(stripped):
		return os.ReadFile(stripped[p.srcIdx])
	case p.srcIdx == -1:
		// Korrupt: ser ut som PDF-magic men resten är skräp.
		return []byte("%PDF-1.4\n%corrupted-fixture\n"), nil
	case p.srcIdx == -2:
		// Minimal "tom" PDF — räknas inte som 3.1-cert.
		return []byte(minimalPDF), nil
	}
	return nil, fmt.Errorf("okänt srcIdx %d", p.srcIdx)
}

func writePart(mw *multipart.Writer, contentType, filename, disposition string, data []byte) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", contentType)
	if disposition != "" {
		h.Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, filename))
	}
	h.Set("Content-Transfer-Encoding", "8bit")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = pw.Write(data)
	return err
}

func writeBase64Part(mw *multipart.Writer, contentType, filename, disposition string, data []byte) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", fmt.Sprintf(`%s; name="%s"`, contentType, filename))
	h.Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, filename))
	h.Set("Content-Transfer-Encoding", "base64")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(data)
	// Wrappa till 76 kolumner för pli MIME-konvention.
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		if _, err := pw.Write([]byte(enc[i:end] + "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

const loremLine = "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore.\r\n"

// minimalPDF — en absolut minimal giltig PDF (1 sida, ingen text)
// för att testa "valid PDF utan 3.1-fält"-flödet.
const minimalPDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<<>>/Contents 4 0 R>>endobj
4 0 obj<</Length 0>>stream
endstream
endobj
xref
0 5
0000000000 65535 f
0000000009 00000 n
0000000052 00000 n
0000000098 00000 n
0000000178 00000 n
trailer<</Size 5/Root 1 0 R>>
startxref
220
%%EOF
`
