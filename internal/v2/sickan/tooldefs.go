package sickan

import "github.com/anthropics/anthropic-sdk-go"

// Tool-defs som skickas till Claude. Sista entryn får CacheControl så hela
// tools-arrayen + system cachas (~10 % input-pris från tur två).
func ToolDefs(readOnly bool) []anthropic.ToolUnionParam {
	all := []*anthropic.ToolParam{
		&listOverviewTool, &getCertTool, &updateCertTool, &linkCertTool,
		&unlinkCertTool, &proposeNameTool, &readPdfTool,
		&monitorFindPurchaseOrderTool, &monitorFindSupplierTool, &monitorLookupChargeTool,
		&addNoteTool, &getNotesTool, &markDeliveredTool, &composeDeviationMailTool,
		&rememberRuleTool, &listRulesTool, &addTaskTool, &listTasksTool, &completeTaskTool,
	}
	out := make([]anthropic.ToolUnionParam, 0, len(all))
	for _, t := range all {
		if readOnly && mutatingTools[t.Name] {
			continue
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: t})
	}
	lastCopy := *out[len(out)-1].OfTool
	lastCopy.CacheControl = anthropic.NewCacheControlEphemeralParam()
	out[len(out)-1] = anthropic.ToolUnionParam{OfTool: &lastCopy}
	return out
}

func str(s string) map[string]any     { return map[string]any{"type": "string", "description": s} }
func integer(s string) map[string]any { return map[string]any{"type": "integer", "description": s} }

var listOverviewTool = anthropic.ToolParam{
	Name:        "list_overview",
	Description: anthropic.String("Kompakt läge över allt: orderrader (B-nummer, artikel, datum, cert-krav, leveransstatus) med kopplade cert och domar, plus okopplade cert. Använd alltid detta först."),
	InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
}

var getCertTool = anthropic.ToolParam{
	Name:        "get_cert",
	Description: anthropic.String("Full detalj för ett cert: rå extraktion, rättelser, effektiva värden, länkar, rättelselogg och det levande namnförslaget."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"cert_id": integer("Certets id (från list_overview).")},
		Required:   []string{"cert_id"},
	},
}

var updateCertTool = anthropic.ToolParam{
	Name:        "update_cert",
	Description: anthropic.String("Sätter en rättelse på ett LEVANDE cert. Fält: charge, material, product_form, dimensions, cert_type, b_numbers (kommaseparerad lista). Rättelsen loggas och det levande namnet räknas om. Kräver Robs ja om det inte var exakt det Rob bad om."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"cert_id": integer("Certets id."),
			"field":   str("Ett av: charge, material, product_form, dimensions, cert_type, b_numbers."),
			"value":   str("Nya värdet ('' rensar rättelsen tillbaka till rå extraktion)."),
		},
		Required: []string{"cert_id", "field", "value"},
	},
}

var linkCertTool = anthropic.ToolParam{
	Name:        "link_cert",
	Description: anthropic.String("Kopplar/bekräftar ett cert till ett B-nummer (bekräftad länk). Ange delivery_row_id (exakt rad) ELLER order_number (fri koppling, även om raden inte finns i Monitor-fönstret än). Kräver Robs ja."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"cert_id":         integer("Certets id."),
			"delivery_row_id": integer("Orderradens id (valfri om order_number ges)."),
			"order_number":    str("B-nummer, t.ex. \"B127575\" (valfri om delivery_row_id ges)."),
		},
		Required: []string{"cert_id"},
	},
}

var unlinkCertTool = anthropic.ToolParam{
	Name:        "unlink_cert",
	Description: anthropic.String("Kopplar loss/avvisar en länk (foreslagen eller bekraftad). Kräver Robs ja."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"link_id": integer("Länkens id.")},
		Required:   []string{"link_id"},
	},
}

var proposeNameTool = anthropic.ToolParam{
	Name:        "propose_name",
	Description: anthropic.String("Returnerar certets levande namnförslag (och vilka B-nummer det bygger på). Med override sätts ett manuellt namn ('' rensar tillbaka till beräknat)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"cert_id":  integer("Certets id."),
			"override": str("Valfritt: manuellt filnamn, eller '' för att rensa overriden."),
		},
		Required: []string{"cert_id"},
	},
}

var readPdfTool = anthropic.ToolParam{
	Name:        "read_pdf",
	Description: anthropic.String("Bifogar certets original-PDF från certlagret så du kan läsa innehållet (text + stämplar)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"cert_id": integer("Certets id.")},
		Required:   []string{"cert_id"},
	},
}

