package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"cert-renamer/internal/v2/app"
	"cert-renamer/internal/v2/domain"
	v2store "cert-renamer/internal/v2/store"
)

// GET /api/overview är den ENDA datafeeden till UI:t. Alla 64-bitars ID:n
// serialiseras som strängar (JS-precisionsskydd). SSE-eventet "overview" är
// bara en ping — klienten refetchar alltid härifrån.

type certJSON struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	OriginalFilename string `json:"original_filename"`
	StoredName       string `json:"stored_name"`
	EmailSubject     string `json:"email_subject"`
	EmailFrom        string `json:"email_from"`
	EmailDate        string `json:"email_date"`

	// Rå extraktion
	CertType          string   `json:"cert_type"`
	Charge            string   `json:"charge"`
	Material          string   `json:"material"`
	EnStandardPresent bool     `json:"en_standard_present"`
	IsEnglish         bool     `json:"is_english"`
	ProductForm       string   `json:"product_form"`
	Dimensions        string   `json:"dimensions"`
	CountryOfOrigin   string   `json:"country_of_origin"`
	BNumbers          []string `json:"b_numbers"`
	Confidence        string   `json:"confidence"`
	Issues            []string `json:"issues"`

	// Kolumnkalibrerad extraktion (Task 1-3): slag-, kemi- och märkningsdata.
	// Nullable-tal förblir *float64 rakt igenom så nil serialiseras som JSON
	// null (inte 0) — 0 betyder "ej angivet" bara för ImpactEnergyJ, som inte
	// är nullable i domänmodellen.
	IsLegible            bool     `json:"is_legible"`
	IsUnaltered          bool     `json:"is_unaltered"`
	NormSystem           string   `json:"norm_system"`
	ImpactTempC          *float64 `json:"impact_temp_c"`
	ImpactEnergyJ        float64  `json:"impact_energy_j"`
	NormEdition          string   `json:"norm_edition"`
	PedDirective         string   `json:"ped_directive"`
	Cev                  *float64 `json:"cev"`
	CarbonPct            *float64 `json:"carbon_pct"`
	PPct                 *float64 `json:"p_pct"`
	SPct                 *float64 `json:"s_pct"`
	HasBendTest          bool     `json:"has_bend_test"`
	HasIntergranularTest bool     `json:"has_intergranular_test"`
	HasStampPhoto        bool     `json:"has_stamp_photo"`
	MinTemperatureC      *float64 `json:"min_temperature_c"`
	DeliveryCondition    string   `json:"delivery_condition"`

	// Rättelser + effektivt
	Corrected         map[string]string   `json:"corrected"` // fält → värde ('' = ej rättad)
	Effective         map[string]string   `json:"effective"`
	EffectiveBNumbers []string            `json:"effective_b_numbers"`
	CorrectionLog     []domain.Correction `json:"correction_log"`

	// Levande namn + livscykel
	NameOverride     string `json:"name_override"`
	ProposedFilename string `json:"proposed_filename"`
	FinalFilename    string `json:"final_filename"`
	OutputPath       string `json:"output_path"`
	SavedAt          string `json:"saved_at"`
	ReceivedAt       string `json:"received_at"`

	Notes []*v2store.Note `json:"notes"`
}

type linkJSON struct {
	ID            string `json:"id"`
	CertID        string `json:"cert_id"`
	DeliveryRowID string `json:"delivery_row_id"`
	OrderNumber   string `json:"order_number"`
	Status        string `json:"status"`
	MatchSource   string `json:"match_source"`

	RequiredMaterial    string `json:"required_material"`
	RequiredCert        string `json:"required_cert"`
	OurMaterial         string `json:"our_material"`
	MaterialOK          string `json:"material_ok"`
	RequiredProductForm string `json:"required_product_form"`
	ProductFormOK       string `json:"product_form_ok"`
	AINotes             string `json:"ai_notes"`

	// Regelrätta domar (Task 9) — BERÄKNAS FÄRSKT per render ur radens krav +
	// certets kolumner (domain.*Verdict), persisteras aldrig. Tomma ("") när
	// ingen cert är kopplad (linkJSONOf utan certkontext, t.ex. handleLink-ACK).
	EnglishVerdict  string `json:"english_verdict"`
	CertTypeVerdict string `json:"cert_type_verdict"`
	ImpactVerdict   string `json:"impact_verdict"`

	Cert *certJSON `json:"cert,omitempty"`
}

