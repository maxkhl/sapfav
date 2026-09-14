package store

import (
	"errors"
	"os"
	"testing"

	"github.com/maxkhl/sapfav/internal/sapfile"
)

func mustCustomer(t *testing.T, s *Store, name string) *Customer {
	t.Helper()
	c, err := s.CreateCustomer(ctx, CustomerInput{Name: name})
	if err != nil {
		t.Fatalf("CreateCustomer %s: %v", name, err)
	}
	return c
}

func TestCustomerCRUD(t *testing.T) {
	s := newStore(t)
	c, err := s.CreateCustomer(ctx, CustomerInput{Name: " Müller AG ", Notes: ptr("S4P/100, S4Q/200")})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Müller AG" || *c.Notes != "S4P/100, S4Q/200" || c.NodeCount != 0 {
		t.Errorf("%+v", c)
	}
	mustCustomer(t, s, "ACME")

	if _, err := s.CreateCustomer(ctx, CustomerInput{Name: "acme"}); !errors.Is(err, ErrConflict) {
		t.Errorf("doppelter Name (Groß/klein): %v", err)
	}
	if _, err := s.CreateCustomer(ctx, CustomerInput{Name: "  "}); !errors.Is(err, ErrInvalid) {
		t.Errorf("leerer Name: %v", err)
	}
	if _, err := s.UpdateCustomer(ctx, c.ID, CustomerInput{Name: "ACME"}); !errors.Is(err, ErrConflict) {
		t.Errorf("Umbenennen auf vorhandenen Namen: %v", err)
	}

	u, err := s.UpdateCustomer(ctx, c.ID, CustomerInput{Name: "Müller GmbH", Notes: ptr(" ")})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Müller GmbH" || u.Notes != nil {
		t.Errorf("%+v", u)
	}
	if _, err := s.UpdateCustomer(ctx, c.ID, CustomerInput{Name: "Müller GmbH"}); err != nil {
		t.Errorf("eigenen Namen behalten: %v", err)
	}
	if _, err := s.UpdateCustomer(ctx, 999, CustomerInput{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unbekannter Kunde: %v", err)
	}

	mustCreateIn(t, s, &c.ID, nil, KindTx, "ZSD084", "Case Kalkulation")
	list, err := s.Customers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "ACME" || list[1].Name != "Müller GmbH" || list[1].NodeCount != 1 {
		t.Errorf("Liste: %+v %+v", list[0], list[1])
	}
}

