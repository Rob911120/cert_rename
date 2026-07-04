package main

// approved-PDF:er vi cyklar igenom som bilage-källor.
// Lever i ~/Projects/cert-renamer/inbox/approved/.
var approvedPdfs = []string{
	"3148554B-fyrkantsror-250x150x8-S355-B127093.pdf",
	"610042-coil-5-1.4307-B127340.pdf",
	"610042-plat-5-1.4307-B127340.pdf",
	"612037-plat-5-1.4404-B127340.pdf",
	"68923E4-plat-50-S355-B127559.pdf",
	"70703-plat-16-S690QL-B127562.pdf",
	"8646063334-rundror-100x80x6-S355-B127196.pdf",
	"87210-plat-15-S690-B127562.pdf",
}

// pdfRef pekar på en PDF i approvedPdfs (index) och säger vad bilagan
// ska heta i mailet (kan vara annat än ursprunget för att testa
// B-nummer-extraktion ur filnamn).
type pdfRef struct {
	srcIdx     int    // index i approvedPdfs, eller -1 = corrupt placeholder
	attachName string // filename i Content-Disposition
}

// Scenario är ett enskilt eml-fall.
type Scenario struct {
	Name    string // utan .eml
	Subject string
	// SubjectEncode == true → Subject base64-encodas som =?utf-8?B?...?=
	SubjectEncode bool
	From          string
	To            string
	Body          string
	// LargeBodyPad → om >0, bädda på N KB lorem efter Body
	LargeBodyPad int
	PDFs         []pdfRef
	// TextAttach → ren text-bilaga (.txt) istället för/utöver PDF
	TextAttach string
	// AltOnly → använd multipart/alternative (text+html) utan mixed,
	// PDF:er ignoreras då.
	AltOnly bool
}