type rowJSON struct {
	DeliveryRowID    string  `json:"delivery_row_id"`
	OrderNumber      string  `json:"order_number"`
	SupplierName     string  `json:"supplier_name"`
	PartNumber       string  `json:"part_number"`
	Description      string  `json:"description"`
	ExtraDescription string  `json:"extra_description"`
	PlannedQty       float64 `json:"planned_qty"`
	DeliveryDate     string  `json:"delivery_date"`
	CertRequired     bool    `json:"cert_required"`
	Delivered        bool    `json:"delivered"`
	InMonitor        bool    `json:"in_monitor"`

	// Cert-bärande fält (Task 6): rå kravtext + artikeldata från Monitor.
	// AI-parsning kommer i en senare task — här bärs texterna bara oförädlade
	// till UI:t. Tomma strängar utelämnas inte här; JS filtrerar vid render.
	ReceivingMessage               string             `json:"receiving_message"`
	ReceivingInspectionInstruction string             `json:"receiving_inspection_instruction"`
	RowGoodsLabel                  string             `json:"row_goods_label"`
	RowNotes                       string             `json:"row_notes"`
	SupplierDrawingNumber          string             `json:"supplier_drawing_number"`
	SupplierRevisionNumber         string             `json:"supplier_revision_number"`
	FreeText                       string             `json:"free_text"`
	OrderGoodsLabel                string             `json:"order_goods_label"`
	ExternalComment                string             `json:"external_comment"`
	BusinessContactOrderNumber     string             `json:"business_contact_order_number"`
	AlloyCode                      string             `json:"alloy_code"`
	AlloyDescription               string             `json:"alloy_description"`
	PartReceivingInstruction       string             `json:"part_receiving_instruction"`
	PartPurchaseComment            string             `json:"part_purchase_comment"`
	PartComment                    string             `json:"part_comment"`
	PartLength                     float64            `json:"part_length"`
	PartWidth                      float64            `json:"part_width"`
	PartHeight                     float64            `json:"part_height"`
	WeightPerUnit                  float64            `json:"weight_per_unit"`
	GoodsType                      string             `json:"goods_type"`
	CategoryString                 string             `json:"category_string"`
	ExtraFieldsRaw                 string             `json:"extra_fields_raw"`
	Hyperlinks                     []domain.Hyperlink `json:"hyperlinks"`
	DrawingNumbers                 string             `json:"drawing_numbers"`

	// AI-tolkade krav (Task 8/9): den beställda sanningen, exponerad så UI:ts
	// Krav-kolumn kan visas ÄVEN utan länkat cert. Tomma fält = inget
	// uttryckligt krav framgick (samma "gissa aldrig"-princip).
	ReqMaterial    string `json:"req_material"`
	ReqEnNorm      string `json:"req_en_norm"`
	ReqCertType    string `json:"req_cert_type"`
	ReqEnglish     bool   `json:"req_english"`
	ReqProductForm string `json:"req_product_form"`
	ReqDimensions  string `json:"req_dimensions"`
	ReqImpact      string `json:"req_impact"`
	ReqNotes       string `json:"req_notes"`

	Links []linkJSON      `json:"links"`
	Notes []*v2store.Note `json:"notes"`
}

type orderGroupJSON struct {
	OrderNumber  string    `json:"order_number"`
	SupplierName string    `json:"supplier_name"`
	Rows         []rowJSON `json:"rows"`
}

type suggestionJSON struct {
	LinkID        string `json:"link_id"`
	DeliveryRowID string `json:"delivery_row_id"`
	OrderNumber   string `json:"order_number"`
	PartNumber    string `json:"part_number"`
	Description   string `json:"description"`
	DeliveryDate  string `json:"delivery_date"`
	MatchSource   string `json:"match_source"`
}

