package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/maxkhl/sapfav/internal/sapimport"
)

const (
	KindFolder = "folder"
	KindTx     = "tx"
)

// Node ist ein Ordner oder eine Transaktion. Children ist nur beim Baum gefüllt.
type Node struct {
	ID          int64   `json:"id"`
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

// NodeInput sind die änderbaren Felder. Kind und ParentID zählen nur beim Anlegen.
type NodeInput struct {
	ParentID    *int64  `json:"parent_id"`
	Kind        string  `json:"kind"`
	TCode       *string `json:"tcode"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

const nodeColumns = "id, parent_id, kind, tcode, title, description, sort_index, created_at, updated_at"

const now = "strftime('%Y-%m-%dT%H:%M:%SZ', 'now')"

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	err := row.Scan(&n.ID, &n.ParentID, &n.Kind, &n.TCode, &n.Title, &n.Description,
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

// Tree liefert alle Knoten verschachtelt, sortiert nach sort_index.
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

// checkParent stellt sicher, dass der Parent existiert und ein Ordner ist.
func checkParent(ctx context.Context, tx *sql.Tx, parentID *int64) error {
	if parentID == nil {
		return nil
	}
	p, err := getNode(ctx, tx, *parentID)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Zielordner %d existiert nicht", ErrInvalid, *parentID)
	}
	if err != nil {
		return err
	}
	if p.Kind != KindFolder {
		return fmt.Errorf("%w: Ziel %d ist kein Ordner", ErrInvalid, *parentID)
	}
	return nil
}

func nextSortIndex(ctx context.Context, tx *sql.Tx, parentID *int64) (int, error) {
	var next int
	err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_index), -1) + 1 FROM nodes WHERE parent_id IS ?", parentID).Scan(&next)
	return next, err
}

// renumber vergibt sort_index 0..n-1 innerhalb eines Parents.
func renumber(ctx context.Context, tx *sql.Tx, parentID *int64) error {
	ids, err := childIDs(ctx, tx, parentID)
	if err != nil {
		return err
	}
	return setOrder(ctx, tx, parentID, ids)
}

func childIDs(ctx context.Context, tx *sql.Tx, parentID *int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT id FROM nodes WHERE parent_id IS ? ORDER BY sort_index, id", parentID)
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
func setOrder(ctx context.Context, tx *sql.Tx, parentID *int64, ids []int64) error {
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE nodes SET parent_id = ?, sort_index = ? WHERE id = ?", parentID, i, id); err != nil {
			return err
		}
	}
	return nil
}

// Create legt einen Knoten am Ende seiner Ebene an.
func (s *Store) Create(ctx context.Context, in NodeInput) (*Node, error) {
	if err := normalize(in.Kind, &in); err != nil {
		return nil, err
	}
	var n *Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkParent(ctx, tx, in.ParentID); err != nil {
			return err
		}
		idx, err := nextSortIndex(ctx, tx, in.ParentID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO nodes (parent_id, kind, tcode, title, description, sort_index) VALUES (?, ?, ?, ?, ?, ?)",
			in.ParentID, in.Kind, in.TCode, in.Title, in.Description, idx)
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

// Update ändert T-Code, Titel und Beschreibung. Art und Position bleiben.
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
		kids, err := childIDs(ctx, tx, &id)
		if err != nil {
			return err
		}
		if len(kids) > 0 && children == "" {
			return fmt.Errorf("%w: Ordner ist nicht leer – Inhalt hochziehen oder mitlöschen?", ErrConflict)
		}

		siblings, err := childIDs(ctx, tx, n.ParentID)
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
		return renumber(ctx, tx, n.ParentID)
	})
}

// Move hängt einen Knoten ans Ende des Zielordners (nil = oberste Ebene).
func (s *Store) Move(ctx context.Context, id int64, parentID *int64) (*Node, error) {
	var n *Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getNode(ctx, tx, id)
		if err != nil {
			return err
		}
		if sameParent(cur.ParentID, parentID) {
			n = cur
			return nil
		}
		if err := checkParent(ctx, tx, parentID); err != nil {
			return err
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
		idx, err := nextSortIndex(ctx, tx, parentID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE nodes SET parent_id = ?, sort_index = ?, updated_at = "+now+" WHERE id = ?",
			parentID, idx, id); err != nil {
			return err
		}
		if err := renumber(ctx, tx, cur.ParentID); err != nil {
			return err
		}
		if err := renumber(ctx, tx, parentID); err != nil {
			return err
		}
		n, err = getNode(ctx, tx, id)
		return err
	})
	return n, err
}

func sameParent(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
		ids, err := childIDs(ctx, tx, n.ParentID)
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

// Import schreibt einen geparsten SAP-Baum. replace löscht vorher den Bestand,
// sonst wird ans Ende der obersten Ebene angehängt.
func (s *Store) Import(ctx context.Context, tree []*sapimport.TreeNode, replace bool) (ImportStats, error) {
	var stats ImportStats
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if replace {
			if _, err := tx.ExecContext(ctx, "DELETE FROM nodes"); err != nil {
				return err
			}
		}
		start, err := nextSortIndex(ctx, tx, nil)
		if err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx,
			"INSERT INTO nodes (parent_id, kind, tcode, title, sort_index) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer ins.Close()

		var insert func(level []*sapimport.TreeNode, parentID *int64, offset int) error
		insert = func(level []*sapimport.TreeNode, parentID *int64, offset int) error {
			for i, t := range level {
				var tcode *string
				if t.Kind == sapimport.KindTx {
					tcode = &t.TCode
					stats.Transactions++
				} else {
					stats.Folders++
				}
				res, err := ins.ExecContext(ctx, parentID, string(t.Kind), tcode, t.Title, offset+i)
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
