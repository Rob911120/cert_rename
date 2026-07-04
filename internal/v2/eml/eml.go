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
	ct := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		body, _ := io.ReadAll(msg.Body)
		out.Body = string(body)
		return out, nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		if err := walkParts(msg.Body, params["boundary"], out); err != nil {
			return nil, err
		}
	} else {
		body, _ := io.ReadAll(msg.Body)
		out.Body = string(body)
	}
	return out, nil
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
			out.Body = string(data)
		}
	}
}

func readPartDecoded(p *multipart.Part) ([]byte, error) {
	enc := strings.ToLower(p.Header.Get("Content-Transfer-Encoding"))
	raw, err := io.ReadAll(p)
	if err != nil {
		return nil, err
	}
	if enc == "base64" {
		clean := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(raw))
		return base64.StdEncoding.DecodeString(clean)
	}
	return raw, nil
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
	dec := new(mime.WordDecoder)
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}
