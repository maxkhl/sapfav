# sapfav

Kleines Web-Tool für SAP-Transaktionscodes. Ersetzt die Favoritenliste in
SAP Easy Access durch einen Baum, den man vernünftig pflegen und durchsuchen kann –
mit eigenen Favoriten je Kunde.

- Ordner und Transaktionen in beliebiger Tiefe, aufklappbar
- Kunden: allgemeine Favoriten plus eigene Favoriten je Kunde
- Live-Suche über T-Code, Titel und Beschreibung (`/` fokussiert, `Esc` leert)
- Klick auf eine Transaktion kopiert den T-Code
- Anlegen, Bearbeiten, Löschen, Verschieben, Hoch/Runter
- Import des SAP-Favoriten-Exports mit Vorschau
- Export zurück ins SAP-Format zum Hochladen in SAP
- Backup des ganzen Datenbestands als JSON

Ein einzelnes, statisch gelinktes Go-Binary mit SQLite, kein Framework, keine
CDN-Abhängigkeiten – läuft offline im LAN.

> **Keine eigene Anmeldung.** Die App muss hinter einem Auth-Proxy laufen
> (z. B. Authentik Forward Auth im Nginx Proxy Manager). Wer sie direkt
> erreicht, kann alles lesen und ändern.

## Setup mit Docker Compose / Dockge

```sh
git clone https://github.com/maxkhl/sapfav.git
cd sapfav
cp .env.example .env      # optional, Defaults passen
docker compose up -d --build
```

Danach läuft die App auf `http://<host>:8000`. In Dockge den Inhalt von
`docker-compose.yml` als neuen Stack anlegen.

### Fertiges Image aus GHCR

Jeder Push auf `main` baut ein Image für `linux/amd64` und `linux/arm64`:

- `ghcr.io/maxkhl/sapfav:latest`
- `ghcr.io/maxkhl/sapfav:<short-sha>` – zum Festnageln einer Version

In `docker-compose.yml` dazu `build: .` auskommentieren und die
`image:`-Zeile aktivieren.

**Hinweis:** Neue Pakete auf GHCR sind zunächst **privat**. Entweder unter
GitHub → Profil → Packages → `sapfav` → Package settings die Sichtbarkeit auf
*Public* stellen, oder auf dem Docker-Host einloggen:

```sh
echo <PAT mit read:packages> | docker login ghcr.io -u maxkhl --password-stdin
```

### Konfiguration

| Variable      | Default           | Bedeutung                          |
|---------------|-------------------|------------------------------------|
| `SAPFAV_PORT` | `8000`            | Port im Container und auf dem Host |
| `SAPFAV_DB`   | `/data/tcodes.db` | Pfad der SQLite-Datenbank          |

### Daten und Rechte

Die Datenbank liegt im Volume `/data`. Der Container läuft als unprivilegierter
Nutzer **65532** (distroless `nonroot`).

- Mit dem **benannten Volume** aus der `docker-compose.yml` passt das automatisch.
- Mit einem **Bind-Mount** (`./data:/data`) muss der Ordner vorher dem Nutzer gehören:

  ```sh
  mkdir -p data && sudo chown 65532:65532 data
  ```

### Hinter Nginx Proxy Manager

Proxy-Host auf `http://<docker-host>:8000` anlegen und Authentik Forward Auth
davorschalten. Liegen NPM und sapfav im selben Docker-Netz, kann das
`ports:`-Mapping entfallen; Ziel ist dann `http://sapfav:8000`.

Der Container hat einen Healthcheck (`/sapfav healthcheck` ruft `/healthz` auf).

## Kunden

Jeder Favorit gehört entweder zu **Allgemein** (z. B. VA03, SE16, SU53 – braucht
man überall) oder zu genau einem **Kunden** (z. B. dessen Z-Transaktionen).

- **Kunden…** in der Kopfzeile: anlegen, umbenennen, Notizen pflegen
  (Systeme, Mandanten …), löschen. Beim Löschen eines Kunden mit Favoriten wird
  gefragt, ob sie nach Allgemein wandern oder mitgelöscht werden.
- Die **Ansicht** oben links wählt, was angezeigt wird:
  - *Alle Kunden* – Allgemein und jeder Kunde als eigener Abschnitt
  - *Nur Allgemein*
  - ein Kunde – Allgemein und dieser Kunde
- Die Suche läuft über die gerade angezeigten Abschnitte.
- **Verschieben nach…** kann Einträge auch zwischen Allgemein und Kunden
  verschieben; Ordner nehmen ihren Inhalt mit.

## Import des SAP-Exports

1. In SAP Easy Access die Favoriten als Textdatei exportieren
   (Menü **Favoriten → Herunterladen auf PC**).
2. In sapfav **Import…** öffnen, Datei wählen.
3. **Encoding**: SAP schreibt unter Windows `cp1252` (Default). Nur wenn die
   Datei vorher in einem Editor als UTF-8 gespeichert wurde, `UTF-8` wählen.
   Falsches Encoding erkennt man in der Vorschau an kaputten Umlauten bzw. an
   einer Fehlermeldung.