func TestScopes(t *testing.T) {
	s := newStore(t)
	c := mustCustomer(t, s, "ACME")
	d := mustCustomer(t, s, "Beta")

	g := mustCreateIn(t, s, nil, nil, KindFolder, "", "G")
	cf := mustCreateIn(t, s, &c.ID, nil, KindFolder, "", "C-Ordner")
	ct := mustCreateIn(t, s, &c.ID, nil, KindTx, "ZSD084", "C-tx")
	if g.SortIndex != 0 || cf.SortIndex != 0 || ct.SortIndex != 1 {
		t.Errorf("sort_index je Bereich: G %d, C-Ordner %d, C-tx %d", g.SortIndex, cf.SortIndex, ct.SortIndex)
	}

	inner := mustCreateIn(t, s, nil, &cf.ID, KindTx, "VA03", "erbt Kunde")
	if inner.CustomerID == nil || *inner.CustomerID != c.ID {
		t.Errorf("Kind erbt Kunde nicht: %+v", inner)
	}
	if _, err := s.Create(ctx, NodeInput{CustomerID: &d.ID, ParentID: &cf.ID, Kind: KindTx, TCode: ptr("SE16"), Title: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Kunde passt nicht zum Ordner: %v", err)
	}
	if _, err := s.Move(ctx, ct.ID, &g.ID, &c.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("Move mit abweichendem Kunden: %v", err)
	}

	if got := orderIn(t, s, &c.ID, nil); !equal(got, []string{"C-Ordner", "C-tx"}) {
		t.Errorf("oben bei C: %v", got)
	}
	if got := orderIn(t, s, nil, nil); !equal(got, []string{"G"}) {
		t.Errorf("oben Allgemein: %v", got)
	}

	// Ordner mit Inhalt von Kunde C nach Allgemein: Unterbaum zieht mit.
	moved, err := s.Move(ctx, cf.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved.CustomerID != nil {
		t.Errorf("Ordner noch bei Kunde: %+v", moved)
	}
	if n, _ := s.Get(ctx, inner.ID); n.CustomerID != nil {
		t.Errorf("Inhalt nicht mitgezogen: %+v", n)
	}
	if got := orderIn(t, s, nil, nil); !equal(got, []string{"G", "C-Ordner"}) {
		t.Errorf("Allgemein nach Move: %v", got)
	}
	if got := orderIn(t, s, &c.ID, nil); !equal(got, []string{"C-tx"}) {
		t.Errorf("C nach Move: %v", got)
	}

	// Transaktion aus Allgemein in einen Ordner von Kunde D.
	df := mustCreateIn(t, s, &d.ID, nil, KindFolder, "", "D-Ordner")
	if n, err := s.Move(ctx, inner.ID, &df.ID, nil); err != nil || n.CustomerID == nil || *n.CustomerID != d.ID {
		t.Errorf("Move in Kundenordner: %+v %v", n, err)
	}

	// Reihenfolge oben bleibt je Bereich getrennt.
	if err := s.Reorder(ctx, ct.ID, DirectionUp); err != nil {
		t.Fatal(err)
	}
	if got := orderIn(t, s, &c.ID, nil); !equal(got, []string{"C-tx"}) {
		t.Errorf("Reorder über Bereichsgrenze: %v", got)
	}

	if n, _ := s.Count(ctx, &d.ID); n != 2 {
		t.Errorf("Count D: %d", n)
	}
	if n, _ := s.Count(ctx, nil); n != 2 {
		t.Errorf("Count Allgemein: %d", n)
	}
}

func TestDeleteCustomer(t *testing.T) {
	s := newStore(t)
	mustCreateIn(t, s, nil, nil, KindTx, "SE16", "allgemein")
	c := mustCustomer(t, s, "ACME")
	f := mustCreateIn(t, s, &c.ID, nil, KindFolder, "", "C-Ordner")
	mustCreateIn(t, s, &c.ID, &f.ID, KindTx, "ZSD084", "C-innen")
	mustCreateIn(t, s, &c.ID, nil, KindTx, "ZMM002", "C-oben")

	if err := s.DeleteCustomer(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("Kunde mit Favoriten ohne Wahl: %v", err)
	}
	if err := s.DeleteCustomer(ctx, c.ID, "weg"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ungültige Wahl: %v", err)
	}
	if err := s.DeleteCustomer(ctx, c.ID, DeleteNodesGeneral); err != nil {
		t.Fatal(err)
	}
	if got := orderIn(t, s, nil, nil); !equal(got, []string{"allgemein", "C-Ordner", "C-oben"}) {
		t.Errorf("Allgemein nach Löschen: %v", got)
	}
	if n, _ := s.Count(ctx, nil); n != 4 {
		t.Errorf("Count Allgemein: %d, want 4", n)
	}
	if _, err := s.GetCustomer(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Kunde noch da: %v", err)
	}

	d := mustCustomer(t, s, "Beta")
	df := mustCreateIn(t, s, &d.ID, nil, KindFolder, "", "D-Ordner")
	mustCreateIn(t, s, &d.ID, &df.ID, KindTx, "VA03", "D-innen")
	if err := s.DeleteCustomer(ctx, d.ID, DeleteNodesDelete); err != nil {
		t.Fatal(err)
	}
	if f, x := countTree(t, s); f != 1 || x != 3 {
		t.Errorf("nach Mitlöschen: %d Ordner, %d Transaktionen", f, x)
	}

	e := mustCustomer(t, s, "Leer")
	if err := s.DeleteCustomer(ctx, e.ID, ""); err != nil {
		t.Errorf("leerer Kunde ohne Wahl: %v", err)
	}
}

func TestImportIntoCustomer(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	res, err := sapfile.Parse(data, sapfile.EncodingCP1252)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	c := mustCustomer(t, s, "ACME")
	mustCreateIn(t, s, nil, nil, KindTx, "SE16", "allgemein")

	if _, err := s.Import(ctx, res.Tree(), &c.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, res.Tree(), &c.ID, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx, &c.ID); n != 237 {
		t.Errorf("Kunde nach replace: %d, want 237", n)
	}
	if n, _ := s.Count(ctx, nil); n != 1 {
		t.Errorf("Allgemein von replace betroffen: %d", n)
	}
	if _, err := s.Import(ctx, res.Tree(), ptr[int64](999), false); !errors.Is(err, ErrInvalid) {
		t.Errorf("unbekannter Kunde: %v", err)
	}
}
