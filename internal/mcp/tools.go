package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/maxkhl/sapfav/internal/store"
)

// errBadArgs steht für Argumente, die das Modell selbst korrigieren kann.
var errBadArgs = errors.New("ungültige Argumente")

type tool struct {
	name         string
	title        string
	description  string
	inputSchema  string
	outputSchema string
	readOnly     bool
	destructive  bool
	run          func(ctx context.Context, args json.RawMessage) (any, error)
}

func (t *tool) definition() map[string]any {
	def := map[string]any{
		"name":        t.name,
		"title":       t.title,
		"description": t.description,
		"inputSchema": json.RawMessage(t.inputSchema),
		"annotations": map[string]any{
			"readOnlyHint":    t.readOnly,
			"destructiveHint": t.destructive,
		},
	}
	if t.outputSchema != "" {
		def["outputSchema"] = json.RawMessage(t.outputSchema)
	}
	return def
}

func (s *Server) add(t *tool) {
	s.tools[t.name] = t
	s.order = append(s.order, t.name)
}

// ---------- Argumente ----------

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %s", errBadArgs, err)
	}
	return nil
}

// parseID liest ein Feld, das eine positive Zahl oder null sein darf.
// present sagt, ob der Schlüssel überhaupt angegeben war.
func parseID(raw json.RawMessage, field string) (id *int64, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return nil, true, fmt.Errorf("%w: %s muss eine positive Zahl oder null sein", errBadArgs, field)
	}
	return &value, true, nil
}

func optionalString(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return p
}

// ---------- Ausgabeformen ----------

type nodeOut struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	TCode       string    `json:"tcode,omitempty"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Children    []nodeOut `json:"children,omitempty"`
}

func nodesOut(nodes []*store.Node) []nodeOut {
	out := make([]nodeOut, 0, len(nodes))
	for _, n := range nodes {
		item := nodeOut{ID: n.ID, Kind: n.Kind, Title: n.Title, Children: nodesOut(n.Children)}
		if n.TCode != nil {
			item.TCode = *n.TCode
		}
		if n.Description != nil {
			item.Description = *n.Description
		}
		out = append(out, item)
	}
	return out
}

type nodeRef struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	TCode       string `json:"tcode,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	ParentID    *int64 `json:"parent_id"`
	CustomerID  *int64 `json:"customer_id"`
}

func ref(n *store.Node) nodeRef {
	r := nodeRef{ID: n.ID, Kind: n.Kind, Title: n.Title, ParentID: n.ParentID, CustomerID: n.CustomerID}
	if n.TCode != nil {
		r.TCode = *n.TCode
	}
	if n.Description != nil {
		r.Description = *n.Description
	}
	return r
}

type customerOut struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Notes     string `json:"notes,omitempty"`
	NodeCount int    `json:"node_count"`
}

func customerRef(c *store.Customer) customerOut {
	out := customerOut{ID: c.ID, Name: c.Name, NodeCount: c.NodeCount}
	if c.Notes != nil {
		out.Notes = *c.Notes
	}
	return out
}

// ---------- Bereiche ----------

type scopeArgs struct {
	Scope      string `json:"scope"`
	CustomerID *int64 `json:"customer_id"`
}

type scope struct {
	customerID *int64
	name       string
}

// scopes löst die Bereichsangabe auf: alle Bereiche, nur Allgemein oder ein Kunde.
func (s *Server) scopes(ctx context.Context, a scopeArgs) ([]scope, error) {
	switch a.Scope {
	case "", "all":
		customers, err := s.store.Customers(ctx)
		if err != nil {
			return nil, err
		}
		out := []scope{{nil, "Allgemein"}}
		for _, c := range customers {
			out = append(out, scope{&c.ID, c.Name})
		}
		return out, nil
	case "general":
		return []scope{{nil, "Allgemein"}}, nil
	case "customer":
		if a.CustomerID == nil {
			return nil, fmt.Errorf(`%w: scope "customer" braucht customer_id`, errBadArgs)
		}
		c, err := s.store.GetCustomer(ctx, *a.CustomerID)
		if err != nil {
			return nil, err
		}
		return []scope{{&c.ID, c.Name}}, nil
	default:
		return nil, fmt.Errorf(`%w: scope muss "all", "general" oder "customer" sein`, errBadArgs)
	}
}

