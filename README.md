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
  inte licensierat på systemet, så klienten är renodlat läsande.) Inloggning sker
  **lazy** först vid faktisk API-användning — aldrig vid start — eftersom varje
  login loggar ut den interaktiva Monitor-sessionen.
- **Följesedel → vision → matchning** — bild på en följesedel tolkas med
  Claudes vision och matchas mot en inköpsorder/orderrad.
- **Inleverans via UI-styrning** (Windows) — eftersom skriv-API:t saknas
  registreras inleverans/mottagningskontroll genom att styra Monitor-klienten
  (öppnar rutinen via `mond://`-länk, fokuserar fönstret, fyller i ordernummer,
  Ctrl+L; Ctrl+S bara efter uttrycklig bekräftelse + påslagen auto-spara).
  Rutin-länkarna är hårdkodade i `internal/server/monitorui.go`.
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

---

## V2 (`cmd/cert-renamer-v2`) — parallellt spår

V2 är en omskrivning med omvänd datamodell: **databasen är sanningen**, inte
filen. Ett cert som kommer in blir en levande rad + en stabil, aldrig omdöpt
PDF i certlagret. Filnamnet är ett *levande förslag* som räknas om från
effektiv data (rättelser > rå extraktion, bekräftade B-nummer > extraherade)
och blir verklighet först när Rob trycker **Spara** — då skrivs den omdöpta
kopian med inbäddad metadata till utmappen och certet fryses.

Ett enda UI (Översikt) + Sickan-chat. Kö/Granskas/Inleverans finns inte i V2.

### Köra båda binärerna parallellt

V1 och V2 delar `config.json` (API-nyckel, Monitor-uppgifter, inbox) men har
**separata databaser** (`cert-renamer.db` / `cert-renamer-v2.db`) och separata
filytor (V2: `<inbox>/v2/store` + `<inbox>/v2/out`, överstyrbara via
`v2_store_dir`/`v2_output_dir`).

**Viktigt under parallelldrift:** båda binärernas mailintag pollar samma
inbox. Kör bara EN av dem med intaget på (håll V1-workern "Av" när V2:s
intag är igång) — annars kapplöper de om samma `.eml`-filer.

Engångsimport av V1:s historik (matchningshistorik — gamla cert fortsätter
matcha nya orderrader):

```bash
cert-renamer-v2 -import-v1   # läser approved/ + queue/ via inbäddad metadata; V1:s mappar röres inte
```

### Arkitekturregler (icke förhandlingsbara)

1. **En enda skrivväg** — all mutation går genom `internal/v2/app`; HTTP,
   Sickan och bakgrundsjobb är tunna adaptrar.
2. **Ren domänkärna** — `internal/v2/domain` har noll IO-beroenden;
   tillståndsmaskin, effective-values och namnbygge är tabelltestade.
3. **Ports för sidoeffekter** — AI/Monitor/klocka bakom småinterfaces;
   `go test ./...` kör grönt offline.
4. **Crash-säker, idempotent Spara** — utfil + inbäddning först, DB-commit
   sist; om-spar återanvänder utfilen via hash (aldrig `_2`-dubbletter).
5. **Typade fel, mappade på ett ställe** — `ErrNotFound`→404,
   `ErrFrozen`/`ErrTransition`→409, valideringsvarningar→422.
6. **Migrations från dag 1** — `PRAGMA user_version` + numrerad lista.
7. **Strukturerad logg** — `log/slog`; SSE-loggen matas därifrån.
8. **CI-grind** — vet/build/test på varje push (`.github/workflows/ci.yml`).

### V2-struktur

```
cmd/cert-renamer-v2/  # wiring + graceful shutdown; -import-v1
internal/v2/
├── domain/           # ren kärna: typer, tillståndsmaskin, effective, levande namn
├── app/              # ENDA skrivvägen: guards, tx, rättelselogg, Spara
├── store/            # migrations, dum CRUD, certlager + utmapp
├── intake/           # eml → levande cert-rader (AI bakom port)
├── monitorsync/      # daglig Monitor-refresh, matchning, cachad AI-dom
├── sickan/           # bantad agent (~19 verktyg, tunna app-adaptrar)
├── importer/         # -import-v1
└── server/           # tunna handlers, SSE, ES-modul-UI utan byggsteg
```

Medvetet uteslutet i V2: inleveransregistrering (Monitor-UI-styrning),
följesedelflödet, morgonbriefen (kan porteras senare vid behov).
