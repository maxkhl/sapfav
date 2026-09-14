# sapfav

Kleines Web-Tool für SAP-Transaktionscodes. Ersetzt die Favoritenliste in
SAP Easy Access durch einen Baum, den man vernünftig pflegen und durchsuchen kann.

- Ordner und Transaktionen in beliebiger Tiefe, aufklappbar
- Live-Suche über T-Code, Titel und Beschreibung (`/` fokussiert, `Esc` leert)
- Klick auf eine Transaktion kopiert den T-Code
- Anlegen, Bearbeiten, Löschen, Verschieben, Hoch/Runter
- Import des SAP-Favoriten-Exports mit Vorschau
- Export des ganzen Baums als JSON (Backup)

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

## Import des SAP-Exports

1. In SAP Easy Access die Favoriten als Textdatei exportieren
   (Menü **Favoriten → Herunterladen auf PC**).
2. In sapfav **Import…** öffnen, Datei wählen.
3. **Encoding**: SAP schreibt unter Windows `cp1252` (Default). Nur wenn die
   Datei vorher in einem Editor als UTF-8 gespeichert wurde, `UTF-8` wählen.
   Falsches Encoding erkennt man in der Vorschau an kaputten Umlauten bzw. an
   einer Fehlermeldung.
4. **Vorschau** zeigt, was erkannt wurde:
   - Anzahl Ordner und Transaktionen
   - Zeilen, die nicht übernommen werden, mit Grund (z. B. doppelte ID)
   - Einträge, deren Parent-ID ins Leere zeigt – sie landen auf der obersten Ebene
5. Übernehmen wählen:
   - **An Bestand anhängen** – importierte Einträge kommen ans Ende der obersten Ebene
   - **Bestand ersetzen** – löscht vorher alles, auch eigene Beschreibungen
6. **Importieren**.

Die Datei wird dabei nicht gespeichert; bei der Bestätigung wird sie noch einmal
hochgeladen und neu gelesen.

### Dateiformat

Eine Zeile je Eintrag, feste Spalten:

| Spalte | Inhalt |
|--------|--------|
| 0–1    | `TR` = Transaktion, zwei Leerzeichen = Ordner |
| 2–6    | ID des übergeordneten Ordners (`00001` = oberste Ebene) |
| 7–11   | eigene ID |
| 12–59  | Transaktionscode (bei Ordnern leer) |
| ab 60  | Kurztext |

Zeilen mit verrutschten Spalten (z. B. nach Kopieren aus einem Editor) werden
per Fallback zerlegt statt verworfen.

## Backup

**Export (JSON)** in der Kopfzeile lädt den kompletten Baum inklusive
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

| Methode | Pfad | Body |
|---|---|---|
| GET    | `/healthz` | – |
| GET    | `/api/tree` | – |
| GET    | `/api/export` | – |
| POST   | `/api/nodes` | `{parent_id, kind, tcode, title, description}` |
| PUT    | `/api/nodes/{id}` | `{tcode, title, description}` |
| DELETE | `/api/nodes/{id}` | `{children: "lift" \| "delete"}` bei nicht-leeren Ordnern |
| POST   | `/api/nodes/{id}/move` | `{parent_id}` (`null` = oberste Ebene) |
| POST   | `/api/nodes/{id}/reorder` | `{direction: "up" \| "down"}` |
| POST   | `/api/import/preview` | multipart: `file`, `encoding` |
| POST   | `/api/import` | multipart: `file`, `encoding`, `mode` = `append` \| `replace` |
