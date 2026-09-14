package sapimport

import (
	"os"
	"strings"
	"testing"
)

func TestParseRealExport(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Parse(data, EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}

	if got := len(res.Nodes); got != 237 {
		t.Errorf("Knoten: got %d, want 237", got)
	}
	if res.Folders != 15 {
		t.Errorf("Ordner: got %d, want 15", res.Folders)
	}
	if res.Transactions != 222 {
		t.Errorf("Transaktionen: got %d, want 222", res.Transactions)
	}
	if len(res.Unparsable) != 0 {
		t.Errorf("unparsbare Zeilen: %+v", res.Unparsable)
	}
	if len(res.Orphans) != 0 {
		t.Errorf("Waisen: %+v", res.Orphans)
	}
	if res.Fallbacks != 0 {
		t.Errorf("Fallback-Zeilen im sauberen Export: %d", res.Fallbacks)
	}

	seen := map[string]bool{}
	var walk func(level []*TreeNode, depth int)
	walk = func(level []*TreeNode, depth int) {
		for _, n := range level {
			if seen[n.ID] {
				t.Errorf("ID %s doppelt erreicht", n.ID)
			}
			seen[n.ID] = true
			if n.Kind == KindTx && len(n.Children) > 0 {
				t.Errorf("Transaktion %s hat Kinder", n.ID)
			}
			walk(n.Children, depth+1)
		}
	}
	walk(res.Tree(), 0)
	if len(seen) != 237 {
		t.Errorf("ab oberster Ebene erreichbar: %d, want 237", len(seen))
	}

	// Stichproben: Umlaute aus cp1252 und verschachtelte Ordner.
	byID := map[string]*Node{}
	for _, n := range res.Nodes {
		byID[n.ID] = n
	}
	if n := byID["00318"]; n == nil || n.TCode != "CO09" || n.Title != "Verfügbarkeitsübersicht" {
		t.Errorf("00318: %+v", n)
	}
	if n := byID["00286"]; n == nil || n.TCode != "/IWFND/MAINT_SERVICE" {
		t.Errorf("00286: %+v", n)
	}
	if n := byID["00157"]; n == nil || n.Kind != KindFolder || n.Title != "Berechtigung" || n.ParentID != "00152" {
		t.Errorf("00157: %+v", n)
	}
}

func TestParseUTF8RejectsCP1252(t *testing.T) {
	if _, err := Parse([]byte("TR0000100002SE16 \xc4ndern"), EncodingUTF8); err == nil {
		t.Error("ungültiges UTF-8 wurde akzeptiert")
	}
	if _, err := Parse(nil, "latin-9"); err == nil {
		t.Error("unbekanntes Encoding wurde akzeptiert")
	}
}

func TestParseUTF8WithBOM(t *testing.T) {
	res, err := Parse([]byte("\xef\xbb\xbfTR0000100002SE16 Data Browser für Tabellen"), EncodingUTF8)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Title != "Data Browser für Tabellen" {
		t.Errorf("%+v", res.Nodes)
	}
}

// col baut eine Zeile im festen Spaltenformat.
func col(prefix, parent, id, tcode, title string) string {
	return prefix + parent + id + tcode + strings.Repeat(" ", 48-len(tcode)) + title
}

func TestParseFallbackAndProblems(t *testing.T) {
	lines := []string{
		col("  ", "00001", "00010", "", "Ordner"),
		col("TR", "00010", "00011", "SE16", "Data Browser"),
		"TR0001000012SM37 Jobübersicht",                // kurz, Spalten verrutscht
		"TR 00010 00013 ST22",                          // Leerraum, ohne Kurztext
		"00001 00014    Ordner ohne Einrückung",        // führende Leerzeichen verloren
		"",                                             // leer: ignorieren
		"Kopfzeile ohne IDs",                           // unparsbar
		"TR0001000015",                                 // T-Code fehlt
		col("TR", "00010", "00011", "SE11", "Doppelt"), // doppelte ID
		col("TR", "00099", "00016", "SU53", "Parent fehlt"),
		col("TR", "00011", "00017", "SU01", "Parent ist Transaktion"),
		col("  ", "00019", "00018", "", "Kreis A"),
		col("  ", "00018", "00019", "", "Kreis B"),
		col("TR", "00019", "00020", "PFCG", "im Kreis"),
	}
	res, err := Parse([]byte(strings.Join(lines, "\r\n")), EncodingUTF8)
	if err != nil {
		t.Fatal(err)
	}

	if res.Folders != 4 || res.Transactions != 6 {
		t.Errorf("Ordner %d / Transaktionen %d, want 4 / 6", res.Folders, res.Transactions)
	}
	if res.Fallbacks != 3 {
		t.Errorf("Fallbacks: %d, want 3", res.Fallbacks)
	}

	byID := map[string]*Node{}
	for _, n := range res.Nodes {
		byID[n.ID] = n
	}
	if n := byID["00012"]; n == nil || n.TCode != "SM37" || n.Title != "Jobübersicht" || !n.Fallback {
		t.Errorf("00012: %+v", n)
	}
	if n := byID["00013"]; n == nil || n.TCode != "ST22" || n.Title != "" {
		t.Errorf("00013: %+v", n)
	}
	if n := byID["00014"]; n == nil || n.Kind != KindFolder || n.Title != "Ordner ohne Einrückung" {
		t.Errorf("00014: %+v", n)
	}
	if n := byID["00011"]; n == nil || n.TCode != "SE16" {
		t.Errorf("erstes Vorkommen von 00011 muss gewinnen: %+v", n)
	}

	wantUnparsable := map[int]string{7: "weder", 8: "ohne T-Code", 9: "doppelte ID"}
	if len(res.Unparsable) != len(wantUnparsable) {
		t.Errorf("unparsbar: %+v", res.Unparsable)
	}
	for _, p := range res.Unparsable {
		if !strings.Contains(p.Reason, wantUnparsable[p.Line]) {
			t.Errorf("Zeile %d: Grund %q", p.Line, p.Reason)
		}
	}

	orphans := map[string]string{}
	for _, o := range res.Orphans {
		orphans[o.ID] = o.Reason
	}
	if len(orphans) != 3 || orphans["00016"] != "Parent-ID existiert nicht" ||
		orphans["00017"] != "Parent ist eine Transaktion" || orphans["00018"] != "Zyklus in der Hierarchie" {
		t.Errorf("Waisen: %+v", res.Orphans)
	}

	count := 0
	var walk func([]*TreeNode)
	walk = func(level []*TreeNode) {
		for _, n := range level {
			count++
			walk(n.Children)
		}
	}
	walk(res.Tree())
	if count != len(res.Nodes) {
		t.Errorf("erreichbar %d von %d", count, len(res.Nodes))
	}
}
