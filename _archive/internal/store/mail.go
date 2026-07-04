package store

// Avvikelsemail-utkast (mailto) för Kommande inleveranser. Samma format som
// UI:ts knappar (openRestMail/openCertMissingMail i index.html) — Go-versionen
// är källan för Sickans compose_deviation_mail och framtida användning.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Avvikelsemail-typer.
const (
	MailKindRest        = "rest"         // positioner som inte levererats in
	MailKindCertMissing = "cert_missing" // positioner som saknar materialcert
)

// DeviationMail är ett färdigt mailutkast.
type DeviationMail struct {
	To        string   `json:"to"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	MailtoURL string   `json:"mailto_url"`
	Positions []string `json:"positions"`
}

// BuildDeviationMail bygger ett utkast för en order. rows är orderns rader
// (funktionen filtrerar själv på kind); to är avvikelseadressen (ReportEmail).
func BuildDeviationMail(rows []UpcomingDelivery, kind, to string) (*DeviationMail, error) {
	if to == "" {
		return nil, fmt.Errorf("ingen avvikelseadress ifylld — sätt ✉️ Avvikelsemail i ⚙️ Inställningar")
	}
	var picked []UpcomingDelivery
	var suffix, intro string
	switch kind {
	case MailKindRest:
		suffix, intro = "ej inlevererade positioner", "levererades inte in"
		for _, r := range rows {
			if r.LocalStatus != UpcomingDelivered {
				picked = append(picked, r)
			}
		}
	case MailKindCertMissing:
		suffix, intro = "cert saknas", "saknar materialcert"
		for _, r := range rows {
			if r.CertStatus == CertMissing {
				picked = append(picked, r)
			}
		}
	default:
		return nil, fmt.Errorf("okänd mailtyp: %q (rest eller cert_missing)", kind)
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("inga positioner matchar mailtypen %q på ordern", kind)
	}

	orderKey := picked[0].OrderNumber
	if orderKey == "" {
		orderKey = fmt.Sprintf("PO %d", picked[0].PurchaseOrderID)
	}
	supplier := ""
	for _, r := range picked {
		if r.SupplierName != "" {
			supplier = r.SupplierName
			break
		}
	}
	orderLabel := orderKey
	if supplier != "" {
		orderLabel += " (" + supplier + ")"
	}

	lines := make([]string, 0, len(picked))
	for _, r := range picked {
		extra := ""
		if kind == MailKindCertMissing && r.RequiredCert != "" {
			extra = ", krav: " + r.RequiredCert
		}
		lines = append(lines, positionLine(r, extra))
	}

	subject := fmt.Sprintf("Order %s — %s", orderLabel, suffix)
	body := fmt.Sprintf("Hej Daniel,\n\nFöljande positioner på order %s %s:\n\n%s\n\n",
		orderLabel, intro, strings.Join(lines, "\n"))
	mailto := "mailto:" + mailtoEscape(to) +
		"?subject=" + mailtoEscape(subject) +
		"&body=" + mailtoEscape(body)
	return &DeviationMail{To: to, Subject: subject, Body: body, MailtoURL: mailto, Positions: lines}, nil
}

// mailtoEscape URL-kodar för mailto-URL:er. QueryEscape kodar mellanslag som
// "+" vilket mailklienter visar bokstavligt — mailto kräver %20 (som JS:ets
// encodeURIComponent).
func mailtoEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// positionLine: "- ART-123 — Benämning, 5 st, lev.datum 2026-07-01" (+ ev. extra).
func positionLine(r UpcomingDelivery, extra string) string {
	s := "- "
	if r.PartNumber != "" {
		s += r.PartNumber
	} else {
		s += fmt.Sprintf("art %d", r.PartID)
	}
	if d := rowDescription(r); d != "" {
		s += " — " + d
	}
	if r.PlannedQty > 0 {
		s += fmt.Sprintf(", %g st", r.PlannedQty)
	}
	if r.DeliveryDate != "" {
		s += ", lev.datum " + r.DeliveryDate
	}
	return s + extra
}

// rowDescription: benämningen — toppnivåfältet med fallback till evidence_json
// (samma logik som UI:ts rowDescription).
func rowDescription(r UpcomingDelivery) string {
	if r.Description != "" {
		return r.Description
	}
	var ev struct {
		PartDescription string `json:"part_description"`
	}
	if json.Unmarshal([]byte(r.EvidenceJSON), &ev) == nil {
		return ev.PartDescription
	}
	return ""
}
