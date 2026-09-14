package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Customer ist ein Kunde mit eigenem Favoritenbaum.
type Customer struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Notes     *string `json:"notes"`
	NodeCount int     `json:"node_count"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type CustomerInput struct {
	Name  string  `json:"name"`
	Notes *string `json:"notes"`
}

const customerQuery = `
	SELECT c.id, c.name, c.notes, c.created_at, c.updated_at,
	       (SELECT COUNT(*) FROM nodes n WHERE n.customer_id = c.id)
	FROM customers c`

func scanCustomer(row interface{ Scan(...any) error }) (*Customer, error) {
	c := &Customer{}
	err := row.Scan(&c.ID, &c.Name, &c.Notes, &c.CreatedAt, &c.UpdatedAt, &c.NodeCount)
	return c, err
}

func getCustomer(ctx context.Context, q querier, id int64) (*Customer, error) {
	c, err := scanCustomer(q.QueryRowContext(ctx, customerQuery+" WHERE c.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: Kunde %d", ErrNotFound, id)
	}
	return c, err
}

// checkCustomer stellt sicher, dass ein angegebener Kunde existiert (nil = Allgemein).
func checkCustomer(ctx context.Context, tx *sql.Tx, id *int64) error {
	if id == nil {
		return nil
	}
	if _, err := getCustomer(ctx, tx, *id); errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Kunde %d existiert nicht", ErrInvalid, *id)
	} else {
		return err
	}
}

func (s *Store) GetCustomer(ctx context.Context, id int64) (*Customer, error) {
	return getCustomer(ctx, s.db, id)
}

// Customers liefert alle Kunden alphabetisch.
func (s *Store) Customers(ctx context.Context) ([]*Customer, error) {
	rows, err := s.db.QueryContext(ctx, customerQuery+" ORDER BY c.name COLLATE NOCASE, c.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []*Customer{}
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func normalizeCustomer(ctx context.Context, tx *sql.Tx, id int64, in *CustomerInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("%w: Kundenname fehlt", ErrInvalid)
	}
	if in.Notes != nil {
		if n := strings.TrimSpace(*in.Notes); n == "" {
			in.Notes = nil
		} else {
			in.Notes = &n
		}
	}
	var taken bool
	err := tx.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM customers WHERE name = ? COLLATE NOCASE AND id <> ?)", in.Name, id).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: Kunde „%s“ gibt es schon", ErrConflict, in.Name)
	}
	return nil
}

func (s *Store) CreateCustomer(ctx context.Context, in CustomerInput) (*Customer, error) {
	var c *Customer
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := normalizeCustomer(ctx, tx, 0, &in); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO customers (name, notes) VALUES (?, ?)", in.Name, in.Notes)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		c, err = getCustomer(ctx, tx, id)
		return err
	})
	return c, err
}

func (s *Store) UpdateCustomer(ctx context.Context, id int64, in CustomerInput) (*Customer, error) {
	var c *Customer
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getCustomer(ctx, tx, id); err != nil {
			return err
		}
		if err := normalizeCustomer(ctx, tx, id, &in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE customers SET name = ?, notes = ?, updated_at = "+now+" WHERE id = ?",
			in.Name, in.Notes, id); err != nil {
			return err
		}
		var err error
		c, err = getCustomer(ctx, tx, id)
		return err
	})
	return c, err
}

const (
	DeleteNodesGeneral = "general"
	DeleteNodesDelete  = "delete"
)

// DeleteCustomer löscht einen Kunden. Hat er Favoriten, muss nodes "general"
// (ans Ende von Allgemein verschieben) oder "delete" (mitlöschen) sein.
func (s *Store) DeleteCustomer(ctx context.Context, id int64, nodes string) error {
	if nodes != "" && nodes != DeleteNodesGeneral && nodes != DeleteNodesDelete {
		return fmt.Errorf("%w: nodes muss 'general' oder 'delete' sein", ErrInvalid)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := getCustomer(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.NodeCount > 0 && nodes == "" {
			return fmt.Errorf("%w: Kunde „%s“ hat %d Favoriten – nach Allgemein verschieben oder mitlöschen?",
				ErrConflict, c.Name, c.NodeCount)
		}
		if nodes == DeleteNodesGeneral {
			top, err := childIDs(ctx, tx, nil, &id)
			if err != nil {
				return err
			}
			start, err := nextSortIndex(ctx, tx, nil, nil)
			if err != nil {
				return err
			}
			for i, nid := range top {
				if _, err := tx.ExecContext(ctx, "UPDATE nodes SET sort_index = ? WHERE id = ?", start+i, nid); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "UPDATE nodes SET customer_id = NULL WHERE customer_id = ?", id); err != nil {
				return err
			}
		}
		// Bei "delete" entfernt ON DELETE CASCADE die Favoriten.
		_, err = tx.ExecContext(ctx, "DELETE FROM customers WHERE id = ?", id)
		return err
	})
}