// topLevel filtert die oberste Ebene eines Bereichs aus dem Gesamtbaum.
func topLevel(tree []*store.Node, customerID *int64) []*store.Node {
	var out []*store.Node
	for _, n := range tree {
		if sameScope(n.CustomerID, customerID) {
			out = append(out, n)
		}
	}
	return out
}

func sameScope(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---------- Schemata ----------

const (
	schemaScopeProps = `"scope":{"type":"string","enum":["all","general","customer"],` +
		`"description":"all = alle Bereiche (Standard), general = nur Allgemein, customer = ein Kunde"},` +
		`"customer_id":{"type":["integer","null"],"description":"Kunde, nur bei scope=customer"}`

	schemaNodeDefs = `"$defs":{"node":{"type":"object","properties":{` +
		`"id":{"type":"integer"},"kind":{"type":"string","enum":["folder","tx"]},` +
		`"tcode":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},` +
		`"children":{"type":"array","items":{"$ref":"#/$defs/node"}}},` +
		`"required":["id","kind","title"]}}`

	schemaNodeRef = `{"type":"object","properties":{"id":{"type":"integer"},` +
		`"kind":{"type":"string","enum":["folder","tx"]},"tcode":{"type":"string"},` +
		`"title":{"type":"string"},"description":{"type":"string"},` +
		`"parent_id":{"type":["integer","null"]},"customer_id":{"type":["integer","null"]}},` +
		`"required":["id","kind","title"]}`

	schemaCustomerList = `{"type":"object","properties":{` +
		`"general_count":{"type":"integer","description":"Einträge im Bereich Allgemein"},` +
		`"customers":{"type":"array","items":{"type":"object","properties":{` +
		`"id":{"type":"integer"},"name":{"type":"string"},"notes":{"type":"string"},` +
		`"node_count":{"type":"integer"}},"required":["id","name","node_count"]}}},` +
		`"required":["general_count","customers"]}`

	schemaCustomerRef = `{"type":"object","properties":{"id":{"type":"integer"},` +
		`"name":{"type":"string"},"notes":{"type":"string"},"node_count":{"type":"integer"}},` +
		`"required":["id","name","node_count"]}`
)

// ---------- Registrierung ----------

func (s *Server) register() {
	s.add(&tool{
		name:         "list_customers",
		title:        "Kunden auflisten",
		description:  "Listet alle Kunden mit Notizen und der Anzahl ihrer Einträge, dazu die Anzahl im Bereich Allgemein.",
		inputSchema:  `{"type":"object","additionalProperties":false}`,
		outputSchema: schemaCustomerList,
		readOnly:     true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			if err := decodeArgs(args, &struct{}{}); err != nil {
				return nil, err
			}
			customers, err := s.store.Customers(ctx)
			if err != nil {
				return nil, err
			}
			count, err := s.store.Count(ctx, nil)
			if err != nil {
				return nil, err
			}
			list := make([]customerOut, 0, len(customers))
			for _, c := range customers {
				list = append(list, customerRef(c))
			}
			return map[string]any{"general_count": count, "customers": list}, nil
		},
	})

	s.add(&tool{
		name:  "get_tree",
		title: "Favoritenbaum lesen",
		description: "Liefert den Baum aus Ordnern und Transaktionen, gruppiert nach Bereich " +
			"(Allgemein und Kunden). Enthält IDs, T-Codes, Titel und Beschreibungen.",
		inputSchema:  `{"type":"object","additionalProperties":false,"properties":{` + schemaScopeProps + `}}`,
		outputSchema: `{"type":"object",` + schemaNodeDefs + `,"properties":{"scopes":{"type":"array","items":{"type":"object","properties":{"customer_id":{"type":["integer","null"]},"customer":{"type":"string"},"nodes":{"type":"array","items":{"$ref":"#/$defs/node"}}},"required":["customer","nodes"]}}},"required":["scopes"]}`,
		readOnly:     true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a scopeArgs
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			scopes, err := s.scopes(ctx, a)
			if err != nil {
				return nil, err
			}
			tree, err := s.store.Tree(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(scopes))
			for _, sc := range scopes {
				out = append(out, map[string]any{
					"customer_id": sc.customerID,
					"customer":    sc.name,
					"nodes":       nodesOut(topLevel(tree, sc.customerID)),
				})
			}
			return map[string]any{"scopes": out}, nil
		},
	})

	s.add(&tool{
		name:  "search",
		title: "Favoriten suchen",
		description: "Sucht in T-Code, Titel und Beschreibung. Alle Begriffe müssen vorkommen " +
			"(Groß- und Kleinschreibung egal). Jeder Treffer nennt seinen Pfad im Baum.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["query"],"properties":{` +
			`"query":{"type":"string","description":"Suchbegriffe, durch Leerzeichen getrennt"},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"Höchstzahl der Treffer, Standard 50"},` +
			schemaScopeProps + `}}`,
		outputSchema: `{"type":"object","properties":{"count":{"type":"integer"},` +
			`"truncated":{"type":"boolean","description":"true, wenn weitere Treffer wegen limit fehlen"},` +
			`"hits":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"},` +
			`"kind":{"type":"string","enum":["folder","tx"]},"tcode":{"type":"string"},"title":{"type":"string"},` +
			`"description":{"type":"string"},"path":{"type":"string"},"customer_id":{"type":["integer","null"]}},` +
			`"required":["id","kind","title","path"]}}},"required":["count","hits"]}`,
		readOnly: true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				Query      string `json:"query"`
				Limit      int    `json:"limit"`
				Scope      string `json:"scope"`
				CustomerID *int64 `json:"customer_id"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			terms := strings.Fields(strings.ToLower(a.Query))
			if len(terms) == 0 {
				return nil, fmt.Errorf("%w: query ist leer", errBadArgs)
			}
			switch {
			case a.Limit == 0:
				a.Limit = 50
			case a.Limit < 0 || a.Limit > 200:
				return nil, fmt.Errorf("%w: limit muss zwischen 1 und 200 liegen", errBadArgs)
			}
			scopes, err := s.scopes(ctx, scopeArgs{Scope: a.Scope, CustomerID: a.CustomerID})
			if err != nil {
				return nil, err
			}
			tree, err := s.store.Tree(ctx)
			if err != nil {
				return nil, err
			}

			hits := []map[string]any{}
			total := 0
			for _, sc := range scopes {
				var walk func(nodes []*store.Node, path string)
				walk = func(nodes []*store.Node, path string) {
					for _, n := range nodes {
						if matches(n, terms) {
							total++
							if len(hits) < a.Limit {
								hit := map[string]any{"id": n.ID, "kind": n.Kind, "title": n.Title,
									"path": path, "customer_id": sc.customerID}
								if n.TCode != nil {
									hit["tcode"] = *n.TCode
								}
								if n.Description != nil {
									hit["description"] = *n.Description
								}
								hits = append(hits, hit)
							}
						}
						walk(n.Children, path+" / "+n.Title)
					}
				}
				walk(topLevel(tree, sc.customerID), sc.name)
			}
			return map[string]any{"count": total, "truncated": total > len(hits), "hits": hits}, nil
		},
	})

	s.add(&tool{
		name:  "create_node",
		title: "Ordner oder Transaktion anlegen",
		description: "Legt einen Eintrag am Ende seiner Ebene an. Ziel ist entweder ein Ordner " +
			"(parent_id) oder die oberste Ebene eines Bereichs (parent_id null, customer_id " +
			"null = Allgemein). Unter einem Ordner gilt dessen Bereich.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["kind","title"],"properties":{` +
			`"kind":{"type":"string","enum":["folder","tx"],"description":"folder = Ordner, tx = Transaktion"},` +
			`"title":{"type":"string","description":"Ordnername bzw. Kurztext der Transaktion"},` +
			`"tcode":{"type":"string","description":"Transaktionscode, nur bei kind=tx"},` +
			`"description":{"type":"string","description":"Freitext, mehrzeilig erlaubt"},` +
			`"parent_id":{"type":["integer","null"],"description":"Zielordner, null = oberste Ebene"},` +
			`"customer_id":{"type":["integer","null"],"description":"Bereich, null = Allgemein; nur ohne parent_id"}}}`,
		outputSchema: schemaNodeRef,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				Kind        string          `json:"kind"`
				Title       string          `json:"title"`
				TCode       string          `json:"tcode"`
				Description string          `json:"description"`
				ParentID    json.RawMessage `json:"parent_id"`
				CustomerID  json.RawMessage `json:"customer_id"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			parentID, _, err := parseID(a.ParentID, "parent_id")
			if err != nil {
				return nil, err
			}
			customerID, _, err := parseID(a.CustomerID, "customer_id")
			if err != nil {
				return nil, err
			}
			in := store.NodeInput{CustomerID: customerID, ParentID: parentID, Kind: a.Kind, Title: a.Title}
			if a.TCode != "" {
				in.TCode = &a.TCode
			}
			if a.Description != "" {
				in.Description = &a.Description
			}
			n, err := s.store.Create(ctx, in)
			if err != nil {
				return nil, err
			}
			return ref(n), nil
		},
	})

	s.add(&tool{
		name:  "update_node",
		title: "Eintrag ändern",
		description: "Ändert T-Code, Titel oder Beschreibung. Nur mitgeschickte Felder werden " +
			"geändert; eine leere Beschreibung löscht sie. Art und Position bleiben — " +
			"zum Umhängen move_node nutzen.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{` +
			`"id":{"type":"integer"},"tcode":{"type":"string"},"title":{"type":"string"},` +
			`"description":{"type":"string","description":"leerer String löscht die Beschreibung"}}}`,
		outputSchema: schemaNodeRef,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ID          int64   `json:"id"`
				TCode       *string `json:"tcode"`
				Title       *string `json:"title"`
				Description *string `json:"description"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			cur, err := s.store.Get(ctx, a.ID)
			if err != nil {
				return nil, err
			}
			in := store.NodeInput{Title: cur.Title, TCode: cur.TCode, Description: cur.Description}
			if a.Title != nil {
				in.Title = *a.Title
			}
			if a.TCode != nil {
				in.TCode = a.TCode
			}
			if a.Description != nil {
				in.Description = a.Description
			}
			n, err := s.store.Update(ctx, a.ID, in)
			if err != nil {
				return nil, err
			}
			return ref(n), nil
		},
	})

	s.add(&tool{
		name:  "move_node",
		title: "Eintrag verschieben",
		description: "Hängt einen Eintrag ans Ende eines Ordners oder der obersten Ebene eines " +
			"Bereichs. parent_id muss angegeben werden (null = oberste Ebene). Ordner nehmen " +
			"ihren Inhalt mit, auch zu einem anderen Kunden.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["id","parent_id"],"properties":{` +
			`"id":{"type":"integer"},"parent_id":{"type":["integer","null"],"description":"Zielordner, null = oberste Ebene"},` +
			`"customer_id":{"type":["integer","null"],"description":"Bereich, null = Allgemein; nur ohne parent_id"}}}`,
		outputSchema: schemaNodeRef,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ID         int64           `json:"id"`
				ParentID   json.RawMessage `json:"parent_id"`
				CustomerID json.RawMessage `json:"customer_id"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			parentID, present, err := parseID(a.ParentID, "parent_id")
			if err != nil {
				return nil, err
			}
			if !present {
				return nil, fmt.Errorf("%w: parent_id muss angegeben werden (null = oberste Ebene)", errBadArgs)
			}
			customerID, _, err := parseID(a.CustomerID, "customer_id")
			if err != nil {
				return nil, err
			}
			n, err := s.store.Move(ctx, a.ID, parentID, customerID)
			if err != nil {
				return nil, err
			}
			return ref(n), nil
		},
	})

	s.add(&tool{
		name:  "set_children_order",
		title: "Reihenfolge einer Ebene setzen",
		description: "Setzt die Reihenfolge einer ganzen Ebene in einem Aufruf — zum Sortieren " +
			"oder Aufräumen. ids muss genau die Einträge dieser Ebene enthalten, in der " +
			"gewünschten Reihenfolge. Vorher mit get_tree die IDs der Ebene holen.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["parent_id","ids"],"properties":{` +
			`"parent_id":{"type":["integer","null"],"description":"Ordner, dessen Inhalt sortiert wird; null = oberste Ebene"},` +
			`"customer_id":{"type":["integer","null"],"description":"Bereich, null = Allgemein; nur ohne parent_id"},` +
			`"ids":{"type":"array","items":{"type":"integer"},"description":"alle IDs dieser Ebene in neuer Reihenfolge"}}}`,
		outputSchema: `{"type":"object","properties":{"ordered":{"type":"array","items":{"type":"object",` +
			`"properties":{"id":{"type":"integer"},"title":{"type":"string"}},"required":["id","title"]}}},` +
			`"required":["ordered"]}`,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ParentID   json.RawMessage `json:"parent_id"`
				CustomerID json.RawMessage `json:"customer_id"`
				IDs        []int64         `json:"ids"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			parentID, present, err := parseID(a.ParentID, "parent_id")
			if err != nil {
				return nil, err
			}
			if !present {
				return nil, fmt.Errorf("%w: parent_id muss angegeben werden (null = oberste Ebene)", errBadArgs)
			}
			customerID, _, err := parseID(a.CustomerID, "customer_id")
			if err != nil {
				return nil, err
			}
			if err := s.store.SetOrder(ctx, parentID, customerID, a.IDs); err != nil {
				return nil, err
			}
			ordered := make([]map[string]any, 0, len(a.IDs))
			for _, id := range a.IDs {
				n, err := s.store.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				ordered = append(ordered, map[string]any{"id": n.ID, "title": n.Title})
			}
			return map[string]any{"ordered": ordered}, nil
		},
	})

	s.add(&tool{
		name:  "delete_node",
		title: "Eintrag löschen",
		description: "Löscht einen Eintrag. Bei einem Ordner mit Inhalt muss children angegeben " +
			"werden: lift zieht den Inhalt eine Ebene hoch, delete löscht ihn mit.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{` +
			`"id":{"type":"integer"},"children":{"type":"string","enum":["lift","delete"],` +
			`"description":"nur bei Ordnern mit Inhalt nötig"}}}`,
		outputSchema: `{"type":"object","properties":{"deleted":{"type":"integer"},` +
			`"children":{"type":"string"}},"required":["deleted"]}`,
		destructive: true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ID       int64  `json:"id"`
				Children string `json:"children"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			if err := s.store.Delete(ctx, a.ID, a.Children); err != nil {
				return nil, err
			}
			out := map[string]any{"deleted": a.ID}
			if a.Children != "" {
				out["children"] = a.Children
			}
			return out, nil
		},
	})

	s.add(&tool{
		name:  "delete_nodes",
		title: "Mehrere Einträge löschen",
		description: "Löscht mehrere Einträge in einem Aufruf und einer Transaktion: " +
			"entweder alle oder keiner. Beim Aufräumen von Dubletten diesem Tool den " +
			"Vorzug vor einzelnen delete_node-Aufrufen geben. children gilt wie bei " +
			"delete_node für Ordner mit Inhalt. Einträge, die als Kind eines anderen " +
			"Eintrags der Liste verschwinden, kommen als already_removed zurück.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["ids"],"properties":{` +
			`"ids":{"type":"array","minItems":1,"items":{"type":"integer"},` +
			`"description":"IDs der zu löschenden Einträge"},` +
			`"children":{"type":"string","enum":["lift","delete"],` +
			`"description":"nur nötig, wenn ein Ordner mit Inhalt dabei ist"}}}`,
		outputSchema: `{"type":"object","properties":{` +
			`"deleted":{"type":"array","items":{"type":"integer"}},` +
			`"already_removed":{"type":"array","items":{"type":"integer"}},` +
			`"count":{"type":"integer"}},"required":["deleted","count"]}`,
		destructive: true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				IDs      []int64 `json:"ids"`
				Children string  `json:"children"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			res, err := s.store.DeleteMany(ctx, a.IDs, a.Children)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"deleted":         res.Deleted,
				"already_removed": res.AlreadyRemoved,
				"count":           len(res.Deleted),
			}, nil
		},
	})

	s.add(&tool{
		name:  "find_duplicates",
		title: "Dubletten finden",
		description: "Findet Transaktionen, die mit demselben T-Code mehrfach vorkommen, " +
			"gruppiert nach T-Code und mit Pfad je Eintrag. Ordner bleiben außen vor. " +
			"Die IDs lassen sich direkt an delete_nodes weitergeben.",
		inputSchema: `{"type":"object","additionalProperties":false,"properties":{` + schemaScopeProps + `}}`,
		outputSchema: `{"type":"object","properties":{` +
			`"group_count":{"type":"integer","description":"Anzahl mehrfach vorkommender T-Codes"},` +
			`"extra_count":{"type":"integer","description":"wie viele Einträge übrig wären, bliebe je T-Code einer"},` +
			`"groups":{"type":"array","items":{"type":"object","properties":{` +
			`"tcode":{"type":"string"},"count":{"type":"integer"},` +
			`"entries":{"type":"array","items":{"type":"object","properties":{` +
			`"id":{"type":"integer"},"title":{"type":"string"},"description":{"type":"string"},` +
			`"path":{"type":"string"},"customer_id":{"type":["integer","null"]}},` +
			`"required":["id","title","path"]}}},"required":["tcode","count","entries"]}}},` +
			`"required":["group_count","extra_count","groups"]}`,
		readOnly: true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a scopeArgs
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			scopes, err := s.scopes(ctx, a)
			if err != nil {
				return nil, err
			}
			tree, err := s.store.Tree(ctx)
			if err != nil {
				return nil, err
			}

			byTCode := map[string][]map[string]any{}
			for _, sc := range scopes {
				var walk func(nodes []*store.Node, path string)
				walk = func(nodes []*store.Node, path string) {
					for _, n := range nodes {
						if n.Kind == store.KindTx && n.TCode != nil {
							entry := map[string]any{"id": n.ID, "title": n.Title,
								"path": path, "customer_id": sc.customerID}
							if n.Description != nil {
								entry["description"] = *n.Description
							}
							key := strings.ToUpper(strings.TrimSpace(*n.TCode))
							byTCode[key] = append(byTCode[key], entry)
						}
						walk(n.Children, path+" / "+n.Title)
					}
				}
				walk(topLevel(tree, sc.customerID), sc.name)
			}

			codes := make([]string, 0, len(byTCode))
			for code, entries := range byTCode {
				if len(entries) > 1 {
					codes = append(codes, code)
				}
			}
			sort.Strings(codes) // deterministische Reihenfolge

			groups := make([]map[string]any, 0, len(codes))
			extra := 0
			for _, code := range codes {
				entries := byTCode[code]
				extra += len(entries) - 1
				groups = append(groups, map[string]any{
					"tcode": code, "count": len(entries), "entries": entries,
				})
			}
			return map[string]any{"group_count": len(groups), "extra_count": extra, "groups": groups}, nil
		},
	})

	s.add(&tool{
		name:         "create_customer",
		title:        "Kunde anlegen",
		description:  "Legt einen Kunden an. Der Name muss eindeutig sein; notes nimmt Systeme, Mandanten und Ähnliches auf.",
		inputSchema:  `{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string"},"notes":{"type":"string"}}}`,
		outputSchema: schemaCustomerRef,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				Name  string  `json:"name"`
				Notes *string `json:"notes"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			c, err := s.store.CreateCustomer(ctx, store.CustomerInput{Name: a.Name, Notes: optionalString(a.Notes)})
			if err != nil {
				return nil, err
			}
			return customerRef(c), nil
		},
	})

	s.add(&tool{
		name:         "update_customer",
		title:        "Kunde ändern",
		description:  "Ändert Name oder Notizen eines Kunden. Nur mitgeschickte Felder werden geändert; leere notes löschen sie.",
		inputSchema:  `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"integer"},"name":{"type":"string"},"notes":{"type":"string"}}}`,
		outputSchema: schemaCustomerRef,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ID    int64   `json:"id"`
				Name  *string `json:"name"`
				Notes *string `json:"notes"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			cur, err := s.store.GetCustomer(ctx, a.ID)
			if err != nil {
				return nil, err
			}
			in := store.CustomerInput{Name: cur.Name, Notes: cur.Notes}
			if a.Name != nil {
				in.Name = *a.Name
			}
			if a.Notes != nil {
				in.Notes = optionalString(a.Notes)
			}
			c, err := s.store.UpdateCustomer(ctx, a.ID, in)
			if err != nil {
				return nil, err
			}
			return customerRef(c), nil
		},
	})

	s.add(&tool{
		name:  "delete_customer",
		title: "Kunde löschen",
		description: "Löscht einen Kunden. Hat er Favoriten, muss nodes angegeben werden: " +
			"general verschiebt sie nach Allgemein, delete löscht sie mit.",
		inputSchema: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{` +
			`"id":{"type":"integer"},"nodes":{"type":"string","enum":["general","delete"],` +
			`"description":"nur bei Kunden mit Favoriten nötig"}}}`,
		outputSchema: `{"type":"object","properties":{"deleted":{"type":"integer"},` +
			`"nodes":{"type":"string"}},"required":["deleted"]}`,
		destructive: true,
		run: func(ctx context.Context, args json.RawMessage) (any, error) {
			var a struct {
				ID    int64  `json:"id"`
				Nodes string `json:"nodes"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return nil, err
			}
			if err := s.store.DeleteCustomer(ctx, a.ID, a.Nodes); err != nil {
				return nil, err
			}
			out := map[string]any{"deleted": a.ID}
			if a.Nodes != "" {
				out["nodes"] = a.Nodes
			}
			return out, nil
		},
	})
}

// matches prüft, ob alle Begriffe in T-Code, Titel oder Beschreibung vorkommen.
func matches(n *store.Node, terms []string) bool {
	var b strings.Builder
	if n.TCode != nil {
		b.WriteString(*n.TCode)
		b.WriteByte('\n')
	}
	b.WriteString(n.Title)
	if n.Description != nil {
		b.WriteByte('\n')
		b.WriteString(*n.Description)
	}
	text := strings.ToLower(b.String())
	for _, t := range terms {
		if !strings.Contains(text, t) {
			return false
		}
	}
	return true
}
