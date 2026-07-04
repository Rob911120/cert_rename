package eml

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
)

// fakePDF returnerar bytes som ser ut som en PDF. Eml-parsern verifierar inte
// PDF-innehåll — bara filändelse/MIME — så ett enkelt header räcker.
func fakePDF(label string) []byte {
	return []byte("%PDF-1.4\n%" + label + "\n%%EOF")
}

// buildZipBytes packar files (namn→bytes) i en zip-bytestream.
func buildZipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip.Write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

type emlPart struct {
	contentType string
	disposition string
	encoding    string
	data        []byte
}

// buildEml skriver en multipart/mixed eml till en temp-fil och returnerar
// sökvägen. Varje part skrivs med valfri Content-Disposition och
// Content-Transfer-Encoding (om "base64" så base64-kodas data:n).
func buildEml(t *testing.T, parts []emlPart) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.SetBoundary("BOUNDARY"); err != nil {
		t.Fatalf("set boundary: %v", err)
	}
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", p.contentType)
		if p.disposition != "" {
			h.Set("Content-Disposition", p.disposition)
		}
		if p.encoding != "" {
			h.Set("Content-Transfer-Encoding", p.encoding)
		}
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("CreatePart: %v", err)
		}
		var payload []byte
		if p.encoding == "base64" {
			payload = []byte(base64.StdEncoding.EncodeToString(p.data))
		} else {
			payload = p.data
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close mw: %v", err)
	}

	var emlBuf bytes.Buffer
	fmt.Fprintf(&emlBuf, "From: test@example.com\r\n")
	fmt.Fprintf(&emlBuf, "To: cert@example.com\r\n")
	fmt.Fprintf(&emlBuf, "Subject: test\r\n")
	fmt.Fprintf(&emlBuf, "Date: Tue, 5 May 2026 10:00:00 +0000\r\n")
	fmt.Fprintf(&emlBuf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&emlBuf, "Content-Type: multipart/mixed; boundary=%q\r\n", "BOUNDARY")
	fmt.Fprintf(&emlBuf, "\r\n")
	emlBuf.Write(body.Bytes())

	path := filepath.Join(t.TempDir(), "test.eml")
	if err := os.WriteFile(path, emlBuf.Bytes(), 0644); err != nil {
		t.Fatalf("write eml: %v", err)
	}
	return path
}

func zipPart(filename string, data []byte) emlPart {
	return emlPart{
		contentType: fmt.Sprintf(`application/zip; name=%q`, filename),
		disposition: fmt.Sprintf(`attachment; filename=%q`, filename),
		encoding:    "base64",
		data:        data,
	}
}

func pdfPart(filename string, data []byte) emlPart {
	return emlPart{
		contentType: fmt.Sprintf(`application/pdf; name=%q`, filename),
		disposition: fmt.Sprintf(`attachment; filename=%q`, filename),
		encoding:    "base64",
		data:        data,
	}
}

func textPart(text string) emlPart {
	return emlPart{
		contentType: "text/plain; charset=utf-8",
		data:        []byte(text),
	}
}

func Test_Parse_ExtractsPdfsFromZip(t *testing.T) {
	zipData := buildZipBytes(t, map[string][]byte{
		"B1.pdf": fakePDF("first"),
		"B2.pdf": fakePDF("second"),
	})
	path := buildEml(t, []emlPart{
		textPart("body\n"),
		zipPart("certs.zip", zipData),
	})

	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Attachments) != 2 {
		t.Fatalf("förväntade 2 bilagor, fick %d", len(c.Attachments))
	}
	names := map[string]bool{}
	for _, a := range c.Attachments {
		names[a.Filename] = true
	}
	if !names["B1.pdf"] || !names["B2.pdf"] {
		t.Errorf("filnamn matchar inte (vill ha B1.pdf+B2.pdf): %v", names)
	}
}

func Test_Parse_ZipWithSubdir_FlattensName(t *testing.T) {
	zipData := buildZipBytes(t, map[string][]byte{
		"certs/B1.pdf": fakePDF("nested"),
	})
	path := buildEml(t, []emlPart{
		textPart("body\n"),
		zipPart("certs.zip", zipData),
	})

	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Attachments) != 1 {
		t.Fatalf("förväntade 1 bilaga, fick %d", len(c.Attachments))
	}
	if c.Attachments[0].Filename != "B1.pdf" {
		t.Errorf("filnamn ska flatten:as till 'B1.pdf', fick %q", c.Attachments[0].Filename)
	}
}

func Test_Parse_ZipFiltersNonPdfFiles(t *testing.T) {
	zipData := buildZipBytes(t, map[string][]byte{
		"B1.pdf":    fakePDF("real"),
		"notes.txt": []byte("text content"),
		"doc.docx":  []byte("word content"),
	})
	path := buildEml(t, []emlPart{
		textPart("body\n"),
		zipPart("certs.zip", zipData),
	})

	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Attachments) != 1 {
		var names []string
		for _, a := range c.Attachments {
			names = append(names, a.Filename)
		}
		t.Fatalf("förväntade 1 PDF, fick %d (%v)", len(c.Attachments), names)
	}
	if c.Attachments[0].Filename != "B1.pdf" {
		t.Errorf("fel namn: %q", c.Attachments[0].Filename)
	}
}

func Test_Parse_MalformedZip_Skipped(t *testing.T) {
	path := buildEml(t, []emlPart{
		textPart("body\n"),
		zipPart("certs.zip", []byte("inte en zip")),
	})

	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse ska INTE returnera fel vid trasig zip: %v", err)
	}
	if len(c.Attachments) != 0 {
		t.Errorf("förväntade 0 bilagor från trasig zip, fick %d", len(c.Attachments))
	}
}

func Test_Parse_MixedPdfAndZip(t *testing.T) {
	zipData := buildZipBytes(t, map[string][]byte{
		"B2.pdf": fakePDF("from-zip"),
	})
	path := buildEml(t, []emlPart{
		textPart("body\n"),
		pdfPart("A1.pdf", fakePDF("direct")),
		zipPart("certs.zip", zipData),
	})

	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Attachments) != 2 {
		var names []string
		for _, a := range c.Attachments {
			names = append(names, a.Filename)
		}
		t.Fatalf("förväntade 2 bilagor (1 direkt + 1 från zip), fick %d (%v)", len(c.Attachments), names)
	}
	names := map[string]bool{}
	for _, a := range c.Attachments {
		names[a.Filename] = true
	}
	if !names["A1.pdf"] || !names["B2.pdf"] {
		t.Errorf("filnamn matchar inte (vill ha A1.pdf+B2.pdf): %v", names)
	}
}
