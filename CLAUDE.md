# sapfav

Web-Tool zur Verwaltung von SAP-Transaktionscodes (Ersatz für die
SAP-Easy-Access-Favoritenliste), mit Favoriten je Kunde für die Arbeit als
SAP-Dienstleister. Läuft als Docker-Container im Homelab (Dockge, hinter
Nginx Proxy Manager und Authentik Forward Auth).

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

Tabelle `customers`: `id`, `name` (eindeutig, ohne Groß/klein), `notes`
(Freitext, z. B. SIDs/Mandanten), `created_at`, `updated_at`.
Eine eigene Systemebene gibt es bewusst nicht.

Tabelle `nodes`, Baum beliebiger Tiefe: `id`, `customer_id` (NULL = Allgemein),
`parent_id` (NULL = oberste Ebene), `kind` (`folder`|`tx`), `tcode` (nur bei `tx`),
`title`, `description`, `sort_index`, `created_at`, `updated_at`.

- Jeder Eintrag gehört zu genau einem Bereich: Allgemein oder ein Kunde
  (Variante A, keine Mehrfachzuordnung). Unter einem Ordner gilt dessen Kunde.
  Die oberste Ebene wird je Bereich sortiert.
- Schema in `internal/store/schema.sql`, eingebettet, beim Start angewandt.
  Versionierung über `PRAGMA user_version`, kein Migrations-Framework.
  Stand 2026-09-14 gibt es noch keine echten Daten, daher wurde Version 1 direkt
  geändert. Sobald die App produktiv läuft, Änderungen nur noch als neue Version.
- Parent muss ein Ordner sein; keine Zyklen beim Verschieben (im Code geprüft).
- Verschieben: ans Ende des Ziels, Quelle und Ziel werden neu durchnummeriert.
  Wechselt der Bereich, zieht der Unterbaum mit.
- Ordner löschen nur mit expliziter Wahl: Inhalt eine Ebene hoch (`lift`) oder
  mitlöschen (`delete`). Nicht-leerer Ordner ohne Wahl → 409.
- Kunde löschen: Favoriten ans Ende von Allgemein (`general`) oder mitlöschen
  (`delete`). Kunde mit Favoriten ohne Wahl → 409.

## SAP-Dateiformat (Paket `internal/sapfile`)

Feste Spalten je Zeile: 0-1 `TR`/zwei Leerzeichen, 2-6 Parent-ID, 7-11 eigene ID,
12-59 T-Code, ab 60 Kurztext. Hierarchie nur über IDs; Parent `00001` = oberste Ebene.

**Import (Kernfeature)**

- Datei ist cp1252 mit CRLF. Erst dekodieren, dann nach Zeichen (Runes) zerlegen.
  Encoding im Dialog umschaltbar, Default cp1252.
- Parser tolerant: Spaltenpositionen primär, sonst Fallback-Zerlegung; nicht
  passende Zeilen sammeln und melden. Parent-IDs ins Leere → melden, beim Import
  auf oberste Ebene.
- Zwei Schritte, zustandslos: `POST /api/import/preview` (schreibt nichts), dann
  `POST /api/import` mit derselben Datei, Zielbereich `customer` und
  `mode` = `append`|`replace`. `replace` betrifft nur den Zielbereich, `append`
  hängt ans Ende seiner obersten Ebene. `sort_index` = Dateireihenfolge.
- `testdata/sap-favorites-export.txt` ist ein echter Export: 237 Knoten,
  15 Ordner, 222 Transaktionen, alle ab oberster Ebene erreichbar. Nicht anfassen
  (`.gitattributes` schützt die CRLF-Enden).

**Export zurück nach SAP**

- Gleiches Format: cp1252, CRLF auch nach der letzten Zeile, jede Zeile auf
  192 Zeichen aufgefüllt. IDs fortlaufend ab 00002, Ordner vor ihrem Inhalt.
- Kunde + Allgemein in einer Datei **flach**: erst die oberste Ebene von
  Allgemein, dann die des Kunden — keine Hüllordner.
- Verluste werden als Warnung gemeldet: Zeichen außerhalb cp1252 → `?`,
  Kurztext > 132 Zeichen gekürzt, T-Code > 48 Zeichen ausgelassen.
  Beschreibungen haben in SAP kein Feld und entfallen.
- Roundtrip-Test: echter Export → Import → SAP-Export → Import ergibt denselben Baum.
- Ob SAP die Datei beim Hochladen annimmt, ist nur manuell prüfbar.

