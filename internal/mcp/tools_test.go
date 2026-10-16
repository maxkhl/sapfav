package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/maxkhl/sapfav/internal/sapfile"
	"github.com/maxkhl/sapfav/internal/store"
)

func newServerWithStore(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

// runTool ruft ein Tool über den HTTP-Endpunkt auf und liefert das
// strukturierte Ergebnis bzw. bei isError den Fehlertext.
func runTool(t *testing.T, s *Server, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	rec, out := modern(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Fatalf("%s: %d %+v", name, rec.Code, out.Error)
	}
	var res struct {
		IsError           bool           `json:"isError"`
		StructuredContent map[string]any `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		if len(res.Content) == 0 {
			t.Fatalf("%s: isError ohne Text", name)
		}
		return nil, res.Content[0].Text
	}
	return res.StructuredContent, ""
}

func ok(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	out, errText := runTool(t, s, name, args)
	if errText != "" {
		t.Fatalf("%s: %s", name, errText)
	}
	return out
}

func num(t *testing.T, v any) int64 {
	t.Helper()
	f, isFloat := v.(float64)
	if !isFloat {
		t.Fatalf("keine Zahl: %#v", v)
	}
	return int64(f)
}

// nodesOf liest die Knoten eines Bereichs aus einem get_tree-Ergebnis.
func nodesOf(t *testing.T, tree map[string]any, index int) []any {
	t.Helper()
	scopes, isList := tree["scopes"].([]any)
	if !isList || len(scopes) <= index {
		t.Fatalf("scopes: %#v", tree["scopes"])
	}
	scope, _ := scopes[index].(map[string]any)
	nodes, _ := scope["nodes"].([]any)
	return nodes
}

func titlesOf(t *testing.T, nodes []any) []string {
	t.Helper()
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		item, _ := n.(map[string]any)
		title, _ := item["title"].(string)
		out = append(out, title)
	}
	return out
}

func TestToolsWriteAndRead(t *testing.T) {
	s, _ := newServerWithStore(t)

	customer := ok(t, s, "create_customer", map[string]any{"name": "ACME", "notes": "S4P/100"})
	cid := num(t, customer["id"])
	if customer["name"] != "ACME" || customer["notes"] != "S4P/100" {
		t.Errorf("Kunde: %#v", customer)
	}

	folder := ok(t, s, "create_node", map[string]any{"kind": "folder", "title": "Basis", "customer_id": cid})
	fid := num(t, folder["id"])
	if num(t, folder["customer_id"]) != cid || folder["parent_id"] != nil {
		t.Errorf("Ordner: %#v", folder)
	}

	tx := ok(t, s, "create_node", map[string]any{
		"kind": "tx", "tcode": "VA03", "title": "Kundenauftrag anzeigen", "parent_id": fid,
	})
	txID := num(t, tx["id"])
	if num(t, tx["customer_id"]) != cid {
		t.Errorf("Bereich nicht vom Ordner geerbt: %#v", tx)
	}

	// Teilweises Ändern: nur die Beschreibung.
	updated := ok(t, s, "update_node", map[string]any{"id": txID, "description": "Variante /MEINE"})
	if updated["title"] != "Kundenauftrag anzeigen" || updated["tcode"] != "VA03" ||
		updated["description"] != "Variante /MEINE" {
		t.Errorf("nach update: %#v", updated)
	}
	// Leere Beschreibung löscht sie.
	updated = ok(t, s, "update_node", map[string]any{"id": txID, "description": ""})
	if _, stillThere := updated["description"]; stillThere {
		t.Errorf("Beschreibung nicht gelöscht: %#v", updated)
	}

	list := ok(t, s, "list_customers", nil)
	customers, _ := list["customers"].([]any)
	if num(t, list["general_count"]) != 0 || len(customers) != 1 {
		t.Fatalf("list_customers: %#v", list)
	}
	first, _ := customers[0].(map[string]any)
	if num(t, first["node_count"]) != 2 {
		t.Errorf("node_count: %#v", first)
	}

	tree := ok(t, s, "get_tree", map[string]any{"scope": "customer", "customer_id": cid})
	nodes := nodesOf(t, tree, 0)
	if len(nodes) != 1 {
		t.Fatalf("Kundenbaum: %#v", nodes)
	}
	top, _ := nodes[0].(map[string]any)
	children, _ := top["children"].([]any)
	if top["title"] != "Basis" || len(children) != 1 {
		t.Errorf("Baum: %#v", top)
	}

	hits := ok(t, s, "search", map[string]any{"query": "va03"})
	if num(t, hits["count"]) != 1 {
		t.Fatalf("Suche: %#v", hits)
	}
	hitList, _ := hits["hits"].([]any)
	hit, _ := hitList[0].(map[string]any)
	if hit["path"] != "ACME / Basis" || hit["tcode"] != "VA03" {
		t.Errorf("Treffer: %#v", hit)
	}

	// Ordner mit Inhalt nach Allgemein verschieben.
	moved := ok(t, s, "move_node", map[string]any{"id": fid, "parent_id": nil, "customer_id": nil})
	if moved["customer_id"] != nil {
		t.Errorf("nach move: %#v", moved)
	}
	general := ok(t, s, "get_tree", map[string]any{"scope": "general"})
	if got := titlesOf(t, nodesOf(t, general, 0)); len(got) != 1 || got[0] != "Basis" {
		t.Errorf("Allgemein: %v", got)
	}

	// Nicht-leerer Ordner braucht eine Entscheidung.
	if _, errText := runTool(t, s, "delete_node", map[string]any{"id": fid}); errText == "" {
		t.Error("Löschen ohne children hätte scheitern müssen")
	}
	ok(t, s, "delete_node", map[string]any{"id": fid, "children": "lift"})
	general = ok(t, s, "get_tree", map[string]any{"scope": "general"})
	if got := titlesOf(t, nodesOf(t, general, 0)); len(got) != 1 || got[0] != "Kundenauftrag anzeigen" {
		t.Errorf("nach lift: %v", got)
	}

	ok(t, s, "update_customer", map[string]any{"id": cid, "name": "ACME AG"})
	ok(t, s, "delete_customer", map[string]any{"id": cid})
	if list := ok(t, s, "list_customers", nil); len(list["customers"].([]any)) != 0 {
		t.Errorf("Kunde nicht gelöscht: %#v", list)
	}
}

// Sortieren einer echten Ebene: der Export hat 88 Einträge oben.
func TestToolsSetOrderWithRealExport(t *testing.T) {
	s, st := newServerWithStore(t)
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sapfile.Parse(data, sapfile.EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Import(context.Background(), parsed.Tree(), nil, true); err != nil {
		t.Fatal(err)
	}

	tree := ok(t, s, "get_tree", map[string]any{"scope": "general"})
	nodes := nodesOf(t, tree, 0)
	count := 0
	var walk func([]any)
	walk = func(level []any) {
		for _, n := range level {
			count++
			item, _ := n.(map[string]any)
			kids, _ := item["children"].([]any)
			walk(kids)
		}
	}
	walk(nodes)
	if count != 237 {
		t.Errorf("get_tree liefert %d Knoten, want 237", count)
	}

	// Umlaute müssen durch den JSON-Weg unverändert ankommen.
	hits := ok(t, s, "search", map[string]any{"query": "CO09"})
	hitList, _ := hits["hits"].([]any)
	if len(hitList) != 1 {
		t.Fatalf("CO09: %#v", hits)
	}
	hit, _ := hitList[0].(map[string]any)
	if hit["title"] != "Verfügbarkeitsübersicht" || !strings.HasPrefix(hit["path"].(string), "Allgemein") {
		t.Errorf("Treffer: %#v", hit)
	}

	// Oberste Ebene alphabetisch sortieren.
	type entry struct {
		id    int64
		title string
	}
	level := make([]entry, 0, len(nodes))
	for _, n := range nodes {
		item, _ := n.(map[string]any)
		title, _ := item["title"].(string)
		level = append(level, entry{num(t, item["id"]), title})
	}
	sort.Slice(level, func(i, j int) bool {
		return strings.ToLower(level[i].title) < strings.ToLower(level[j].title)
	})
	ids := make([]any, 0, len(level))
	want := make([]string, 0, len(level))
	for _, e := range level {
		ids = append(ids, e.id)
		want = append(want, e.title)
	}

	out := ok(t, s, "set_children_order", map[string]any{"parent_id": nil, "ids": ids})
	ordered, _ := out["ordered"].([]any)
	if len(ordered) != len(ids) {
		t.Fatalf("ordered: %#v", out)
	}
	tree = ok(t, s, "get_tree", map[string]any{"scope": "general"})
	got := titlesOf(t, nodesOf(t, tree, 0))
	if len(got) != len(want) {
		t.Fatalf("%d Einträge oben, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Position %d: %q, want %q", i, got[i], want[i])
		}
	}

	// Begrenzte Trefferzahl wird gemeldet.
	hits = ok(t, s, "search", map[string]any{"query": "a", "limit": 5})
	if len(hits["hits"].([]any)) != 5 || hits["truncated"] != true || num(t, hits["count"]) <= 5 {
		t.Errorf("limit: %#v", hits)
	}
}

func TestToolArgumentErrors(t *testing.T) {
	s, _ := newServerWithStore(t)
	folder := ok(t, s, "create_node", map[string]any{"kind": "folder", "title": "F"})
	fid := num(t, folder["id"])

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"falsche Art", "create_node", map[string]any{"kind": "link", "title": "x"}, "kind muss"},
		{"T-Code fehlt", "create_node", map[string]any{"kind": "tx", "title": "x"}, "T-Code fehlt"},
		{"unbekanntes Feld", "create_node", map[string]any{"kind": "folder", "title": "x", "farbe": "rot"}, "ungültige Argumente"},
		{"unbekannter Kunde", "create_node", map[string]any{"kind": "folder", "title": "x", "customer_id": 999}, "Kunde 999"},
		{"parent_id fehlt", "move_node", map[string]any{"id": fid}, "parent_id muss angegeben werden"},
		{"Ordner in sich selbst", "move_node", map[string]any{"id": fid, "parent_id": fid}, "sich selbst"},
		{"leere Suche", "search", map[string]any{"query": "   "}, "query ist leer"},
		{"limit zu groß", "search", map[string]any{"query": "a", "limit": 500}, "limit muss"},
		{"scope ohne Kunde", "get_tree", map[string]any{"scope": "customer"}, "braucht customer_id"},
		{"falscher scope", "get_tree", map[string]any{"scope": "irgendwas"}, "scope muss"},
		{"unbekannte ID", "update_node", map[string]any{"id": 999, "title": "x"}, "nicht gefunden"},
		{"ids passen nicht", "set_children_order", map[string]any{"parent_id": nil, "ids": []any{999}}, "gehört nicht zu dieser Ebene"},
		{"ids-Anzahl falsch", "set_children_order", map[string]any{"parent_id": nil, "ids": []any{}}, "angegeben sind 0"},
		{"doppelter Kundenname", "create_customer", map[string]any{"name": ""}, "Kundenname fehlt"},
		{"negative ID", "update_node", map[string]any{"id": -1, "title": "x"}, "nicht gefunden"},
	}
	for _, c := range cases {
		_, errText := runTool(t, s, c.tool, c.args)
		if errText == "" {
			t.Errorf("%s: kein Fehler gemeldet", c.name)
			continue
		}
		if !strings.Contains(errText, c.want) {
			t.Errorf("%s: %q enthält nicht %q", c.name, errText, c.want)
		}
	}
}

func TestToolsDeleteNodesAndDuplicates(t *testing.T) {
	s, _ := newServerWithStore(t)
	customer := ok(t, s, "create_customer", map[string]any{"name": "ACME"})
	cid := num(t, customer["id"])

	folder := ok(t, s, "create_node", map[string]any{"kind": "folder", "title": "Vertrieb"})
	fid := num(t, folder["id"])
	oben := ok(t, s, "create_node", map[string]any{"kind": "tx", "tcode": "VA03", "title": "Auftrag oben"})
	innen := ok(t, s, "create_node", map[string]any{"kind": "tx", "tcode": "va03", "title": "Auftrag im Ordner", "parent_id": fid})
	ok(t, s, "create_node", map[string]any{"kind": "tx", "tcode": "SE16", "title": "Data Browser"})
	beimKunden := ok(t, s, "create_node", map[string]any{"kind": "tx", "tcode": "VA03", "title": "Auftrag Kunde", "customer_id": cid})

	// Groß- und Kleinschreibung des T-Codes darf keine Rolle spielen.
	dups := ok(t, s, "find_duplicates", nil)
	if num(t, dups["group_count"]) != 1 || num(t, dups["extra_count"]) != 2 {
		t.Fatalf("Dubletten: %#v", dups)
	}
	groups, _ := dups["groups"].([]any)
	group, _ := groups[0].(map[string]any)
	entries, _ := group["entries"].([]any)
	if group["tcode"] != "VA03" || len(entries) != 3 {
		t.Fatalf("Gruppe: %#v", group)
	}
	paths := map[string]bool{}
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		paths[entry["path"].(string)] = true
	}
	if !paths["Allgemein"] || !paths["Allgemein / Vertrieb"] || !paths["ACME"] {
		t.Errorf("Pfade: %#v", paths)
	}

	// Auf einen Bereich eingeschränkt bleibt nichts Doppeltes übrig.
	only := ok(t, s, "find_duplicates", map[string]any{"scope": "customer", "customer_id": cid})
	if num(t, only["group_count"]) != 0 {
		t.Errorf("Kundenbereich: %#v", only)
	}

	// Fehlerfälle löschen nichts.
	for _, args := range []map[string]any{
		{"ids": []any{num(t, innen["id"]), 999}},
		{"ids": []any{}},
		{"ids": []any{fid}},
	} {
		if _, errText := runTool(t, s, "delete_nodes", args); errText == "" {
			t.Errorf("kein Fehler für %#v", args)
		}
	}
	if tree := ok(t, s, "get_tree", map[string]any{"scope": "general"}); len(nodesOf(t, tree, 0)) != 3 {
		t.Fatalf("trotz Fehler gelöscht: %#v", tree)
	}

	// Stapellöschen: Ordner mit Inhalt plus zwei Einzelne, in einem Aufruf.
	out := ok(t, s, "delete_nodes", map[string]any{
		"ids":      []any{fid, num(t, innen["id"]), num(t, oben["id"]), num(t, beimKunden["id"])},
		"children": "delete",
	})
	if num(t, out["count"]) != 3 {
		t.Errorf("count: %#v", out)
	}
	if already, _ := out["already_removed"].([]any); len(already) != 1 || num(t, already[0]) != num(t, innen["id"]) {
		t.Errorf("already_removed: %#v", out)
	}
	if got := titlesOf(t, nodesOf(t, ok(t, s, "get_tree", map[string]any{"scope": "general"}), 0)); len(got) != 1 || got[0] != "Data Browser" {
		t.Errorf("Allgemein danach: %v", got)
	}
	if num(t, ok(t, s, "find_duplicates", nil)["group_count"]) != 0 {
		t.Error("nach dem Aufräumen gibt es noch Dubletten")
	}
}
