# Cert Renamer

Ett litet desktop-verktyg som automatiskt läser in inkommande mejl med
materialcertifikat (PDF), klassificerar och extraherar fälten med hjälp av
Claude, och föreslår ett enhetligt, sökbart filnamn. Verktyget kör som en lokal
webbserver och öppnar ett UI i webbläsaren — inget moln, ingen installation
utöver en enda binär.

Programmet är skrivet i ren Go (ingen CGO, ingen Docker) och byggs till en
fristående `.exe` (Windows) eller `.app` (macOS).

> **Arkitektur:** appen bygger på modellen **"databasen är sanningen"**. Ett
> inkommande cert blir en levande DB-rad + en stabil, aldrig omdöpt PDF i
> certlagret. Filnamnet är ett *levande förslag* som räknas om från effektiv
> data, och materialiseras (omdöpt kopia + inbäddad metadata) först när du
> trycker **Spara**. Den äldre disk-drivna V1-appen är arkiverad under
> [`_archive/`](#projektstruktur) — kvar som historik men aldrig byggd.

---

## Vad det gör

Verktyget pekas mot en **inbox-mapp** dit `.eml`-filer (sparade mejl) landar.
Ett bakgrundsintag pollar mappen och kör varje mejl genom en pipeline:

1. **Parsa** `.eml` → ämne, avsändare, brödtext, PDF-bilagor (`internal/v2/eml`).
2. **Kategorisera** mejlet med Claude Haiku — certifikat, faktura, följesedel,
   orderbekräftelse, teknisk dokumentation, reklam eller övrigt. Allt loggas i
   databasen; icke-certifikat noteras men surfas inte som arbetsobjekt.
3. **Verifiera** att bilagorna verkligen är cert (EN 10204 3.1 m.fl.).
4. **Extrahera** fält ur PDF:en med Claude Sonnet (charge, material,
   produktform, dimension, B-nummer m.m.).
5. **Skapa en levande rad** i SQLite och lägg originalet oförändrat i
   certlagret. Filnamnsförslaget räknas om från effektiv data (rättelser > rå
   extraktion, bekräftade B-nummer > extraherade).
6. **Spara** (manuellt): den omdöpta kopian skrivs med inbäddad metadata till
   utmappen och certraden fryses.

Hela flödet syns i **Översikt**, och en chat-/agentyta (**Sickan**) kan fylla i
data och köra verktyg mot Monitor.

### Filnamnsschema

Sparade filer följer mönstret:

```
<charge>-<produktform>-<dimension>-<materialkod>-<b-nummer...>.pdf
```

(se namnbygget i `internal/v2/domain`)

### Övriga funktioner

- **Monitor ERP-integration** (`internal/v2/monitor`) — **läser** inköpsorder
  via OData och kan koppla cert mot rätt order/orderrad. (Monitors skriv-API är
  inte licensierat på systemet, så klienten är renodlat läsande.) Inloggning
  sker **lazy** först vid faktisk API-användning — aldrig vid start — eftersom
  varje login loggar ut den interaktiva Monitor-sessionen.
- **Daglig Monitor-synk** (`internal/v2/monitorsync`) — refresh, matchning och
  cachad AI-dom mot öppna orderrader.
- **Kostnadsspårning** — token-användning per Claude-anrop summeras och visas
  live i UI:t via Server-Sent Events.

Medvetet uteslutet (kan porteras senare vid behov): inleveransregistrering
(Monitor-UI-styrning), följesedel-vision-flödet och morgonbriefen.

---

## Köra

Förutsätter Go 1.25+ (se `go.mod`).

```bash
go run ./cmd/cert-renamer
```

Programmet startar en lokal HTTP-server på en slumpad port på `127.0.0.1` och
öppnar UI:t automatiskt. Kör med `-no-browser` för att bara logga URL:en
(`lyssnar url=…`) utan att öppna någon flik.

I UI:t:

1. Öppna **⚙️ Inställningar** och spara din **Anthropic API-nyckel**.
2. Välj **inbox-mapp**.
3. Lägg `.eml`-filer i mappen — de processas automatiskt och dyker upp i
   Översikt. Granska förslaget och tryck **Spara**.

### Konfiguration

Inställningar sparas per användare i `config.json`:

| Plattform | Sökväg |
|-----------|--------|
| macOS     | `~/Library/Application Support/cert-renamer/config.json` |
| Windows   | `%APPDATA%\cert-renamer\config.json` |
| Linux     | `~/.config/cert-renamer/config.json` |

Databasen (`cert-renamer-v2.db`) ligger bredvid `config.json` i samma katalog.

Relevanta fält (se `internal/v2/store/config.go`): `inbox_dir`, `api_key`,
`autostart`, `sickan_model`, `b_number_mode`, `v2_store_dir`, `v2_output_dir`,
samt Monitor-uppgifterna `monitor_url` / `monitor_user` / `monitor_password`.

Monitor-uppgifterna kan anges direkt i UI:t under **⚙️ Inställningar** —
anslutningen görs om direkt utan omstart. De kan också sättas via
miljövariablerna `MONITOR_URL`, `MONITOR_USER` och `MONITOR_PASSWORD`, som har
företräde framför `config.json`.

`config.example.json` visar minsta möjliga inbox-konfiguration.

### Mappstruktur i inboxen

Verktyget använder följande under den valda inboxen (överstyrbart via
`v2_store_dir` / `v2_output_dir`):

```
inbox/
└── v2/
    ├── store/   # certlagret: stabila, aldrig omdöpta original
    └── out/     # sparade (omdöpta) cert med inbäddad metadata
```

Loggar skrivs till en plattformsspecifik logg-mapp och rensas efter 30 dagar.

### Engångsimport av V1-historik

Gamla, redan omdöpta cert (V1:s `approved/` + `queue/`) kan läsas in en gång så
att matchningshistoriken följer med. Importern använder inbäddad metadata i
PDF:erna — den rör inte V1:s mappar:

```bash
cert-renamer -import-v1
```

---

## Bygga

`build.sh` bygger fristående binärer för macOS (arm64, amd64, universal +
`.app`-bundle) och Windows (amd64):

```bash
./build.sh
# resultat i dist/mac och dist/windows
```

CI (`.github/workflows/ci.yml`) kör `go vet/build/test ./...` på varje push;
`release.yml` bygger automatiskt en Windows-`.exe` (`cert-renamer.exe`) och en
portabel zip.

---

## Arkitekturregler (icke förhandlingsbara)

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
8. **CI-grind** — vet/build/test på varje push.

---

## Projektstruktur

```
cmd/cert-renamer/     # wiring: konfig, databas, HTTP-server, browser-launch,
                      # graceful shutdown, -import-v1
internal/v2/
├── domain/           # ren kärna: typer, tillståndsmaskin, effective, levande namn
├── app/              # ENDA skrivvägen: guards, tx, rättelselogg, Spara
├── store/            # SQLite (migrations, dum CRUD), config, kostnader, metadata,
│                     #   certlager + utmapp
├── intake/           # eml → levande cert-rader (AI bakom port)
├── monitorsync/      # daglig Monitor-refresh, matchning, cachad AI-dom
├── sickan/           # bantad agent (tunna app-adaptrar)
├── importer/         # -import-v1
├── server/           # tunna handlers, SSE, ES-modul-UI utan byggsteg
├── ai/               # Claude-anrop: klassificering, verify, extract, vision
├── cert/             # cert-domäntyper + validering
├── eml/              # .eml-parsing + B-nummer-extraktion
└── monitor/          # Monitor ERP-läsklient (OData, inköpsorder)

_archive/             # Go-ignorerat, fryst V1-snapshot (kompileras/byggs aldrig):
                      #   den gamla disk-drivna appen, cmd/monitor-probe,
                      #   scripts/gen-fixtures
```

`_archive/` är kvar som historik. Katalogen är `_`-prefixad, så Go-verktygen
ignorerar den helt — `go build ./...` bygger bara V2. Vill man köra V1 igen får
man återställa via git-historiken.

---

## Tester

```bash
go test ./...
```