## MCP-Schnittstelle (Paket `internal/mcp`)

Ein Endpunkt `POST /mcp`, JSON-RPC 2.0, Antwort immer als einzelnes JSON-Objekt
(kein SSE — wir senden keine Fortschrittsmeldungen).

- **Zwei Protokoll-Zeitalter ("dual-era"):** Revision `2026-07-28` (Version,
  Identität und Fähigkeiten als `_meta` je Anfrage, keine Sessions, kein
  Handshake) **und** der `initialize`-Weg älterer Revisionen (`2025-11-25`
  und früher). Welche ein Claude-Client spricht, steht vorher nicht fest.
  Erkennung: liegt `_meta.io.modelcontextprotocol/protocolVersion` im Body,
  gilt die moderne Revision, sonst die alte. `resultType: "complete"` darf
  nur bei der modernen Revision im Ergebnis stehen.
- **Header werden gegen den Body geprüft:** `MCP-Protocol-Version`,
  `Mcp-Method` und bei `tools/call` `Mcp-Name` spiegeln Body-Felder, damit
  Proxys routen können. Abweichung → `400` mit `-32020`. Die Base64-Sonderform
  `=?base64?…?=` muss vorher dekodiert werden.
- Unbekannte Version → `400` mit `-32022` und Liste der unterstützten
  Versionen. Unbekannte Methode → `404` mit `-32601`. Benachrichtigungen →
  `202`. `GET`/`DELETE` → `405`. Vorhandener fremder `Origin` → `403`
  (Pflicht gegen DNS-Rebinding). `server/discover` ist Pflicht.
- **Fehler:** Was das Modell korrigieren kann (ungültige Argumente,
  `ErrInvalid`, `ErrNotFound`, `ErrConflict`), kommt als Tool-Ergebnis mit
  `isError: true` zurück; alles andere als JSON-RPC-Fehler `-32603`.
- **Tools** (elf): lesend `list_customers`, `get_tree`, `search`; schreibend
  `create_node`, `update_node` (nur mitgeschickte Felder), `move_node`,
  `set_children_order`, `delete_node`; dazu die drei Kunden-Tools.
  `set_children_order` sortiert eine ganze Ebene in einem Aufruf — mit
  Hoch/Runter bräuchte ein Modell für 88 Einträge hunderte Aufrufe.
  Import und SAP-Export sind bewusst **keine** Tools.
- **Absicherung:** `SAPFAV_MCP_TOKEN` schaltet den Endpunkt ein; ist er leer,
  wird `/mcp` nicht registriert. Vergleich in konstanter Zeit. Authentik Forward
  Auth kann hier nicht greifen (keine Browser-Anmeldung möglich), deshalb wird
  `/mcp` im Authentik-Proxy-Provider als *unauthenticated path* geführt.
  OAuth bauen wir nicht: claude.ai kann feste Request-Header (Beta),
  Claude Code ohnehin.

## API-Regeln

- REST unter `/api`, JSON rein/raus. `GET /healthz` für den Healthcheck.
- Schreib-Endpunkte (POST/PUT/DELETE) nur mit `Content-Type: application/json`,
  sonst 415 — Schutz gegen Cross-Site-Form-Posts, da die App selbst keine Auth hat.
- Ausnahme Import-Upload (multipart): verlangt `X-Requested-With: XMLHttpRequest`.
- `GET /api/tree` liefert alle Bereiche; Filtern nach Kunde und Suche laufen im Browser.

## Betrieb

- Env: `SAPFAV_DB` (Default `/data/tcodes.db`), `SAPFAV_PORT` (Default `8000`),
  `SAPFAV_MCP_TOKEN` (leer = MCP-Schnittstelle aus).
- Healthcheck im Container über `sapfav healthcheck` (distroless hat kein curl).
- CI: Push auf `main` → `go vet`, `go test`, dann Image nach
  `ghcr.io/maxkhl/sapfav` (`:latest`, `:<short-sha>`), amd64+arm64 per
  Go-Cross-Compile, kein QEMU.

## Arbeitsweise

- Keine Features einbauen, die nicht vereinbart sind — Ideen als Vorschlag nennen.
- Drag & Drop ist bewusst nicht in v1.
- In kleinen Schritten committen; jeder Commit muss für sich bauen und testen.
- Frontend-Formulare: Felder immer über `form.elements.<name>` ansprechen —
  `form.title`, `form.encoding`, `form.name` usw. sind eingebaute Eigenschaften.
