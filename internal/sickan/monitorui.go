package sickan

import (
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// ---------------------------------------------------------------------------
// Monitor UI-automation-verktyg. När Monitors skriv-API inte är licensierat
// (403 "Monitor.API is not available for this system") styr vi i stället
// skrivbordsklienten: öppna rutinen via monitor://-länk, fyll i ordernummer,
// hämta listan (Ctrl+L) och — efter uttrycklig bekräftelse — spara (Ctrl+S).
// Själva OS-anropet ligger i server-paketet (Notifier.DriveMonitorRoutine).
// ---------------------------------------------------------------------------

var monitorUIReportArrivalTool = anthropic.ToolParam{
	Name:        "monitor_ui_report_arrival",
	Description: anthropic.String("DET ENDA sättet att registrera inleverans/mottagningskontroll (Monitors skriv-API är inte licensierat). Styr Monitor-SKRIVBORDSKLIENTEN: öppnar rutinen, fyller i ordernumret och hämtar listan (Ctrl+L). Utan confirm=true returneras bara en FÖRHANDSVISNING. Med confirm=true körs öppning+ifyllnad+hämtning. save=true skickar dessutom Ctrl+S (spara) — bara efter användarens uttryckliga ja OCH om auto-spara är påslaget i inställningarna. En order i taget."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number":     map[string]any{"type": "string", "description": "Inköpsorderns nummer, t.ex. \"B128756\". Kan utelämnas om delivery_note_id anges."},
			"routine":          map[string]any{"type": "string", "enum": []string{"report_arrival", "inspection"}, "description": "Vilken rutin: report_arrival (Rapportera inleverans) eller inspection (Mottagningskontroll). Default report_arrival."},
			"delivery_note_id": map[string]any{"type": "integer", "description": "Valfritt: hämta ordernumret från en uppladdad följesedel i stället för order_number."},
			"confirm":          map[string]any{"type": "boolean", "description": "Måste vara true för att faktiskt styra klienten. Utan/false = förhandsvisning."},
			"save":             map[string]any{"type": "boolean", "description": "Om true skickas även Ctrl+S (spara/registrera). Bara efter uttryckligt ja; spärras dessutom av inställningen auto-spara."},
		},
		Required: []string{},
	},
}

func (tb *Toolbox) monitorUIReportArrival(input json.RawMessage) (string, error) {
	var args struct {
		OrderNumber    string `json:"order_number"`
		Routine        string `json:"routine"`
		DeliveryNoteID int64  `json:"delivery_note_id"`
		Confirm        bool   `json:"confirm"`
		Save           bool   `json:"save"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}

	routine := args.Routine
	if routine == "" {
		routine = "report_arrival"
	}
	if routine != "report_arrival" && routine != "inspection" {
		return "", fmt.Errorf("okänd routine %q — använd report_arrival eller inspection", routine)
	}

	order := args.OrderNumber
	if order == "" && args.DeliveryNoteID > 0 && tb.Repo != nil {
		dn, err := tb.Repo.GetDeliveryNote(args.DeliveryNoteID)
		if err != nil {
			return "", fmt.Errorf("följesedel %d finns inte: %w", args.DeliveryNoteID, err)
		}
		order = dn.OrderNumber
	}
	if order == "" {
		return "", fmt.Errorf("order_number krävs (eller delivery_note_id med ett ordernummer)")
	}

	routineName := "Rapportera inleverans"
	link := tb.Cfg.MonitorLinkReportArrival
	if routine == "inspection" {
		routineName = "Mottagningskontroll"
		link = tb.Cfg.MonitorLinkInspection
	}
	if link == "" {
		return "", fmt.Errorf("ingen monitor://-länk konfigurerad för %q — fyll i den under ⚙️ Inställningar → Monitor", routineName)
	}

	// GATE: utan confirm=true → förhandsvisa, gör inget.
	if !args.Confirm {
		out, _ := json.Marshal(map[string]any{
			"preview":      true,
			"routine":      routineName,
			"order_number": order,
			"will_save":    args.Save && tb.Cfg.MonitorUIAutoSave,
			"auto_save_on": tb.Cfg.MonitorUIAutoSave,
			"note":         fmt.Sprintf("FÖRSLAG — öppnar Monitor-rutinen %q, fyller i order %s i båda fälten och hämtar listan (Ctrl+L). INGET sparas. Bekräfta med confirm=true; lägg till save=true för att även spara (Ctrl+S).", routineName, order),
		})
		return string(out), nil
	}

	if err := tb.N.DriveMonitorRoutine(routine, order, args.Save); err != nil {
		return "", fmt.Errorf("kunde inte styra Monitor-klienten: %w", err)
	}

	saved := args.Save && tb.Cfg.MonitorUIAutoSave
	if saved {
		tb.N.Logf("🤖 Sickan: Monitor-rutin %q — order %s ifylld, hämtad OCH sparad (Ctrl+S)", routineName, order)
	} else {
		tb.N.Logf("🤖 Sickan: Monitor-rutin %q — order %s ifylld och hämtad (Ctrl+L); inte sparad", routineName, order)
	}

	resp := map[string]any{
		"ok":           true,
		"routine":      routineName,
		"order_number": order,
		"saved":        saved,
	}
	if args.Save && !tb.Cfg.MonitorUIAutoSave {
		resp["note"] = "Hämtning klar i Monitor. Ctrl+S skickades INTE eftersom auto-spara är avstängt — granska listan och tryck Ctrl+S själv, eller slå på auto-spara i Inställningar."
	} else if !saved {
		resp["note"] = "Listan hämtad i Monitor — granska och tryck Ctrl+S själv, eller kör igen med save=true för att spara."
	}
	out, _ := json.Marshal(resp)
	return string(out), nil
}
