// Package sapfile liest und schreibt das Favoriten-Dateiformat von SAP Easy Access.
//
// Format: eine Zeile je Knoten, feste Spalten (nach dem Dekodieren, in Zeichen):
//
//	 0-1   "TR" = Transaktion, zwei Leerzeichen = Ordner
//	 2-6   ID des übergeordneten Knotens, 00001 = oberste Ebene
//	 7-11  eigene ID
//	12-59  Transaktionscode, bei Ordnern leer
//	60-    Kurztext
//
// Die Hierarchie steckt ausschließlich in den IDs.
package sapfile

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// RootID ist die Parent-ID der obersten Ebene.
const RootID = "00001"

const (
	EncodingCP1252 = "cp1252"
	EncodingUTF8   = "utf-8"
)

// Encodings sind die im Import-Dialog wählbaren Kodierungen, Default zuerst.
var Encodings = []string{EncodingCP1252, EncodingUTF8}

type Kind string

const (
	KindFolder Kind = "folder"
	KindTx     Kind = "tx"
)

// Node ist eine erfolgreich zerlegte Zeile.
type Node struct {
	Line     int    `json:"line"`
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Kind     Kind   `json:"kind"`
	TCode    string `json:"tcode,omitempty"`
	Title    string `json:"title"`
	// Fallback ist gesetzt, wenn die Zeile nicht den festen Spalten entsprach.
	Fallback bool `json:"fallback,omitempty"`
}

// Problem ist eine Zeile, die nicht übernommen wird.
type Problem struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// Orphan ist ein Knoten, dessen Parent-ID nicht in den Baum führt.
// Beim Import landet er auf der obersten Ebene.
type Orphan struct {
	Line     int    `json:"line"`
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Reason   string `json:"reason"`
}

// TreeNode ist ein Knoten im aufgebauten Baum; Kinder in Dateireihenfolge.
type TreeNode struct {
	*Node
	Children []*TreeNode
}

type Result struct {
	Nodes        []*Node   `json:"-"`
	Folders      int       `json:"folders"`
	Transactions int       `json:"transactions"`
	Fallbacks    int       `json:"fallbacks"`
	Unparsable   []Problem `json:"unparsable"`
	Orphans      []Orphan  `json:"orphans"`

	// effectiveParent: Parent-ID nach Einordnung der Waisen (RootID für Waisen).
	effectiveParent map[string]string
}

// Decode wandelt die Rohdaten in einen String um.
func Decode(data []byte, encoding string) (string, error) {
	switch strings.ToLower(encoding) {
	case "", EncodingCP1252:
		return charmap.Windows1252.NewDecoder().String(string(data))
	case EncodingUTF8:
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			return "", errors.New("Datei ist kein gültiges UTF-8 – anderes Encoding wählen (SAP exportiert meist cp1252)")
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unbekanntes Encoding %q", encoding)
	}
}

// Parse dekodiert und zerlegt einen Export.
func Parse(data []byte, encoding string) (*Result, error) {
	text, err := Decode(data, encoding)
	if err != nil {
		return nil, err
	}
	res := &Result{Unparsable: []Problem{}, Orphans: []Orphan{}}
	firstLine := map[string]int{}

	for i, raw := range strings.Split(text, "\n") {
		lineNo := i + 1
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		n, reason := parseLine(line)
		if n == nil {
			res.Unparsable = append(res.Unparsable, Problem{lineNo, strings.TrimRight(line, " "), reason})
			continue
		}
		n.Line = lineNo
		if n.ID == RootID {
			res.Unparsable = append(res.Unparsable, Problem{lineNo, strings.TrimRight(line, " "),
				"eigene ID " + RootID + " ist für die oberste Ebene reserviert"})
			continue
		}
		if first, dup := firstLine[n.ID]; dup {
			res.Unparsable = append(res.Unparsable, Problem{lineNo, strings.TrimRight(line, " "),
				fmt.Sprintf("doppelte ID %s, erstes Vorkommen in Zeile %d", n.ID, first)})
			continue
		}
		firstLine[n.ID] = lineNo
		res.Nodes = append(res.Nodes, n)
		if n.Kind == KindFolder {
			res.Folders++
		} else {
			res.Transactions++
		}
		if n.Fallback {
			res.Fallbacks++
		}
	}
	res.resolveParents()
	return res, nil
}

// parseLine liefert den Knoten oder den Grund, warum die Zeile nicht passt.
func parseLine(line string) (*Node, string) {
	r := []rune(line)
	if n := parseColumns(r); n != nil {
		return n, ""
	}
	return parseFallback(line)
}

