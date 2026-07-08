// Package eml parsar .eml-filer till struktur + bilagor och extraherar B-nummer
// från text. Inga deps utöver standard-biblioteket.
package eml

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
)

// MaxBodyBytes är gränsen för hur mycket email-body som inkluderas i AI-anrop
// och i lagrad metadata.
const MaxBodyBytes = 64 * 1024

type Content struct {
	Subject     string
	From        string
	Date        string
	Body        string
	Attachments []Attachment
}

type Attachment struct {
	Filename string
	Data     []byte
}

// Parse läser .eml-filen och returnerar struktur + bilagor.
func Parse(path string) (*Content, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	msg, err := mail.ReadMessage(f)
	if err != nil {
		return nil, fmt.Errorf("mail.ReadMessage: %w", err)
	}
	out := &Content{
		Subject: decodeHeader(msg.Header.Get("Subject")),
		From:    decodeHeader(msg.Header.Get("From")),
		Date:    msg.Header.Get("Date"),
	}
	cte := msg.Header.Get("Content-Transfer-Encoding")
	ct := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		out.Body = decodeBody(msg.Body, cte, "")
		return out, nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		if err := walkParts(msg.Body, params["boundary"], out); err != nil {
			return nil, err
		}
	} else {
		// Icke-multipart: kroppen kan vara quoted-printable/base64-kodad och i
		// annan charset än UTF-8 — utan avkodning blir svenska mejl
		// "f=C3=B6r"-soppa och B-nummer kan brytas av mjuka radbrytningar.
		out.Body = decodeBody(msg.Body, cte, params["charset"])
	}
	return out, nil
}

// decodeBody läser en mailkropp och avkodar Content-Transfer-Encoding
// (quoted-printable/base64) samt charset till UTF-8. Fel är mjuka: den råa
// texten är alltid bättre än ingen text.
func decodeBody(r io.Reader, cte, charset string) string {
	raw, _ := io.ReadAll(r)
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		clean := strings.Map(dropB64Whitespace, string(raw))
		if b, err := base64.StdEncoding.DecodeString(clean); err == nil {
			raw = b
		}
	case "quoted-printable":
		if b, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw))); err == nil || len(b) > 0 {
			raw = b
		}
	}
	return decodeCharset(raw, charset)
}

func walkParts(r io.Reader, boundary string, out *Content) error {
	mr := multipart.NewReader(r, boundary)
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		ct := p.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(ct)
		if strings.HasPrefix(mediaType, "multipart/") {
			if err := walkParts(p, params["boundary"], out); err != nil {
				return err
			}
			continue
		}
		filename := ""
		if cd := p.Header.Get("Content-Disposition"); cd != "" {
			if _, dparams, err := mime.ParseMediaType(cd); err == nil {
				filename = decodeHeader(dparams["filename"])
			}
		}
		if filename == "" {
			filename = decodeHeader(params["name"])
		}
		data, err := readPartDecoded(p)
		if err != nil {
			return err
		}
		if strings.EqualFold(mediaType, "application/pdf") ||
			strings.HasSuffix(strings.ToLower(filename), ".pdf") {
			out.Attachments = append(out.Attachments, Attachment{Filename: filename, Data: data})
			continue
		}
		if isZipAttachment(mediaType, filename) {
			out.Attachments = append(out.Attachments, extractPDFsFromZip(data)...)
			continue
		}
		if mediaType == "text/plain" && out.Body == "" {
			out.Body = decodeCharset(data, params["charset"])
		}
	}
}

func readPartDecoded(p *multipart.Part) ([]byte, error) {
	// quoted-printable avkodas transparent av mime/multipart (headern göms);
	// base64 måste vi avkoda själva.
	enc := strings.ToLower(p.Header.Get("Content-Transfer-Encoding"))
	raw, err := io.ReadAll(p)
	if err != nil {
		return nil, err
	}
	if enc == "base64" {
		clean := strings.Map(dropB64Whitespace, string(raw))
		return base64.StdEncoding.DecodeString(clean)
	}
	return raw, nil
}

func dropB64Whitespace(r rune) rune {
	if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
		return -1
	}
	return r
}

// isZipAttachment matchar zip-MIME-typer eller .zip-suffix på filnamn.
func isZipAttachment(mediaType, filename string) bool {
	if strings.EqualFold(mediaType, "application/zip") ||
		strings.EqualFold(mediaType, "application/x-zip-compressed") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(filename), ".zip")
}

// extractPDFsFromZip läser zip-byten och returnerar PDF-filerna inuti som
// Attachment:s. Subdir-prefix flattenas (filepath.Base). Vid fel loggas och
// en (möjligen tom) slice returneras — Parse fortsätter med övriga delar.
func extractPDFsFromZip(data []byte) []Attachment {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		log.Printf("eml: zip extract: %v", err)
		return nil
	}
	var out []Attachment
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".pdf") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			log.Printf("eml: zip open %s: %v", f.Name, err)
			continue
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Printf("eml: zip read %s: %v", f.Name, err)
			continue
		}
		out = append(out, Attachment{Filename: filepath.Base(f.Name), Data: b})
	}
	return out
}

func decodeHeader(s string) string {
	out, err := headerDecoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// headerDecoder avkodar encoded-words (=?charset?Q?...?=) i ämnen/filnamn.
// WordDecoder hanterar bara utf-8/iso-8859-1/us-ascii själv — svenska mejl
// från äldre system använder ofta windows-1252, som annars lämnas som rå
// MIME-token i UI, DB och AI-prompt.
var headerDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		raw, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(decodeCharset(raw, charset)), nil
	},
}

// decodeCharset konverterar mailtext till UTF-8 utifrån charset-parametern.
// Bara de kodningar svenska mejl faktiskt använder hanteras: UTF-8/ASCII
// passerar orört, ISO-8859-1/-15 och Windows-1252 avkodas byte-för-byte.
// Okänd charset returneras oförändrad — rå text är bättre än ingen.
func decodeCharset(data []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return string(data)
	case "iso-8859-1", "iso8859-1", "latin1", "iso-8859-15", "iso8859-15", "windows-1252", "cp1252":
		var b strings.Builder
		b.Grow(len(data) + len(data)/2)
		for _, c := range data {
			if c >= 0x80 && c <= 0x9F {
				// Windows-1252:s tryckbara område; i ren latin-1 är det
				// (oanvända) kontrolltecken, så mappningen är säker för båda.
				b.WriteRune(cp1252High[c-0x80])
			} else {
				b.WriteRune(rune(c)) // latin-1: bytevärde = kodpunkt
			}
		}
		return b.String()
	default:
		return string(data)
	}
}

// cp1252High är Windows-1252:s tecken för 0x80–0x9F (0xFFFD för odefinierade).
var cp1252High = [32]rune{
	'€', 0xFFFD, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0xFFFD, 'Ž', 0xFFFD,
	0xFFFD, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0xFFFD, 'ž', 'Ÿ',
}
