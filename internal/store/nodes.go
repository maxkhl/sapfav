package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/maxkhl/sapfav/internal/sapfile"
)

const (
	KindFolder = "folder"
	KindTx     = "tx"
)

// Node ist ein Ordner oder eine Transaktion. Children ist nur beim Baum gefüllt.
// CustomerID nil bedeutet Allgemein; unter einem Ordner gilt dessen Kunde.
type Node struct {
	ID          int64   `json:"id"`
	CustomerID  *int64  `json:"customer_id"`
	ParentID    *int64  `json:"parent_id"`
	Kind        string  `json:"kind"`
	TCode       *string `json:"tcode"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	SortIndex   int     `json:"sort_index"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Children    []*Node `json:"children,omitempty"`
}

// NodeInput sind die änderbaren Felder. CustomerID, Kind und ParentID zählen
// nur beim Anlegen; CustomerID nur, wenn kein ParentID angegeben ist.
type NodeInput struct {
	CustomerID  *int64  `json:"customer_id"`
	ParentID    *int64  `json:"parent_id"`
	Kind        string  `json:"kind"`
	TCode       *string `json:"tcode"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

const nodeColumns = "id, customer_id, parent_id, kind, tcode, title, description, sort_index, created_at, updated_at"

const now = "strftime('%Y-%m-%dT%H:%M:%SZ', 'now')"

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	err := row.Scan(&n.ID, &n.CustomerID, &n.ParentID, &n.Kind, &n.TCode, &n.Title, &n.Description,
		&n.SortIndex, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func getNode(ctx context.Context, q querier, id int64) (*Node, error) {
	n, err := scanNode(q.QueryRowContext(ctx, "SELECT "+nodeColumns+" FROM nodes WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: Knoten %d", ErrNotFound, id)
	}
	return n, err
}

func (s *Store) Get(ctx context.Context, id int64) (*Node, error) {
	return getNode(ctx, s.db, id)
}

// Count liefert die Anzahl der Knoten eines Bereichs (nil = Allgemein).
func (s *Store) Count(ctx context.Context, customerID *int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE customer_id IS ?", customerID).Scan(&n)
	return n, err
}

// Tree liefert alle Knoten aller Bereiche verschachtelt, sortiert nach sort_index.
// Die oberste Ebene enthält Knoten verschiedener Bereiche (customer_id).
func (s *Store) Tree(ctx context.Context) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+nodeColumns+" FROM nodes ORDER BY sort_index, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []*Node
	children := map[int64][]*Node{}
	top := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, n)
		if n.ParentID == nil {
			top = append(top, n)
		} else {
			children[*n.ParentID] = append(children[*n.ParentID], n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, n := range all {
		if n.Kind == KindFolder {
			n.Children = children[n.ID]
		}
	}
	return top, nil
}

// normalize prüft und bereinigt die Eingabe für die gegebene Art.
func normalize(kind string, in *NodeInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Description != nil {
		if d := strings.TrimSpace(*in.Description); d == "" {
			in.Description = nil
		} else {
			in.Description = &d
		}
	}
	var tcode string
	if in.TCode != nil {
		tcode = strings.TrimSpace(*in.TCode)
	}
	switch kind {
	case KindFolder:
		if tcode != "" {
			return fmt.Errorf("%w: Ordner haben keinen T-Code", ErrInvalid)
		}
		if in.Title == "" {
			return fmt.Errorf("%w: Ordnername fehlt", ErrInvalid)
		}
		in.TCode = nil
	case KindTx:
		if tcode == "" {
			return fmt.Errorf("%w: T-Code fehlt", ErrInvalid)
		}
		if strings.ContainsFunc(tcode, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }) {
			return fmt.Errorf("%w: T-Code darf keine Leerzeichen enthalten", ErrInvalid)
		}
		in.TCode = &tcode
	default:
		return fmt.Errorf("%w: kind muss 'folder' oder 'tx' sein", ErrInvalid)
	}
	return nil
}

func sameID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// resolveScope ermittelt den Bereich eines Ziels. Unter einem Ordner gilt dessen
// Kunde; ein abweichend angegebener Kunde ist ein Fehler. Oben gilt customerID.
func resolveScope(ctx context.Context, tx *sql.Tx, parentID, customerID *int64) (*int64, error) {
	if parentID == nil {
		return customerID, checkCustomer(ctx, tx, customerID)
	}
	p, err := getNode(ctx, tx, *parentID)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: Zielordner %d existiert nicht", ErrInvalid, *parentID)
	}
	if err != nil {
		return nil, err
	}
	if p.Kind != KindFolder {
		return nil, fmt.Errorf("%w: Ziel %d ist kein Ordner", ErrInvalid, *parentID)
	}
	if customerID != nil && !sameID(customerID, p.CustomerID) {
		return nil, fmt.Errorf("%w: Zielordner gehört zu einem anderen Kunden", ErrInvalid)
	}
	return p.CustomerID, nil
}

// levelWhere beschreibt eine Ebene: unter einem Ordner oder oben in einem Bereich.
func levelWhere(parentID, customerID *int64) (string, []any) {
	if parentID != nil {
		return "parent_id = ?", []any{*parentID}
	}
	return "parent_id IS NULL AND customer_id IS ?", []any{customerID}
}

func nextSortIndex(ctx context.Context, tx *sql.Tx, parentID, customerID *int64) (int, error) {
	where, args := levelWhere(parentID, customerID)
	var next int
	err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_index), -1) + 1 FROM nodes WHERE "+where, args...).Scan(&next)
	return next, err
}

func childIDs(ctx context.Context, tx *sql.Tx, parentID, customerID *int64) ([]int64, error) {
	where, args := levelWhere(parentID, customerID)
	rows, err := tx.QueryContext(ctx, "SELECT id FROM nodes WHERE "+where+" ORDER BY sort_index, id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// setOrder hängt die Knoten in genau dieser Reihenfolge unter parentID.
// Der Bereich (customer_id) bleibt unverändert.
func setOrder(ctx context.Context, tx *sql.Tx, parentID *int64, ids []int64) error {
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE nodes SET parent_id = ?, sort_index = ? WHERE id = ?", parentID, i, id); err != nil {
			return err
		}
	}
	return nil
}

// renumber vergibt sort_index 0..n-1 innerhalb einer Ebene.
func renumber(ctx context.Context, tx *sql.Tx, parentID, customerID *int64) error {
	ids, err := childIDs(ctx, tx, parentID, customerID)
	if err != nil {
		return err
	}
	return setOrder(ctx, tx, parentID, ids)
}

// Create legt einen Knoten am Ende seiner Ebene an.
func (s *Store) Create(ctx context.Context, in NodeInput) (*Node, error) {
	if err := normalize(in.Kind, &in); err != nil {
		return nil, err
	}
	var n *Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		scope, err := resolveScope(ctx, tx, in.ParentID, in.CustomerID)
		if err != nil {
			return err
		}
		idx, err := nextSortIndex(ctx, tx, in.ParentID, scope)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO nodes (customer_id, parent_id, kind, tcode, title, description, sort_index) VALUES (?, ?, ?, ?, ?, ?, ?)",
			scope, in.ParentID, in.Kind, in.TCode, in.Title, in.Description, idx)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		n, err = getNode(ctx, tx, id)
		return err
	})
	return n, err
}

// Update ändert T-Code, Titel und Beschreibung. Art, Bereich und Position bleiben.
func (s *Store) Update(ctx context.Context, id int64, in NodeInput) (*Node, error) {
	var n *Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getNode(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := normalize(cur.Kind, &in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE nodes SET tcode = ?, title = ?, description = ?, updated_at = "+now+" WHERE id = ?",
			in.TCode, in.Title, in.Description, id); err != nil {
			return err
		}
		n, err = getNode(ctx, tx, id)
		return err
	})
	return n, err
}

const (
	DeleteChildrenLift   = "lift"
	DeleteChildrenDelete = "delete"
)

// Delete löscht einen Knoten. Bei nicht-leeren Ordnern muss children
// "lift" (Inhalt an die Stelle des Ordners) oder "delete" sein.
func (s *Store) Delete(ctx context.Context, id int64, children string) error {
	if children != "" && children != DeleteChildrenLift && children != DeleteChildrenDelete {
		return fmt.Errorf("%w: children muss 'lift' oder 'delete' sein", ErrInvalid)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		n, err := getNode(ctx, tx, id)
		if err != nil {
			return err
		}
		kids, err := childIDs(ctx, tx, &id, nil)
		if err != nil {
			return err
		}
		if len(kids) > 0 && children == "" {
			return fmt.Errorf("%w: Ordner ist nicht leer – Inhalt hochziehen oder mitlöschen?", ErrConflict)
		}

		siblings, err := childIDs(ctx, tx, n.ParentID, n.CustomerID)
		if err != nil {
			return err
		}
		order := make([]int64, 0, len(siblings)+len(kids))
		for _, sib := range siblings {
			if sib != id {
				order = append(order, sib)
			} else if children == DeleteChildrenLift {
				order = append(order, kids...)
			}
		}
		if children == DeleteChildrenLift {
			// Erst umhängen, damit ON DELETE CASCADE die Kinder nicht erfasst.
			if err := setOrder(ctx, tx, n.ParentID, order); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE id = ?", id); err != nil {
			return err
		}
		return renumber(ctx, tx, n.ParentID, n.CustomerID)
	})
}

// Move hängt einen Knoten ans Ende des Ziels: eines Ordners (parentID) oder der
// obersten Ebene eines Bereichs (parentID nil, customerID nil = Allgemein).
// Wechselt der Bereich, zieht der ganze Unterbaum mit.
func (s *Store) Move(ctx context.Context, id int64, parentID, customerID *int64) (*Node, error) {
	var n *Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getNode(ctx, tx, id)
		if err != nil {
			return err
		}
		scope, err := resolveScope(ctx, tx, parentID, customerID)
		if err != nil {
			return err
		}
		if sameID(cur.ParentID, parentID) && sameID(cur.CustomerID, scope) {
			n = cur
			return nil
		}
		if parentID != nil {
			var inside bool
			err := tx.QueryRowContext(ctx, `
				WITH RECURSIVE sub(id) AS (
					SELECT ? UNION ALL SELECT n.id FROM nodes n JOIN sub ON n.parent_id = sub.id
				)
				SELECT EXISTS (SELECT 1 FROM sub WHERE id = ?)`, id, *parentID).Scan(&inside)
			if err != nil {
				return err
			}
			if inside {
				return fmt.Errorf("%w: ein Ordner kann nicht in sich selbst verschoben werden", ErrInvalid)
			}
		}
		idx, err := nextSortIndex(ctx, tx, parentID, scope)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE nodes SET parent_id = ?, customer_id = ?, sort_index = ?, updated_at = "+now+" WHERE id = ?",
			parentID, scope, idx, id); err != nil {
			return err
		}
		if !sameID(cur.CustomerID, scope) {
			if _, err := tx.ExecContext(ctx, `
				WITH RECURSIVE sub(id) AS (
					SELECT id FROM nodes WHERE parent_id = ?
					UNION ALL SELECT n.id FROM nodes n JOIN sub ON n.parent_id = sub.id
				)
				UPDATE nodes SET customer_id = ? WHERE id IN (SELECT id FROM sub)`, id, scope); err != nil {
				return err
			}
		}
		if err := renumber(ctx, tx, cur.ParentID, cur.CustomerID); err != nil {
			return err
		}
		if err := renumber(ctx, tx, parentID, scope); err != nil {
			return err
		}
		n, err = getNode(ctx, tx, id)
		return err
	})
	return n, err
}

const (
	DirectionUp   = "up"
	DirectionDown = "down"
)

// Reorder tauscht einen Knoten mit seinem Nachbarn. Am Rand passiert nichts.
func (s *Store) Reorder(ctx context.Context, id int64, direction string) error {
	var delta int
	switch direction {
	case DirectionUp:
		delta = -1
	case DirectionDown:
		delta = 1
	default:
		return fmt.Errorf("%w: direction muss 'up' oder 'down' sein", ErrInvalid)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		n, err := getNode(ctx, tx, id)
		if err != nil {
			return err
		}
		ids, err := childIDs(ctx, tx, n.ParentID, n.CustomerID)
		if err != nil {
			return err
		}
		for i, sib := range ids {
			if sib == id {
				if j := i + delta; j >= 0 && j < len(ids) {
					ids[i], ids[j] = ids[j], ids[i]
				}
				break
			}
		}
		return setOrder(ctx, tx, n.ParentID, ids)
	})
}

type ImportStats struct {
	Folders      int `json:"folders"`
	Transactions int `json:"transactions"`
}

// Import schreibt einen geparsten SAP-Baum in einen Bereich (nil = Allgemein).
// replace löscht vorher nur den Bestand dieses Bereichs, sonst wird ans Ende
// seiner obersten Ebene angehängt.
func (s *Store) Import(ctx context.Context, tree []*sapfile.TreeNode, customerID *int64, replace bool) (ImportStats, error) {
	var stats ImportStats
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkCustomer(ctx, tx, customerID); err != nil {
			return err
		}
		if replace {
			if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE customer_id IS ?", customerID); err != nil {
				return err
			}
		}
		start, err := nextSortIndex(ctx, tx, nil, customerID)
		if err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx,
			"INSERT INTO nodes (customer_id, parent_id, kind, tcode, title, sort_index) VALUES (?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer ins.Close()

		var insert func(level []*sapfile.TreeNode, parentID *int64, offset int) error
		insert = func(level []*sapfile.TreeNode, parentID *int64, offset int) error {
			for i, t := range level {
				var tcode *string
				if t.Kind == sapfile.KindTx {
					tcode = &t.TCode
					stats.Transactions++
				} else {
					stats.Folders++
				}
				res, err := ins.ExecContext(ctx, customerID, parentID, string(t.Kind), tcode, t.Title, offset+i)
				if err != nil {
					return fmt.Errorf("Zeile %d: %w", t.Line, err)
				}
				id, err := res.LastInsertId()
				if err != nil {
					return err
				}
				if err := insert(t.Children, &id, 0); err != nil {
					return err
				}
			}
			return nil
		}
		return insert(tree, nil, start)
	})
	return stats, err
}