var monitorFindPurchaseOrderTool = anthropic.ToolParam{
	Name:        "monitor_find_purchase_order",
	Description: anthropic.String("Slår upp en inköpsorder i Monitor ERP via ordernummer: order, leverantör och orderrader (read-only)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"order_number": str("Inköpsorderns nummer, t.ex. \"B127196\".")},
		Required:   []string{"order_number"},
	},
}

var monitorFindSupplierTool = anthropic.ToolParam{
	Name:        "monitor_find_supplier",
	Description: anthropic.String("Söker leverantörer i Monitor ERP på kod (exakt) eller namn (delsträng)."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"term": str("Leverantörskod eller del av namnet.")},
		Required:   []string{"term"},
	},
}

var monitorLookupChargeTool = anthropic.ToolParam{
	Name:        "monitor_lookup_charge",
	Description: anthropic.String("Slår upp en charge i Monitors ProductRecords → kandidatorder/artikel/leverantör. FÖRESLÅR koppling — tillämpa via link_cert efter Robs ja. Ange cert_id (använder certets effektiva charge) eller charge direkt."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"cert_id": integer("Certets id (valfri om charge ges)."),
			"charge":  str("Charge/heat att slå upp (valfri om cert_id ges)."),
		},
	},
}

var addNoteTool = anthropic.ToolParam{
	Name:        "add_note",
	Description: anthropic.String("Lägger en notering på en orderrad eller ett cert — det gemensamma minnet med Rob."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"kind":         str("'order_row' eller 'cert'."),
			"ref_id":       integer("delivery_row_id respektive cert_id."),
			"order_number": str("B-nummer för kontext (valfri)."),
			"text":         str("Noteringstexten."),
		},
		Required: []string{"kind", "ref_id", "text"},
	},
}

var getNotesTool = anthropic.ToolParam{
	Name:        "get_notes",
	Description: anthropic.String("Läser noteringarna på en orderrad eller ett cert. Läs innan du föreslår något."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"kind":   str("'order_row' eller 'cert'."),
			"ref_id": integer("delivery_row_id respektive cert_id."),
		},
		Required: []string{"kind", "ref_id"},
	},
}

var markDeliveredTool = anthropic.ToolParam{
	Name:        "mark_delivered",
	Description: anthropic.String("Markerar orderrader som levererade i appen (lokal bokföring — inte Monitor). Kräver Robs ja."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"delivery_row_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"},
				"description": "Orderradernas id:n."},
			"delivered": map[string]any{"type": "boolean", "description": "false för att ångra (default true)."},
		},
		Required: []string{"delivery_row_ids"},
	},
}

var composeDeviationMailTool = anthropic.ToolParam{
	Name:        "compose_deviation_mail",
	Description: anthropic.String("Bygger ett färdigt mailto-utkast för en order: 'rest' (ej inlevererade positioner) eller 'cert_missing' (cert-krävande rader utan bekräftat cert). Skickar INGET."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"order_number": str("B-nummer."),
			"kind":         str("'rest' eller 'cert_missing'."),
		},
		Required: []string{"order_number", "kind"},
	},
}

var rememberRuleTool = anthropic.ToolParam{
	Name:        "remember_rule",
	Description: anthropic.String("Sparar en arbetsregel du lärt dig av Rob. Spara DIREKT när Rob uttrycker ett arbetssätt — fråga inte."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"text": str("Regeln, kort och generell.")},
		Required:   []string{"text"},
	},
}

var listRulesTool = anthropic.ToolParam{
	Name:        "list_rules",
	Description: anthropic.String("Listar dina inlärda arbetsregler."),
	InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
}

var addTaskTool = anthropic.ToolParam{
	Name:        "add_task",
	Description: anthropic.String("Lägger till en punkt i att-göra-listan ('glöm inte att X')."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"text":         str("Vad som ska göras."),
			"due_date":     str("YYYY-MM-DD (valfri)."),
			"order_number": str("B-nummer för kontext (valfri)."),
		},
		Required: []string{"text"},
	},
}

var listTasksTool = anthropic.ToolParam{
	Name:        "list_tasks",
	Description: anthropic.String("Listar att-göra-punkterna."),
	InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
}

var completeTaskTool = anthropic.ToolParam{
	Name:        "complete_task",
	Description: anthropic.String("Markerar en task som klar. Kräver Robs ja om det inte är din egen task."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"id": integer("Taskens id.")},
		Required:   []string{"id"},
	},
}
