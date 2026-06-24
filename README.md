# Cert Renamer

Ett litet desktop-verktyg som automatiskt läser in inkommande mejl med
materialcertifikat (PDF), klassificerar och extraherar fälten med hjälp av
Claude, och döper om PDF:erna till ett enhetligt, sökbart filnamn. Verktyget
kör som en lokal webbserver och öppnar ett UI i webbläsaren — inget moln, ingen
installation utöver en enda binär.

Programmet är skrivet i ren Go (ingen CGO, ingen Docker) och byggs till en
fristående `.exe` (Windows) eller `.app` (macOS).

---

## Vad det gör

Verktyget pekas mot en **inbox-mapp** dit `.eml`-filer (sparade mejl) landar.
En bakgrundsarbetare pollar mappen var 30:e sekund och kör varje mejl genom en
pipeline:

1. **Parsa** `.eml` → ämne, avsändare, brödtext, PDF-bilagor (`internal/eml`).
2. **Kategorisera** mejlet med Claude Haiku — certifikat, faktura, följesedel,
   orderbekräftelse, teknisk dokumentation, reklam eller övrigt. Allt loggas i
   databasen; icke-certifikat arkiveras direkt.
3. **Verifiera** att bilagorna verkligen är cert (EN 10204 3.1 m.fl.).
4. **Extrahera** fält ur PDF:en med Claude Sonnet (charge, material,
   produktform, dimension, B-nummer m.m.).
5. **Validera** extraktionen. Lyckas allt döps PDF:en om och läggs i `queue/`
   för godkännande; annars hamnar den i `review/` för manuell granskning.
6. **Bädda in metadata** i PDF:en och spara allt i en lokal SQLite-databas.

Godkända filer flyttas till `approved/`, granskade kan befordras tillbaka till
kön eller arkiveras.

### Filnamnsschema

Omdöpta filer följer mönstret:

```
<charge>-<produktform>-<dimension>-<materialkod>-<b-nummer...>.pdf
```

(se `cert.BuildFilename` i `internal/cert/cert.go`)

### Övriga funktioner

- **Monitor ERP-integration** (`internal/monitor`) — **läser** inköpsorder via
  OData och kan koppla cert/följesedlar mot rätt order. (Monitors skriv-API är
  inte licensierat på systemet, så klienten är renodlat läsande.)
- **Följesedel → vision → matchning** — bild på en följesedel tolkas med
  Claudes vision och matchas mot en inköpsorder/orderrad.
- **Inleverans via UI-styrning** (Windows) — eftersom skriv-API:t saknas
  registreras inleverans/mottagningskontroll genom att styra Monitor-klienten
  (öppnar rutinen via `monitor://`-länk, fyller i ordernummer, Ctrl+L; Ctrl+S
  bara efter uttrycklig bekräftelse). Länkar/fönstertitel ställs in i UI:t.
- **"Sickan"** (`internal/sickan`) — en chat-/agentyta som kan fylla i data och
  köra verktyg mot Monitor.
- **Kostnadsspårning** — token-användning per Claude-anrop summeras och visas
  live i UI:t via Server-Sent Events.

---

## Köra

Förutsätter Go 1.25+ (se `go.mod`).

```bash
go run ./cmd/cert-renamer
```

Programmet startar en lokal HTTP-server på en slumpad port på `127.0.0.1` och
öppnar UI:t automatiskt (Chrome/Edge i `--app`-läge om det finns, annars
standardwebbläsaren).

I UI:t:

1. Öppna **⚙️ Inställningar** och spara din **Anthropic API-nyckel**.
2. Välj **inbox-mapp**.
3. Tryck **Start**. Lägg `.eml`-filer i mappen — de processas automatiskt.

### Konfiguration

Inställningar sparas per användare i `config.json`:

| Plattform | Sökväg |
|-----------|--------|
| macOS     | `~/Library/Application Support/cert-renamer/config.json` |
| Windows   | `%APPDATA%\cert-renamer\config.json` |
| Linux     | `~/.config/cert-renamer/config.json` |

Relevanta fält (se `internal/store/config.go`): `inbox_dir`, `api_key`,
`autostart`, `sickan_model`, `b_number_mode`, samt Monitor-uppgifterna
`monitor_url` / `monitor_user` / `monitor_password`.

Monitor-uppgifterna kan anges direkt i UI:t under **⚙️ Inställningar → 🔌
Monitor ERP** (URL, användarnamn, lösenord) — anslutningen görs om direkt utan
omstart. De kan också sättas via miljövariablerna `MONITOR_URL`,
`MONITOR_USER` och `MONITOR_PASSWORD`, som har företräde framför `config.json`.

`config.example.json` visar minsta möjliga inbox-konfiguration.

### Mappstruktur i inboxen

Verktyget skapar och använder följande undermappar i den valda inboxen:

```
inbox/
├── queue/           # omdöpta cert som väntar på godkännande
├── review/          # mejl/PDF som behöver manuell granskning
├── approved/        # godkända cert
├── arkiverat/       # arkiverade mejl (icke-cert, dubbletter m.m.)
└── delivery_notes/  # följesedlar
```

Loggar skrivs till en plattformsspecifik logg-mapp och rensas efter 30 dagar.

---

## Bygga

`build.sh` bygger fristående binärer för macOS (arm64, amd64, universal +
`.app`-bundle) och Windows (amd64):

```bash
./build.sh
# resultat i dist/mac och dist/windows
```

CI (`.github/workflows`) bygger automatiskt en Windows-`.exe` och en portabel
zip.

---

## Projektstruktur

```
cmd/cert-renamer/     # entrypoint: loggning, browser-launch, HTTP-server
internal/
├── ai/               # Claude-anrop: klassificering, verify, extract, vision
├── cert/             # cert-domänen: validering + filnamnsbyggande
├── eml/              # .eml-parsing + B-nummer-extraktion
├── monitor/          # Monitor ERP-klient (OData, inköpsorder, write)
├── server/           # HTTP-API + SSE + UI (embeddad)
├── sickan/           # chat-/agentverktyg
├── store/            # SQLite, config, disk-IO, kostnader, metadata
└── worker/           # inbox-pollning och processeringspipeline
scripts/gen-fixtures/ # generering av testfixturer
testdata/             # testdata
```

---

## Tester

```bash
go test ./...
```
