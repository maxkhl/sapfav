# sapfav

Web-Tool zur Verwaltung von SAP-Transaktionscodes (Ersatz für die
SAP-Easy-Access-Favoritenliste). Läuft als Docker-Container im Homelab
(Dockge, hinter Nginx Proxy Manager und Authentik Forward Auth).

## Stack — bewusst gewählt, nicht umwerfen

- Go 1.26 (`go 1.26` in go.mod, Build-Image `golang:1.26`), `net/http` mit
  ServeMux-Routing (Methoden im Pattern), kein Framework. Ursprünglich war 1.23
  vorgesehen; 1.23 ist ohne Sicherheitsupdates und die aktuellen Versionen beider
  Abhängigkeiten verlangen ≥ 1.26 — daher bewusst angehoben (Entscheidung 2026-09-14).
- Abhängigkeiten: **nur** `modernc.org/sqlite` (pure Go) und
  `golang.org/x/text/encoding/charmap`. Keine weiteren.
- Frontend: statisches HTML/CSS/Vanilla-JS per `go:embed`. Kein Build-Step,
  kein npm, keine CDN-Ressourcen (muss offline im LAN laufen).
- Keine Authentifizierung, keine Sessions, keine User-Tabelle — das macht Authentik.
- Statisch gelinkt (`CGO_ENABLED=0`), Laufzeitimage `gcr.io/distroless/static-debian13:nonroot`.
- Lokale Toolchain liegt unter `~/.local/go` (Go 1.26.8). Mit `GOTOOLCHAIN=local`
  arbeiten, damit keine Abhängigkeit unbemerkt eine neuere Go-Version erzwingt
  als das Build-Image `golang:1.26` hat.

## Datenmodell

Eine Tabelle `nodes`, Baum beliebiger Tiefe: `id`, `parent_id` (NULL = oberste
Ebene), `kind` (`folder`|`tx`), `tcode` (nur bei `tx`), `title`, `description`,
`sort_index`, `created_at`, `updated_at`.

- Schema in `internal/store/schema.sql`, eingebettet, beim Start angewandt.
  Versionierung über `PRAGMA user_version`, kein Migrations-Framework.
- Parent muss ein Ordner sein; keine Zyklen beim Verschieben (im Code geprüft).
- Verschieben: ans Ende des Ziel-Parents, Quelle und Ziel werden neu durchnummeriert.
- Ordner löschen nur mit expliziter Wahl: Inhalt eine Ebene hoch (`lift`) oder
  mitlöschen (`delete`). Nicht-leerer Ordner ohne Wahl → 409.

## SAP-Import (Kernfeature)

Feste Spalten je Zeile: 0-1 `TR`/zwei Leerzeichen, 2-6 Parent-ID, 7-11 eigene ID,
12-59 T-Code, ab 60 Kurztext. Hierarchie nur über IDs; Parent `00001` = oberste Ebene.

- Datei ist cp1252 mit CRLF. Erst dekodieren, dann nach Zeichen (Runes) zerlegen.
  Encoding im Dialog umschaltbar, Default cp1252.
- Parser tolerant: Spaltenpositionen primär, sonst Fallback-Zerlegung; nicht
  passende Zeilen sammeln und melden. Parent-IDs ins Leere → melden, beim Import
  auf oberste Ebene.
- Zwei Schritte, zustandslos: `POST /api/import/preview` (schreibt nichts), dann
  `POST /api/import` mit derselben Datei und `mode` = `append`|`replace`.
  `append` hängt ans Ende der obersten Ebene. `sort_index` = Dateireihenfolge.
- `testdata/sap-favorites-export.txt` ist ein echter Export: 237 Knoten,
  15 Ordner, 222 Transaktionen, alle ab oberster Ebene erreichbar. Nicht anfassen
  (`.gitattributes` schützt die CRLF-Enden).

## API-Regeln

- REST unter `/api`, JSON rein/raus. `GET /healthz` für den Healthcheck.
- Schreib-Endpunkte (POST/PUT/DELETE) nur mit `Content-Type: application/json`,
  sonst 415 — Schutz gegen Cross-Site-Form-Posts, da die App selbst keine Auth hat.
- Ausnahme Import-Upload (multipart): verlangt `X-Requested-With: XMLHttpRequest`.
- Suche läuft clientseitig über den geladenen Baum.

## Betrieb

- Env: `SAPFAV_DB` (Default `/data/tcodes.db`), `SAPFAV_PORT` (Default `8000`).
- Healthcheck im Container über `sapfav healthcheck` (distroless hat kein curl).
- CI: Push auf `main` → `go vet`, `go test`, dann Image nach
  `ghcr.io/maxkhl/sapfav` (`:latest`, `:<short-sha>`), amd64+arm64 per
  Go-Cross-Compile, kein QEMU.

## Arbeitsweise

- Keine Features einbauen, die nicht vereinbart sind — Ideen als Vorschlag nennen.
- Drag & Drop ist bewusst nicht in v1.
- In kleinen Schritten committen.
