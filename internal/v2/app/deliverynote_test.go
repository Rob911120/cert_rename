package app

import (
	"context"
	"os"
	"testing"

	"cert-renamer/internal/v2/domain"
	"cert-renamer/internal/v2/store"
)

func seedOrderRow(t *testing.T, a *App, rowID, poID int64, orderNumber string) {
	t.Helper()
	r := &domain.OrderRow{
		DeliveryRowID: rowID, PurchaseOrderID: poID, OrderNumber: orderNumber,
		PartNumber: "P-1", Description: "Plåt S355", FirstSeen: "2026-07-01T08:00:00Z",
	}
	if err := a.Repo.UpsertOrderRow(context.Background(), r, "2026-07-01T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func TestIngestDeliveryNote_WritesRowFileAndDedupes(t *testing.T) {
	a, notify, cfg := testApp(t)
	ctx := context.Background()
	in := DeliveryNoteInput{
		OriginalFilename: "foljesedel.jpg",
		Data:             []byte("\xff\xd8\xff fejk-jpeg bytes"),
		Supplier:         "SSAB",
		OrderNumber:      "b127562",
		Charge:           "43136",
		Quantity:         3,
		Unit:             "st",
	}
	d, dup, err := a.IngestDeliveryNote(ctx, in)
	if err != nil || dup {
		t.Fatalf("ingest: err=%v dup=%v", err, dup)
	}
	if d.OrderNumber != "B127562" {
		t.Errorf("ordernummer ska normaliseras till versaler, fick %q", d.OrderNumber)
	}
	// Bild skriven till följesedel-lagret.
	if _, err := os.ReadFile(store.DeliveryNoteImagePath(cfg, d.ImageFilename)); err != nil {
		t.Errorf("bild inte skriven: %v", err)
	}
	// Dedupe: samma bytes igen → no-op.
	d2, dup2, err := a.IngestDeliveryNote(ctx, in)
	if err != nil || !dup2 {
		t.Fatalf("dublett-ingest: err=%v dup=%v", err, dup2)
	}
	if d2.ID != d.ID {
		t.Errorf("dublett ska returnera samma rad, %d != %d", d2.ID, d.ID)
	}
	list, _ := a.ListDeliveryNotes(ctx, domain.DNMottagen)
	if len(list) != 1 {
		t.Errorf("förväntade 1 följesedel, fick %d", len(list))
	}
	if notify.overview == 0 {
		t.Error("OverviewChanged aldrig pingat")
	}
}

func TestMatchDeliveryNoteLocal(t *testing.T) {
	a, _, _ := testApp(t)
	ctx := context.Background()

	// Exakt en rad för ordern → auto-match.
	seedOrderRow(t, a, 100, 900, "B127562")
	d, _, err := a.IngestDeliveryNote(ctx, DeliveryNoteInput{
		OriginalFilename: "a.jpg", Data: []byte("jpeg-a"), OrderNumber: "B127562",
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.MatchDeliveryNoteLocal(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Matched || m.Row.DeliveryRowID != 100 {
		t.Fatalf("förväntade auto-match mot rad 100, fick %+v", m)
	}
	got, _ := a.GetDeliveryNote(ctx, d.ID)
	if got.MatchedRowID != 100 || got.MatchedPOID != 900 {
		t.Errorf("matchnings-datafält ej satta: rad=%d po=%d", got.MatchedRowID, got.MatchedPOID)
	}

	// Två rader för ordern → ingen auto-match, två kandidater.
	seedOrderRow(t, a, 200, 901, "B999999")
	seedOrderRow(t, a, 201, 901, "B999999")
	d2, _, _ := a.IngestDeliveryNote(ctx, DeliveryNoteInput{
		OriginalFilename: "b.jpg", Data: []byte("jpeg-b"), OrderNumber: "B999999",
	})
	m2, err := a.MatchDeliveryNoteLocal(ctx, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Matched {
		t.Error("två kandidater ska INTE auto-matcha")
	}
	if len(m2.Candidates) != 2 {
		t.Errorf("förväntade 2 kandidater, fick %d", len(m2.Candidates))
	}
}

func TestDeliveryNoteLifecycle(t *testing.T) {
	a, _, _ := testApp(t)
	ctx := context.Background()
	d, _, _ := a.IngestDeliveryNote(ctx, DeliveryNoteInput{OriginalFilename: "c.jpg", Data: []byte("jpeg-c")})

	if err := a.RejectDeliveryNote(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkDeliveryNoteRegistered(ctx, d.ID); err == nil {
		t.Error("avfardad→inlevererad ska avvisas (ogiltig övergång)")
	}
	if err := a.UnrejectDeliveryNote(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkDeliveryNoteRegistered(ctx, d.ID); err != nil {
		t.Fatalf("mottagen→inlevererad ska tillåtas: %v", err)
	}
	// Inlevererade faller ur den mottagna listan.
	list, _ := a.ListDeliveryNotes(ctx, domain.DNMottagen)
	if len(list) != 0 {
		t.Errorf("inlevererad ska inte listas som mottagen, fick %d", len(list))
	}
}