type unlinkedJSON struct {
	certJSON
	Suggestions []suggestionJSON `json:"suggestions"`
}

type overviewJSON struct {
	GeneratedAt   string           `json:"generated_at"`
	Running       bool             `json:"running"`
	LastSync      string           `json:"last_sync"`
	Orders        []orderGroupJSON `json:"orders"`
	UnlinkedCerts []unlinkedJSON   `json:"unlinked_certs"`
	SavedRecent   []certJSON       `json:"saved_recent"`
	Archived      []certJSON       `json:"archived"`
	Tasks         []*v2store.Task  `json:"tasks"`
	Errors        []string         `json:"errors"`
}

func idStr(v int64) string { return strconv.FormatInt(v, 10) }

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func linkJSONOf(l *domain.Link) linkJSON {
	return linkJSON{
		ID: idStr(l.ID), CertID: idStr(l.CertID), DeliveryRowID: idStr(l.DeliveryRowID),
		OrderNumber: l.OrderNumber, Status: string(l.Status), MatchSource: l.MatchSource,
		RequiredMaterial: l.RequiredMaterial, RequiredCert: l.RequiredCert,
		OurMaterial: l.OurMaterial, MaterialOK: l.MaterialOK,
		RequiredProductForm: l.RequiredProductForm, ProductFormOK: l.ProductFormOK,
		AINotes: l.AINotes,
	}
}