func parseColumns(r []rune) *Node {
	if len(r) < 60 {
		return nil
	}
	var kind Kind
	switch string(r[0:2]) {
	case "TR":
		kind = KindTx
	case "  ":
		kind = KindFolder
	default:
		return nil
	}
	parent, id := string(r[2:7]), string(r[7:12])
	if !isDigits(parent) || !isDigits(id) {
		return nil
	}
	tcode := strings.TrimSpace(string(r[12:60]))
	if strings.ContainsFunc(tcode, unicode.IsSpace) {
		return nil
	}
	if (kind == KindTx) != (tcode != "") {
		return nil
	}
	return &Node{ID: id, ParentID: parent, Kind: kind, TCode: tcode, Title: strings.TrimSpace(string(r[60:]))}
}

// parseFallback zerlegt Zeilen, deren Spalten verrutscht oder abgeschnitten sind:
// Kennung, zwei fünfstellige IDs (Leerraum dazwischen erlaubt), dann bei
// Transaktionen das erste Wort als T-Code und der Rest als Kurztext.
func parseFallback(line string) (*Node, string) {
	s := line
	kind := KindFolder
	if strings.HasPrefix(s, "TR") {
		kind = KindTx
		s = s[2:]
	}
	s = strings.TrimLeftFunc(s, unicode.IsSpace)

	var ids [2]string
	for k := range ids {
		if len(s) < 5 || !isDigits(s[:5]) {
			if kind == KindFolder && k == 0 {
				return nil, "weder Transaktion (TR) noch Ordner erkennbar"
			}
			return nil, "keine zwei fünfstelligen IDs gefunden"
		}
		ids[k] = s[:5]
		s = strings.TrimLeftFunc(s[5:], unicode.IsSpace)
	}
	if kind == KindFolder && s != "" && unicode.IsDigit(rune(s[0])) {
		return nil, "mehr Ziffern als zwei fünfstellige IDs"
	}

	n := &Node{ID: ids[1], ParentID: ids[0], Kind: kind, Fallback: true}
	if kind == KindTx {
		tcode, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
		if tcode == "" {
			return nil, "Transaktion ohne T-Code"
		}
		n.TCode, n.Title = tcode, strings.TrimSpace(rest)
	} else {
		n.Title = strings.TrimSpace(s)
	}
	return n, ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resolveParents ordnet jedem Knoten einen erreichbaren Parent zu. Knoten,
// deren Parent fehlt, eine Transaktion ist oder die in einem Zyklus hängen,
// werden als Waisen gemeldet und auf die oberste Ebene gesetzt.
func (res *Result) resolveParents() {
	byID := make(map[string]*Node, len(res.Nodes))
	for _, n := range res.Nodes {
		byID[n.ID] = n
	}
	res.effectiveParent = make(map[string]string, len(res.Nodes))
	orphan := func(n *Node, reason string) {
		res.Orphans = append(res.Orphans, Orphan{n.Line, n.ID, n.ParentID, reason})
		res.effectiveParent[n.ID] = RootID
	}
	for _, n := range res.Nodes {
		p, ok := byID[n.ParentID]
		switch {
		case n.ParentID == RootID:
			res.effectiveParent[n.ID] = RootID
		case !ok:
			orphan(n, "Parent-ID existiert nicht")
		case p.Kind != KindFolder:
			orphan(n, "Parent ist eine Transaktion")
		default:
			res.effectiveParent[n.ID] = n.ParentID
		}
	}

	// Zyklen: Knoten, die nach obiger Einordnung nicht erreichbar sind, hängen
	// in einem Kreis. Den ersten (nach Zeile) lösen und erneut prüfen.
	for {
		reached := map[string]bool{}
		var walk func(*TreeNode)
		walk = func(t *TreeNode) {
			reached[t.ID] = true
			for _, c := range t.Children {
				walk(c)
			}
		}
		for _, t := range res.Tree() {
			walk(t)
		}
		var stuck *Node
		for _, n := range res.Nodes {
			if !reached[n.ID] {
				stuck = n
				break
			}
		}
		if stuck == nil {
			return
		}
		orphan(stuck, "Zyklus in der Hierarchie")
	}
}

// Tree baut den Baum ab der obersten Ebene; Waisen stehen oben.
func (res *Result) Tree() []*TreeNode {
	children := map[string][]*TreeNode{}
	for _, n := range res.Nodes {
		p := res.effectiveParent[n.ID]
		children[p] = append(children[p], &TreeNode{Node: n})
	}
	var attach func([]*TreeNode)
	attach = func(level []*TreeNode) {
		for _, t := range level {
			if t.Kind == KindFolder {
				t.Children = children[t.ID]
				delete(children, t.ID) // schützt vor Endlosschleife bei Zyklen
				attach(t.Children)
			}
		}
	}
	top := children[RootID]
	delete(children, RootID)
	attach(top)
	return top
}
