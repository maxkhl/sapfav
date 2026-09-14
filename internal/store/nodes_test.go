package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxkhl/sapfav/internal/sapimport"
)

var ctx = context.Background()

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ptr[T any](v T) *T { return &v }

func mustCreate(t *testing.T, s *Store, parent *int64, kind, tcode, title string) *Node {
	t.Helper()
	in := NodeInput{ParentID: parent, Kind: kind, Title: title}
	if tcode != "" {
		in.TCode = &tcode
	}
	n, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create %s: %v", title, err)
	}
	return n
}

// order liefert die Titel der Kinder eines Parents in Reihenfolge.
func order(t *testing.T, s *Store, parent *int64) []string {
	t.Helper()
	ids, err := func() ([]int64, error) {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		return childIDs(ctx, tx, parent)
	}()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for i, id := range ids {
		n, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if n.SortIndex != i {
			t.Errorf("%s: sort_index %d, want %d", n.Title, n.SortIndex, i)
		}
		titles = append(titles, n.Title)
	}
	return titles
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSchemaVersionAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, nil, KindFolder, "", "Admin")
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Errorf("user_version %d (%v)", v, err)
	}
	if tree, _ := s.Tree(ctx); len(tree) != 1 {
		t.Errorf("Daten nach erneutem Öffnen: %d", len(tree))
	}
}

func TestCreateValidation(t *testing.T) {
	s := newStore(t)
	tx := mustCreate(t, s, nil, KindTx, "SE16", "Data Browser")

	cases := []NodeInput{
		{Kind: KindTx, Title: "ohne T-Code"},
		{Kind: KindTx, TCode: ptr("SE 16"), Title: "Leerzeichen"},
		{Kind: KindFolder, Title: "  "},
		{Kind: KindFolder, TCode: ptr("SE16"), Title: "Ordner mit T-Code"},
		{Kind: "link", Title: "falsche Art"},
		{Kind: KindTx, TCode: ptr("SM37"), Title: "Parent ist Transaktion", ParentID: &tx.ID},
		{Kind: KindTx, TCode: ptr("SM37"), Title: "Parent fehlt", ParentID: ptr[int64](999)},
	}
	for _, in := range cases {
		if _, err := s.Create(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", in.Title, err)
		}
	}

	n, err := s.Create(ctx, NodeInput{Kind: KindTx, TCode: ptr(" SM37 "), Title: " Jobs ", Description: ptr("  ")})
	if err != nil {
		t.Fatal(err)
	}
	if *n.TCode != "SM37" || n.Title != "Jobs" || n.Description != nil || n.SortIndex != 1 {
		t.Errorf("nicht normalisiert: %+v", n)
	}
}