4. **Importieren nach**: Allgemein oder ein Kunde.
5. **Vorschau** zeigt, was erkannt wurde:
   - Anzahl Ordner und Transaktionen
   - Zeilen, die nicht übernommen werden, mit Grund (z. B. doppelte ID)
   - Einträge, deren Parent-ID ins Leere zeigt – sie landen auf der obersten Ebene
6. Übernehmen wählen:
   - **An Bestand anhängen** – importierte Einträge kommen ans Ende der obersten Ebene
   - **Bestand ersetzen** – löscht vorher alles **im gewählten Bereich**,
     auch eigene Beschreibungen; andere Kunden bleiben unberührt
7. **Importieren**.

Die Datei wird dabei nicht gespeichert; bei der Bestätigung wird sie noch einmal
hochgeladen und neu gelesen.

## Export für SAP

**Export für SAP…** erzeugt eine Datei im selben Format, wie SAP sie beim
Herunterladen schreibt (cp1252, feste Spalten).

1. Inhalt wählen:
   - *Allgemein*
   - *Kunde + Allgemein* – beides **flach** in einer Datei: erst die allgemeinen
     Einträge, dann die des Kunden, jeweils auf oberster Ebene
   - *Nur Kunde*
2. Die Vorschau zeigt Anzahl und Dateiname und listet Einträge, die nicht
   verlustfrei exportiert werden können:
   - Zeichen, die es in cp1252 nicht gibt (z. B. `→`), werden zu `?`
   - Kurztexte über 132 Zeichen werden gekürzt
   - T-Codes über 48 Zeichen werden ausgelassen
3. **Herunterladen** und in SAP Easy Access über
   **Favoriten → Hochladen vom PC** einspielen.

**Wichtig:**

- **Beschreibungen** gibt es in SAP nicht – sie bleiben nur in sapfav.
- Vor dem Hochladen die **aktuellen SAP-Favoriten herunterladen**, damit man
  zurück kann. Wie SAP beim Hochladen mit bestehenden Favoriten umgeht
  (ergänzen oder ersetzen), vorher einmal mit einem unkritischen Benutzer testen.

### Dateiformat

Eine Zeile je Eintrag, feste Spalten:

| Spalte | Inhalt |
|--------|--------|
| 0–1    | `TR` = Transaktion, zwei Leerzeichen = Ordner |
| 2–6    | ID des übergeordneten Ordners (`00001` = oberste Ebene) |
| 7–11   | eigene ID |
| 12–59  | Transaktionscode (bei Ordnern leer) |
| ab 60  | Kurztext |

Beim Import werden Zeilen mit verrutschten Spalten (z. B. nach Kopieren aus
einem Editor) per Fallback zerlegt statt verworfen.

## Backup

**Backup (JSON)** in der Kopfzeile lädt Kunden und alle Favoriten inklusive
Beschreibungen herunter. Alternativ die Datei `tcodes.db` aus dem Volume sichern
(bei laufendem Container zusätzlich `tcodes.db-wal` mitnehmen oder vorher stoppen).

## Entwicklung

Voraussetzung: Go 1.26.

```sh
go test ./...
SAPFAV_DB=./dev.db go run ./cmd/sapfav
```

Das Frontend liegt unter `internal/web/static/` und wird per `go:embed` ins
Binary gepackt – nach Änderungen neu starten, ein Build-Step ist nicht nötig.

Entscheidungen zu Stack und Aufbau stehen in [CLAUDE.md](CLAUDE.md).

## API

JSON unter `/api`. Schreibende Requests brauchen `Content-Type: application/json`
(Schutz gegen Cross-Site-Formulare); der Import-Upload stattdessen
`X-Requested-With: XMLHttpRequest`.

| Methode | Pfad | Body / Parameter |
|---|---|---|
| GET    | `/healthz` | – |
| GET    | `/api/tree` | – (alle Bereiche, `customer_id` je Knoten) |
| GET    | `/api/export` | – (JSON-Backup) |
| GET    | `/api/export/sap` | `?customer=<id>&with_general=1` (ohne `customer` = Allgemein) |
| GET    | `/api/export/sap/preview` | wie oben, liefert Anzahl und Warnungen |
| GET    | `/api/customers` | – |
| POST   | `/api/customers` | `{name, notes}` |
| PUT    | `/api/customers/{id}` | `{name, notes}` |
| DELETE | `/api/customers/{id}` | `{nodes: "general" \| "delete"}` bei Kunden mit Favoriten |
| POST   | `/api/nodes` | `{customer_id, parent_id, kind, tcode, title, description}` |
| PUT    | `/api/nodes/{id}` | `{tcode, title, description}` |
| DELETE | `/api/nodes/{id}` | `{children: "lift" \| "delete"}` bei nicht-leeren Ordnern |
| POST   | `/api/nodes/{id}/move` | `{parent_id, customer_id}` (`parent_id: null` = oberste Ebene des Bereichs) |
| POST   | `/api/nodes/{id}/reorder` | `{direction: "up" \| "down"}` |
| POST   | `/api/import/preview` | multipart: `file`, `encoding`, `customer` |
| POST   | `/api/import` | multipart: `file`, `encoding`, `customer`, `mode` = `append` \| `replace` |
