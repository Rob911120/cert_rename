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

func imagePart(filename, contentType string, data []byte) emlPart {
	return emlPart{
		contentType: fmt.Sprintf(`%s; name=%q`, contentType, filename),
		disposition: fmt.Sprintf(`attachment; filename=%q`, filename),
		encoding:    "base64",
		data:        data,
	}
}

func Test_Parse_KeepsImageAttachmentWithMediaType(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff fejk-jpeg")
	path := buildEml(t, []emlPart{
		textPart("Här är följesedeln"),
		imagePart("foljesedel.jpg", "image/jpeg", jpeg),
	})
	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Attachments) != 1 {
		t.Fatalf("förväntade 1 bild-bilaga, fick %d", len(c.Attachments))
	}
	att := c.Attachments[0]
	if att.MediaType != "image/jpeg" {
		t.Errorf("MediaType = %q, vill ha image/jpeg", att.MediaType)
	}
	if !bytes.Equal(att.Data, jpeg) {
		t.Error("bilddata skiljer sig efter parse")
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

// Icke-multipart mejl: quoted-printable + ISO-8859-1 ska avkodas — annars blir
// svenska kroppar "H=E4r"-soppa och B-nummer brutna över mjuka radbrytningar
// missas av ExtractBNumbers.
func Test_Parse_NonMultipartQuotedPrintableLatin1(t *testing.T) {
	raw := "From: mill@ssab.com\r\n" +
		"To: rob@example.se\r\n" +
		"Subject: =?windows-1252?Q?Certifikat_f=F6r_st=E5lplattor?=\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"H=E4rdat st=E5l f=F6r order B1273=\r\n" +
		"40. Se bifogat certifikat.\r\n"
	path := filepath.Join(t.TempDir(), "qp.eml")
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Subject != "Certifikat för stålplattor" {
		t.Errorf("windows-1252-ämne avkodades inte: %q", c.Subject)
	}
	if want := "Härdat stål för order B127340. Se bifogat certifikat.\r\n"; c.Body != want {
		t.Errorf("body = %q, vill ha %q", c.Body, want)
	}
	if got := ExtractBNumbers(c.Body); len(got) != 1 || got[0] != "B127340" {
		t.Errorf("B-nummer över mjuk radbrytning: %v", got)
	}
}

// Multipart text/plain i latin-1 ska konverteras till UTF-8.
func Test_Parse_Latin1TextPart(t *testing.T) {
	path := buildEml(t, []emlPart{{
		contentType: "text/plain; charset=iso-8859-1",
		data:        []byte("Best\xe4llning p\xe5 60mm pl\xe5t\n"),
	}})
	c, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Body != "Beställning på 60mm plåt\n" {
		t.Errorf("latin-1-body konverterades inte: %q", c.Body)
	}
}

// B-nummer med litet b ska hittas och versaliseras — resten av appen jämför
// ordernummer versaliserat.
func Test_ExtractBNumbers_CaseInsensitive(t *testing.T) {
	got := ExtractBNumbers("ordernr b127575 och B127575 samt b127576")
	if len(got) != 2 || got[0] != "B127575" || got[1] != "B127576" {
		t.Errorf("ExtractBNumbers = %v", got)
	}
}