// certJSON bygger UI-vyn av ett cert ur app.CertView + noteringar.
func (s *Server) certJSON(ctx context.Context, view *app.CertView) certJSON {
	c := view.Cert
	notes, _ := s.Repo.ListNotes(ctx, "cert", c.ID)
	if notes == nil {
		notes = []*v2store.Note{}
	}
	return certJSON{
		ID: idStr(c.ID), Status: string(c.Status),
		OriginalFilename: c.OriginalFilename, StoredName: c.StoredName,
		EmailSubject: c.EmailSubject, EmailFrom: c.EmailFrom, EmailDate: c.EmailDate,
		CertType: c.CertType, Charge: c.Charge, Material: c.Material,
		EnStandardPresent: c.EnStandardPresent, IsEnglish: c.IsEnglish,
		ProductForm: c.ProductForm, Dimensions: c.Dimensions, CountryOfOrigin: c.CountryOfOrigin,
		BNumbers: orEmpty(c.BNumbers), Confidence: c.Confidence, Issues: orEmpty(c.Issues),
		IsLegible: c.IsLegible, IsUnaltered: c.IsUnaltered,
		NormSystem: c.NormSystem, ImpactTempC: c.ImpactTempC, ImpactEnergyJ: c.ImpactEnergyJ,
		NormEdition: c.NormEdition, PedDirective: c.PedDirective,
		Cev: c.Cev, CarbonPct: c.CarbonPct, PPct: c.PPct, SPct: c.SPct,
		HasBendTest: c.HasBendTest, HasIntergranularTest: c.HasIntergranularTest, HasStampPhoto: c.HasStampPhoto,
		MinTemperatureC: c.MinTemperatureC, DeliveryCondition: c.DeliveryCondition,
		Corrected: map[string]string{
			"charge": c.CorrectedCharge, "material": c.CorrectedMaterial,
			"product_form": c.CorrectedProductForm, "dimensions": c.CorrectedDimensions,
			"cert_type": c.CorrectedCertType,
		},
		Effective: map[string]string{
			"charge": c.EffectiveCharge(), "material": c.EffectiveMaterial(),
			"product_form": c.EffectiveProductForm(), "dimensions": c.EffectiveDimensions(),
			"cert_type": c.EffectiveCertType(),
		},
		EffectiveBNumbers: orEmpty(c.EffectiveBNumbers()),
		CorrectionLog:     orEmptyLog(c.CorrectionLog),
		NameOverride:      c.NameOverride,
		ProposedFilename:  view.ProposedFilename,
		FinalFilename:     c.FinalFilename, OutputPath: c.OutputPath,
		SavedAt: c.SavedAt, ReceivedAt: c.ReceivedAt,
		Notes: notes,
	}
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func orEmptyLog(v []domain.Correction) []domain.Correction {
	if v == nil {
		return []domain.Correction{}
	}
	return v
}

func orEmptyHyperlinks(v []domain.Hyperlink) []domain.Hyperlink {
	if v == nil {
		return []domain.Hyperlink{}
	}
	return v
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := overviewJSON{
		GeneratedAt: nowRFC3339(),
		Running:     s.workerRunning(),
		LastSync:    s.App.State(ctx, "last_sync"),
	}

	// Cert-vyer byggs en gång per cert och återanvänds i länkar/sektioner.
	certViews := map[int64]*app.CertView{}
	getView := func(certID int64) *app.CertView {
		if v, ok := certViews[certID]; ok {
			return v
		}
		v, err := s.App.GetCertView(ctx, certID)
		if err != nil {
			return nil
		}
		certViews[certID] = v
		return v
	}

	// Ordrar: alla kända rader grupperade per ordernummer.
	rows, err := s.Repo.ListOrderRows(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	groups := map[string]*orderGroupJSON{}
	var groupOrder []string
	for _, row := range rows {
		rj := rowJSON{
			DeliveryRowID: idStr(row.DeliveryRowID), OrderNumber: row.OrderNumber,
			SupplierName: row.SupplierName, PartNumber: row.PartNumber,
			Description: row.Description, ExtraDescription: row.ExtraDescription,
			PlannedQty: row.PlannedQty, DeliveryDate: row.DeliveryDate,
			CertRequired: row.CertRequired, Delivered: row.Delivered, InMonitor: row.InMonitor,
			ReceivingMessage: row.ReceivingMessage, ReceivingInspectionInstruction: row.ReceivingInspectionInstruction,
			RowGoodsLabel: row.RowGoodsLabel, RowNotes: row.RowNotes,
			SupplierDrawingNumber: row.SupplierDrawingNumber, SupplierRevisionNumber: row.SupplierRevisionNumber,
			FreeText:        row.FreeText,
			OrderGoodsLabel: row.OrderGoodsLabel, ExternalComment: row.ExternalComment,
			BusinessContactOrderNumber: row.BusinessContactOrderNumber,
			AlloyCode:                  row.AlloyCode, AlloyDescription: row.AlloyDescription,
			PartReceivingInstruction: row.PartReceivingInstruction, PartPurchaseComment: row.PartPurchaseComment,
			PartComment: row.PartComment,
			PartLength:  row.PartLength, PartWidth: row.PartWidth, PartHeight: row.PartHeight,
			WeightPerUnit: row.WeightPerUnit,
			GoodsType:     row.GoodsType, CategoryString: row.CategoryString,
			ExtraFieldsRaw: row.ExtraFieldsRaw,
			Hyperlinks:     orEmptyHyperlinks(row.Hyperlinks),
			DrawingNumbers: row.DrawingNumbers,
			ReqMaterial:    row.Req.Material, ReqEnNorm: row.Req.EnNorm,
			ReqCertType: row.Req.CertType, ReqEnglish: row.Req.English,
			ReqProductForm: row.Req.ProductForm, ReqDimensions: row.Req.Dimensions,
			ReqImpact: row.Req.Impact, ReqNotes: row.Req.Notes,
			Links: []linkJSON{},
		}
		if notes, _ := s.Repo.ListNotes(ctx, "order_row", row.DeliveryRowID); notes != nil {
			rj.Notes = notes
		} else {
			rj.Notes = []*v2store.Note{}
		}
		links, _ := s.Repo.ListLinksForRow(ctx, row.DeliveryRowID)
		for _, l := range links {
			if l.Status == domain.LinkAvfardad {
				continue
			}
			lj := linkJSONOf(l)
			if view := getView(l.CertID); view != nil {
				cj := s.certJSON(ctx, view)
				lj.Cert = &cj
				// Task 9: regelrätta domar beräknas FÄRSKT här ur radens krav
				// + certets kolumner — aldrig cachade/persisterade.
				cert := view.Cert
				// FIX 3a: Krav-cellen i UI:t fallbackar cert-typ till AI-domens
				// required_cert (lj.RequiredCert) när radens parsade cert-typ är tom
				// — döm med SAMMA källa, annars kan ✓/⚠ glida isär mot det som visas.
				reqCertType := row.Req.CertType
				if reqCertType == "" {
					reqCertType = lj.RequiredCert
				}
				lj.EnglishVerdict = domain.EnglishVerdict(row.Req.English, cert.IsEnglish)
				lj.CertTypeVerdict = domain.CertTypeVerdict(reqCertType, cert.EffectiveCertType())
				lj.ImpactVerdict = domain.ImpactVerdict(row.Req.Impact, cert.ImpactEnergyJ, cert.ImpactTempC)
			}
			rj.Links = append(rj.Links, lj)
		}
		g, ok := groups[row.OrderNumber]
		if !ok {
			g = &orderGroupJSON{OrderNumber: row.OrderNumber, SupplierName: row.SupplierName}
			groups[row.OrderNumber] = g
			groupOrder = append(groupOrder, row.OrderNumber)
		}
		g.Rows = append(g.Rows, rj)
	}
	// Grupperna sorterade på tidigaste leveransdatum (tomma datum sist).
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return groupSortKey(groups[groupOrder[i]]) < groupSortKey(groups[groupOrder[j]])
	})
	resp.Orders = make([]orderGroupJSON, 0, len(groupOrder))
	for _, key := range groupOrder {
		resp.Orders = append(resp.Orders, *groups[key])
	}

	// Okopplade cert: levande utan bekräftad länk; förslag som chips.
	living, err := s.Repo.ListCerts(ctx, domain.CertMottagen)
	if err != nil {
		writeError(w, err)
		return
	}
	resp.UnlinkedCerts = []unlinkedJSON{}
	for _, c := range living {
		view := getView(c.ID)
		if view == nil || len(view.ConfirmedOrders) > 0 {
			continue
		}
		u := unlinkedJSON{certJSON: s.certJSON(ctx, view), Suggestions: []suggestionJSON{}}
		for _, l := range view.Links {
			if l.Status != domain.LinkForeslagen {
				continue
			}
			sug := suggestionJSON{
				LinkID: idStr(l.ID), DeliveryRowID: idStr(l.DeliveryRowID),
				OrderNumber: l.OrderNumber, MatchSource: l.MatchSource,
			}
			if l.DeliveryRowID != 0 {
				if row, err := s.Repo.GetOrderRow(ctx, l.DeliveryRowID); err == nil {
					sug.PartNumber, sug.Description, sug.DeliveryDate = row.PartNumber, row.Description, row.DeliveryDate
				}
			}
			u.Suggestions = append(u.Suggestions, sug)
		}
		resp.UnlinkedCerts = append(resp.UnlinkedCerts, u)
	}

	// Nyligen sparade + arkiverade (frysta vyer).
	resp.SavedRecent = s.certList(ctx, getView, domain.CertSparad, 20)
	resp.Archived = s.certList(ctx, getView, domain.CertArkiverad, 20)

	if tasks, _ := s.Repo.ListTasks(ctx, "open"); tasks != nil {
		resp.Tasks = tasks
	} else {
		resp.Tasks = []*v2store.Task{}
	}
	if errs, _ := s.Repo.ListEmailErrors(ctx); errs != nil {
		resp.Errors = errs
	} else {
		resp.Errors = []string{}
	}

	writeJSON(w, resp)
}

func (s *Server) certList(ctx context.Context, getView func(int64) *app.CertView, status domain.CertStatus, limit int) []certJSON {
	certs, err := s.Repo.ListCerts(ctx, status)
	if err != nil {
		return []certJSON{}
	}
	out := make([]certJSON, 0, min(limit, len(certs)))
	for _, c := range certs {
		if len(out) >= limit {
			break
		}
		if view := getView(c.ID); view != nil {
			out = append(out, s.certJSON(ctx, view))
		}
	}
	return out
}

func groupSortKey(g *orderGroupJSON) string {
	key := "9999-99-99"
	for _, r := range g.Rows {
		if r.DeliveryDate != "" && r.DeliveryDate < key {
			key = r.DeliveryDate
		}
	}
	return key + "|" + g.OrderNumber
}
