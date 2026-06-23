package sickan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cert-renamer/internal/monitor"
	"cert-renamer/internal/store"
)

// deliveryStub spelar Monitor och RÄKNAR ReportArrivals-anrop (writes), så att
// testerna kan bevisa att write-gaten håller.
type deliveryStub struct {
	srv      *httptest.Server
	arrivals atomic.Int64
}

func newDeliveryStub(t *testing.T) *deliveryStub {
	t.Helper()
	ds := &deliveryStub{}
	ds.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/001.1/login"):
			_, _ = w.Write([]byte(`{"SessionId":"sess-xyz"}`))
		case strings.HasSuffix(r.URL.Path, "/PurchaseOrders/ReportArrivals"):
			ds.arrivals.Add(1)
			_, _ = w.Write([]byte(`{"Ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrders"):
			_, _ = w.Write([]byte(`{"value":[{"Id":1,"OrderNumber":"B127196","Status":1,"BusinessContactId":7}]}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/PurchaseOrderRows"):
			_, _ = w.Write([]byte(`{"value":[{"Id":11,"ParentOrderId":1,"PartId":5,"RowIndex":1,"OrderedQuantity":10,"RestQuantity":10}]}`))
		case strings.HasSuffix(r.URL.Path, "/Inventory/ProductRecords"):
			_, _ = w.Write([]byte(`{"value":[{"Id":99,"ChargeNumber":"610042","PartId":5,"PurchaseOrderId":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/Purchase/Suppliers"):
			_, _ = w.Write([]byte(`{"value":[{"Id":7,"Name":"BE Group"}]}`))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(ds.srv.Close)
	return ds
}

func setupDeliveryToolbox(t *testing.T) (*Toolbox, *deliveryStub) {
	t.Helper()
	tb, _ := setupToolbox(t)
	ds := newDeliveryStub(t)
	mc := monitor.New(ds.srv.URL)
	if err := mc.Login(context.Background(), "u", "p"); err != nil {
		t.Fatalf("monitor login: %v", err)
	}
	tb.Monitor = mc

	db, err := store.InitDB(t.TempDir() + "/dn.db")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	tb.Repo = store.NewRepository(db)
	return tb, ds
}

// seedDeliveryNote infogar dn och tillämpar sedan matchning/förslag/status via
// repo-metoderna (samma väg som det riktiga flödet — InsertDeliveryNote sätter
// bara basfälten; matchad PO/rad och slutstatus sätts av update-metoderna).
func seedDeliveryNote(t *testing.T, tb *Toolbox, dn *store.DeliveryNote) int64 {
	t.Helper()
	finalStatus := dn.Status
	if finalStatus == "" {
		finalStatus = store.DNUnmatched
	}
	base := *dn
	base.Status = store.DNUnmatched
	id, err := tb.Repo.InsertDeliveryNote(&base)
	if err != nil {
		t.Fatalf("insert dn: %v", err)
	}
	if dn.MatchedPOID != 0 || dn.MatchedRowID != 0 {
		if err := tb.Repo.UpdateDeliveryNoteMatch(id, dn.MatchedPOID, dn.MatchedRowID, store.DNMatchedPO); err != nil {
			t.Fatalf("seed match: %v", err)
		}
	}
	if dn.ProposedQuantity != 0 {
		if err := tb.Repo.UpdateDeliveryNoteProposal(id, dn.ProposedQuantity, store.DNReceivingProposed); err != nil {
			t.Fatalf("seed proposal: %v", err)
		}
	}
	if err := tb.Repo.UpdateDeliveryNoteStatus(id, finalStatus); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	return id
}

func Test_DeliveryFlow_MatchSetsRow(t *testing.T) {
	tb, _ := setupDeliveryToolbox(t)
	id := seedDeliveryNote(t, tb, &store.DeliveryNote{
		ImageFilename: "dn1.png", OrderNumber: "B127196", Charge: "610042", Quantity: 6, Unit: "st",
	})
	res, err := tb.Dispatch("match_delivery_note_to_po", json.RawMessage(`{"id":`+itoa(id)+`}`))
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if !strings.Contains(res.Summary, `"matched":true`) {
		t.Fatalf("förväntade matchning: %s", res.Summary)
	}
	dn, _ := tb.Repo.GetDeliveryNote(id)
	if dn.Status != store.DNMatchedPO || dn.MatchedRowID != 11 || dn.MatchedPOID != 1 {
		t.Fatalf("dn efter match = %+v", dn)
	}
}

func Test_DeliveryFlow_ProposeDoesNotWrite(t *testing.T) {
	tb, ds := setupDeliveryToolbox(t)
	id := seedDeliveryNote(t, tb, &store.DeliveryNote{
		ImageFilename: "dn1.png", OrderNumber: "B127196", Charge: "610042", Quantity: 6, Unit: "st",
		Status: store.DNMatchedPO, MatchedPOID: 1, MatchedRowID: 11,
	})
	res, err := tb.Dispatch("propose_receiving", json.RawMessage(`{"id":`+itoa(id)+`}`))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !strings.Contains(res.Summary, "PurchaseOrderRowId") {
		t.Errorf("preview saknar payload: %s", res.Summary)
	}
	if ds.arrivals.Load() != 0 {
		t.Errorf("propose_receiving FÅR INTE skriva — ReportArrivals anropades %d ggr", ds.arrivals.Load())
	}
	dn, _ := tb.Repo.GetDeliveryNote(id)
	if dn.Status != store.DNReceivingProposed {
		t.Errorf("status efter propose = %q, vill ha receiving_proposed", dn.Status)
	}
}

func Test_RegisterArrival_RequiresConfirmation(t *testing.T) {
	tb, ds := setupDeliveryToolbox(t)
	id := seedDeliveryNote(t, tb, &store.DeliveryNote{
		ImageFilename: "dn1.png", DeliveryNoteNumber: "CCF195", Quantity: 6,
		Status: store.DNReceivingProposed, MatchedPOID: 1, MatchedRowID: 11, ProposedQuantity: 6,
	})

	// Utan confirm → MÅSTE vägra och INTE skriva.
	if _, err := tb.Dispatch("monitor_register_arrival", json.RawMessage(`{"id":`+itoa(id)+`}`)); err == nil {
		t.Error("register utan confirm borde ge fel")
	}
	if ds.arrivals.Load() != 0 {
		t.Fatalf("write skedde utan bekräftelse! ReportArrivals anropades %d ggr", ds.arrivals.Load())
	}

	// Med confirm=true → skriver en gång och markerar confirmed.
	if _, err := tb.Dispatch("monitor_register_arrival", json.RawMessage(`{"id":`+itoa(id)+`,"confirm":true}`)); err != nil {
		t.Fatalf("register med confirm: %v", err)
	}
	if ds.arrivals.Load() != 1 {
		t.Errorf("förväntade exakt 1 ReportArrivals, fick %d", ds.arrivals.Load())
	}
	dn, _ := tb.Repo.GetDeliveryNote(id)
	if dn.Status != store.DNReceivingConfirmed {
		t.Errorf("status efter register = %q, vill ha receiving_confirmed", dn.Status)
	}
}

func Test_RegisterArrival_RefusesIfNotProposed(t *testing.T) {
	tb, ds := setupDeliveryToolbox(t)
	id := seedDeliveryNote(t, tb, &store.DeliveryNote{
		ImageFilename: "dn1.png", Status: store.DNUnmatched, // ej föreslaget
	})
	if _, err := tb.Dispatch("monitor_register_arrival", json.RawMessage(`{"id":`+itoa(id)+`,"confirm":true}`)); err == nil {
		t.Error("register på ej-föreslaget dn borde ge fel")
	}
	if ds.arrivals.Load() != 0 {
		t.Errorf("write skedde på ej-föreslaget dn! anrop=%d", ds.arrivals.Load())
	}
}

func Test_ListDeliveryNotes_DefaultsToUnmatched(t *testing.T) {
	tb, _ := setupDeliveryToolbox(t)
	seedDeliveryNote(t, tb, &store.DeliveryNote{ImageFilename: "a.png", Status: store.DNUnmatched})
	seedDeliveryNote(t, tb, &store.DeliveryNote{ImageFilename: "b.png", Status: store.DNReceivingConfirmed})

	res, err := tb.Dispatch("list_delivery_notes", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal([]byte(res.Summary), &resp)
	if resp.Count != 1 {
		t.Errorf("default-listning förväntade 1 (unmatched), fick %d (%s)", resp.Count, res.Summary)
	}
}

// itoa är en liten helper för att slippa importera strconv i varje testrad.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