func TestUpdate(t *testing.T) {
	s := newStore(t)
	f := mustCreate(t, s, nil, KindFolder, "", "Admin")
	n, err := s.Update(ctx, f.ID, NodeInput{Title: "Basis", Description: ptr("Zeile 1\nZeile 2"), Kind: KindTx})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != KindFolder || n.Title != "Basis" || *n.Description != "Zeile 1\nZeile 2" {
		t.Errorf("%+v", n)
	}
	if _, err := s.Update(ctx, f.ID, NodeInput{Title: "x", TCode: ptr("SE16")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("T-Code an Ordner: %v", err)
	}
	if _, err := s.Update(ctx, 999, NodeInput{Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unbekannte ID: %v", err)
	}
}

func TestMoveAndReorder(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, nil, KindFolder, "", "A")
	b := mustCreate(t, s, nil, KindFolder, "", "B")
	sub := mustCreate(t, s, &a.ID, KindFolder, "", "A/sub")
	x := mustCreate(t, s, &a.ID, KindTx, "SE16", "x")
	mustCreate(t, s, &b.ID, KindTx, "SM37", "y")

	if _, err := s.Move(ctx, a.ID, &sub.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("Ordner in eigenen Unterordner: %v", err)
	}
	if _, err := s.Move(ctx, a.ID, &a.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("Ordner in sich selbst: %v", err)
	}
	if _, err := s.Move(ctx, b.ID, &x.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("in Transaktion: %v", err)
	}

	if _, err := s.Move(ctx, sub.ID, &b.ID); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s, &a.ID); !equal(got, []string{"x"}) {
		t.Errorf("Quelle: %v", got)
	}
	if got := order(t, s, &b.ID); !equal(got, []string{"y", "A/sub"}) {
		t.Errorf("Ziel: %v", got)
	}

	if _, err := s.Move(ctx, x.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s, nil); !equal(got, []string{"A", "B", "x"}) {
		t.Errorf("oberste Ebene: %v", got)
	}

	if err := s.Reorder(ctx, x.ID, DirectionUp); err != nil {
		t.Fatal(err)
	}
	if err := s.Reorder(ctx, a.ID, DirectionUp); err != nil { // am Rand: nichts
		t.Fatal(err)
	}
	if got := order(t, s, nil); !equal(got, []string{"A", "x", "B"}) {
		t.Errorf("nach Reorder: %v", got)
	}
	if err := s.Reorder(ctx, a.ID, "left"); !errors.Is(err, ErrInvalid) {
		t.Errorf("falsche Richtung: %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	mustCreate(t, s, nil, KindTx, "SE16", "vorher")
	f := mustCreate(t, s, nil, KindFolder, "", "F")
	mustCreate(t, s, nil, KindTx, "SM37", "nachher")
	inner := mustCreate(t, s, &f.ID, KindFolder, "", "F/inner")
	mustCreate(t, s, &inner.ID, KindTx, "ST22", "tief")
	mustCreate(t, s, &f.ID, KindTx, "SU53", "F/tx")

	if err := s.Delete(ctx, f.ID, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("nicht-leerer Ordner ohne Wahl: %v", err)
	}
	if err := s.Delete(ctx, f.ID, "irgendwas"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ungültige Wahl: %v", err)
	}

	if err := s.Delete(ctx, f.ID, DeleteChildrenLift); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s, nil); !equal(got, []string{"vorher", "F/inner", "F/tx", "nachher"}) {
		t.Errorf("nach lift: %v", got)
	}
	if got := order(t, s, &inner.ID); !equal(got, []string{"tief"}) {
		t.Errorf("Enkel beim lift verloren: %v", got)
	}

	if err := s.Delete(ctx, inner.ID, DeleteChildrenDelete); err != nil {
		t.Fatal(err)
	}
	if got := order(t, s, nil); !equal(got, []string{"vorher", "F/tx", "nachher"}) {
		t.Errorf("nach delete: %v", got)
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count != 3 {
		t.Errorf("verbleibende Knoten %d, want 3 (Kaskade)", count)
	}
	if err := s.Delete(ctx, 999, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unbekannte ID: %v", err)
	}
}

func TestImportRealExport(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	res, err := sapimport.Parse(data, sapimport.EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	mustCreate(t, s, nil, KindFolder, "", "Bestand")

	count := func() (folders, txs int) {
		tree, err := s.Tree(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var walk func([]*Node)
		walk = func(level []*Node) {
			for _, n := range level {
				if n.Kind == KindFolder {
					folders++
				} else {
					txs++
				}
				walk(n.Children)
			}
		}
		walk(tree)
		return
	}

	stats, err := s.Import(ctx, res.Tree(), false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Folders != 15 || stats.Transactions != 222 {
		t.Errorf("stats %+v", stats)
	}
	if f, x := count(); f != 16 || x != 222 {
		t.Errorf("nach append: %d Ordner, %d Transaktionen", f, x)
	}
	if top := order(t, s, nil); top[0] != "Bestand" || len(top) != 1+len(res.Tree()) {
		t.Errorf("append nicht am Ende: %v", top)
	}

	if _, err := s.Import(ctx, res.Tree(), true); err != nil {
		t.Fatal(err)
	}
	if f, x := count(); f != 15 || x != 222 {
		t.Errorf("nach replace: %d Ordner, %d Transaktionen", f, x)
	}

	var title string
	s.db.QueryRow("SELECT title FROM nodes WHERE tcode = 'CO09'").Scan(&title)
	if title != "Verfügbarkeitsübersicht" {
		t.Errorf("Umlaute: %q", title)
	}
}
