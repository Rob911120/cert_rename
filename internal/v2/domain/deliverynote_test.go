package domain

import "testing"

func TestDeliveryNoteCanTransition(t *testing.T) {
	cases := []struct {
		from, to DeliveryNoteStatus
		want     bool
	}{
		{DNMottagen, DNInlevererad, true},
		{DNMottagen, DNAvfardad, true},
		{DNMottagen, DNMottagen, false},
		{DNAvfardad, DNMottagen, true},
		{DNAvfardad, DNInlevererad, false},
		{DNInlevererad, DNMottagen, false}, // terminal
		{DNInlevererad, DNAvfardad, false},
	}
	for _, c := range cases {
		if got := DeliveryNoteCanTransition(c.from, c.to); got != c.want {
			t.Errorf("%s→%s: got %v want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestDeliveryNoteMatchedAndLiving(t *testing.T) {
	d := &DeliveryNote{Status: DNMottagen}
	if d.Matched() {
		t.Error("ny följesedel ska inte vara matchad")
	}
	if !d.Living() {
		t.Error("mottagen följesedel ska vara living")
	}
	d.MatchedRowID = 42
	if !d.Matched() {
		t.Error("satt MatchedRowID → Matched()")
	}
	d.Status = DNInlevererad
	if d.Living() {
		t.Error("inlevererad följesedel ska inte vara living")
	}
}