// 20 scenarier — se plan-fil för detaljer.
func scenarios() []Scenario {
	return []Scenario{
		// === Lyckliga vägen → queue/ (8) ===
		{
			Name:    "01-cert-bnr-i-subject-orderid-i-filnamn",
			Subject: "Certifikat B100001",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Hej, bifogat följer cert för B100001.\n\nMvh\nLeverantören",
			PDFs:    []pdfRef{{srcIdx: 0, attachName: "11359.pdf"}},
		},
		{
			Name:    "02-cert-bnr-bara-i-subject",
			Subject: "FW: Certifikat B100002",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Se bifogat.\n",
			PDFs:    []pdfRef{{srcIdx: 1, attachName: "SWE_377313.PDF"}},
		},
		{
			Name:    "03-cert-utan-bnr-i-mejl-bara-orderid",
			Subject: "Certifikat",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Hej, se bifogad.\n",
			PDFs:    []pdfRef{{srcIdx: 2, attachName: "68923 E4.pdf"}},
		},
		{
			Name:    "04-cert-bnr-bara-i-body-dim-i-filnamn",
			Subject: "Certifikat",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Hej, bifogat cert för order B100004.\n",
			PDFs:    []pdfRef{{srcIdx: 3, attachName: "100x80x6,3 864606334.pdf"}},
		},
		{
			Name:    "05-cert-flera-bnummer-generiskt-filnamn",
			Subject: "Certifikat B100010 + B100011",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Två B-nummer: B100010 och B100011.\n",
			PDFs:    []pdfRef{{srcIdx: 4, attachName: "Certifikat.pdf"}},
		},
		{
			Name:          "06-cert-encoded-subject-scanner-filnamn",
			Subject:       "Certifikat B100006 — för leverans åäö",
			SubjectEncode: true,
			From:          "certifikat@stalleverantor.se",
			To:            "robin.sundsten@fa-tec.se",
			Body:          "Bifogat cert.\n",
			PDFs:          []pdfRef{{srcIdx: 5, attachName: "Document1.pdf"}},
		},
		{
			Name:    "07-cert-fw-fw-outlook-duplicate-suffix",
			Subject: "FW: FW: Certifikat B100007",
			From:    "intern@fa-tec.se",
			To:      "robin.sundsten@fa-tec.se",
			Body: "Vidarebefordrar.\n\n" +
				"-----Original Message-----\n" +
				"From: certifikat@stalleverantor.se\n" +
				"Subject: Certifikat B100007\n\n" +
				"Hej, här kommer cert.\n",
			PDFs: []pdfRef{{srcIdx: 6, attachName: "cert (1).pdf"}},
		},
		{
			Name:    "08-tva-cert-blandade-filnamn",
			Subject: "Certifikat B100008 + B100009",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Två cert.\n",
			PDFs: []pdfRef{
				{srcIdx: 7, attachName: "11358.pdf"},
				{srcIdx: 0, attachName: "Plat 15mm S690 87210.pdf"},
			},
		},

		// === Arkiveras → arkiverat/ (4) ===
		{
			Name:    "09-marknadsforing-ingen-pdf",
			Subject: "Säkra lyft kräver rätt utrustning",
			From:    "marketing@example.com",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Vi har specialerbjudande på lyftöglor denna månad! Klicka här för mer.\n",
		},
		{
			Name:       "10-text-bilaga-ingen-pdf",
			Subject:    "Leveransspecifikation",
			From:       "leverantor@example.com",
			To:         "robin.sundsten@fa-tec.se",
			Body:       "Se bifogad textfil.\n",
			TextAttach: "Leveransnr: 12345\nGods: 50 st kartonger\n",
		},
		{
			Name:    "11-fakturakopia-med-pdf",
			Subject: "Fakturakopia 2026-04",
			From:    "ekonomi@example.com",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Bifogat hittar du månadens fakturakopia. Vänligen betala inom 30 dagar.\n",
			PDFs:    []pdfRef{{srcIdx: 1, attachName: "Faktura_2026-04.pdf"}},
		},
		{
			Name:    "12-tomt-mejl",
			Subject: "Re: Hej",
			From:    "kollega@fa-tec.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Tack, vi hörs!\n\n/Anna\n",
		},

		// === Godkänn-kön → review/ (5) ===
		{
			Name:    "13-cert-pdf-ljuger-subject",
			Subject: "Reklamblad april",
			From:    "marketing@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Hej, se vårat senaste reklamblad!\n",
			PDFs:    []pdfRef{{srcIdx: 2, attachName: "Reklamblad_april_2026.pdf"}},
		},
		{
			Name:    "14-subject-cert-men-pdf-korrupt",
			Subject: "Certifikat B100020",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Bifogat cert.\n",
			PDFs:    []pdfRef{{srcIdx: -1, attachName: "scan001.pdf"}},
		},
		{
			Name:    "15-cert-utan-31-falt",
			Subject: "Cert",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Bifogat.\n",
			PDFs:    []pdfRef{{srcIdx: -2, attachName: "Document.pdf"}},
		},
		{
			Name:    "16-cert-och-foljesedel",
			Subject: "Certifikat + följesedel B100016",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Hej, följesedel + cert bifogas.\n",
			PDFs: []pdfRef{
				{srcIdx: 3, attachName: "612037.pdf"},
				{srcIdx: -2, attachName: "Foljesedel 612037.pdf"},
			},
		},
		{
			Name:    "17-cert-utan-bnummer-langt-filnamn",
			Subject: "Certifikat",
			From:    "certifikat@stalleverantor.se",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Bifogat cert.\n",
			PDFs:    []pdfRef{{srcIdx: 4, attachName: "Materialcertifikat EN10204 3.1 - plat 16mm S690QL chargenr 70703.pdf"}},
		},

		// === Specialfall (3) ===
		{
			Name:         "18-stort-body-over-64kb",
			Subject:      "Certifikat B100018",
			From:         "leverantor@example.com",
			To:           "robin.sundsten@fa-tec.se",
			Body:         "B-nummer: B100018.\n\n",
			LargeBodyPad: 80, // KB lorem
			PDFs:         []pdfRef{{srcIdx: 5, attachName: "70703.pdf"}},
		},
		{
			Name:    "19-multipart-alternative-pdf-i-html",
			Subject: "Certifikat B100019 (HTML)",
			From:    "leverantor@example.com",
			To:      "robin.sundsten@fa-tec.se",
			Body:    "Cert B100019 är inbäddat i HTML-versionen.\n",
			AltOnly: true,
		},
		{
			Name:          "20-svenska-tecken-base64-subject",
			Subject:       "Stålcertifikat för åäö-leverans B100021",
			SubjectEncode: true,
			From:          "leverantor@example.com",
			To:            "robin.sundsten@fa-tec.se",
			Body:          "Bifogat hittar du certet.\n",
			PDFs:          []pdfRef{{srcIdx: 6, attachName: "Stålcert åäö 864606334.pdf"}},
		},
	}
}
