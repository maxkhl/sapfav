package sapfile

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func toWriteNodes(tree []*TreeNode) []*WriteNode {
	out := make([]*WriteNode, 0, len(tree))
	for _, t := range tree {
		out = append(out, &WriteNode{Kind: t.Kind, TCode: t.TCode, Title: t.Title, Children: toWriteNodes(t.Children)})
	}
	return out
}

func sameTree(t *testing.T, want, got []*TreeNode, path string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: %d Knoten, want %d", path, len(got), len(want))
		return
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Kind != g.Kind || w.TCode != g.TCode || w.Title != g.Title {
			t.Errorf("%s[%d]: got %s %q %q, want %s %q %q", path, i, g.Kind, g.TCode, g.Title, w.Kind, w.TCode, w.Title)
			continue
		}
		sameTree(t, w.Children, g.Children, path+"/"+w.Title)
	}
}

// Import → Export → Import muss denselben Baum ergeben, ohne Fallback-Zeilen.
func TestEncodeRoundTripRealExport(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := Parse(data, EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}

	enc, err := Encode(toWriteNodes(orig.Tree()))
	if err != nil {
		t.Fatal(err)
	}
	if enc.Folders != 15 || enc.Transactions != 222 || len(enc.Warnings) != 0 {
		t.Errorf("Encode: %d Ordner, %d Transaktionen, Warnungen %+v", enc.Folders, enc.Transactions, enc.Warnings)
	}

	lines := bytes.Split(enc.Data, []byte("\r\n"))
	if last := lines[len(lines)-1]; len(last) != 0 {
		t.Errorf("letzte Zeile ohne CRLF: %q", last)
	}
	lines = lines[:len(lines)-1]
	if len(lines) != 237 {
		t.Errorf("Zeilen: %d, want 237", len(lines))
	}
	for i, l := range lines {
		if len(l) != 192 || bytes.ContainsRune(l, '\n') {
			t.Errorf("Zeile %d: %d Bytes, want 192", i+1, len(l))
		}
	}
	if !bytes.Contains(enc.Data, []byte("Verf\xfcgbarkeits\xfcbersicht")) {
		t.Error("Umlaute nicht als cp1252 geschrieben")
	}

	again, err := Parse(enc.Data, EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}
	if again.Fallbacks != 0 || len(again.Unparsable) != 0 || len(again.Orphans) != 0 {
		t.Errorf("Re-Import: Fallbacks %d, unparsbar %+v, Waisen %+v", again.Fallbacks, again.Unparsable, again.Orphans)
	}
	sameTree(t, orig.Tree(), again.Tree(), "")
}

func TestEncodeWarnings(t *testing.T) {
	long := strings.Repeat("x", 140)
	enc, err := Encode([]*WriteNode{
		{Kind: KindFolder, Title: "Kunde → Basis", Children: []*WriteNode{
			{Kind: KindTx, TCode: "SE16", Title: long},
			{Kind: KindTx, TCode: strings.Repeat("Z", 49), Title: "zu langer T-Code"},
			{Kind: KindTx, TCode: "SM37", Title: "Zeile 1\nZeile 2 – ok"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if enc.Folders != 1 || enc.Transactions != 2 {
		t.Errorf("%d Ordner, %d Transaktionen, want 1 / 2", enc.Folders, enc.Transactions)
	}
	reasons := []string{"außerhalb von cp1252", "gekürzt", "nicht exportiert"}
	if len(enc.Warnings) != len(reasons) {
		t.Fatalf("Warnungen: %+v", enc.Warnings)
	}
	for i, r := range reasons {
		if !strings.Contains(enc.Warnings[i].Reason, r) {
			t.Errorf("Warnung %d: %q, want %q", i, enc.Warnings[i].Reason, r)
		}
	}

	res, err := Parse(enc.Data, EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}
	tree := res.Tree()
	if len(tree) != 1 || tree[0].Title != "Kunde ? Basis" || len(tree[0].Children) != 2 {
		t.Fatalf("Baum: %+v", tree)
	}
	if got := tree[0].Children[0].Title; got != strings.Repeat("x", 132) {
		t.Errorf("gekürzter Titel hat %d Zeichen", len(got))
	}
	// Gedankenstrich gibt es in cp1252, Zeilenumbruch wird zum Leerzeichen.
	if got := tree[0].Children[1].Title; got != "Zeile 1 Zeile 2 – ok" {
		t.Errorf("Titel mit Umbruch: %q", got)
	}
}
