package sapfile

import (
	"bytes"
	"fmt"

	"golang.org/x/text/encoding/charmap"
)

const (
	tcodeWidth = 48  // Spalten 12-59
	titleWidth = 132 // ab Spalte 60, SAP füllt die Zeile auf 192 Zeichen auf
	maxID      = 99999
)

// WriteNode ist ein Knoten für den Export; Kinder in Ausgabereihenfolge.
type WriteNode struct {
	Kind     Kind
	TCode    string
	Title    string
	Children []*WriteNode
}

// Warning meldet einen Eintrag, der nicht verlustfrei exportiert werden konnte.
type Warning struct {
	TCode  string `json:"tcode,omitempty"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

type Encoded struct {
	Data         []byte    `json:"-"`
	Folders      int       `json:"folders"`
	Transactions int       `json:"transactions"`
	Warnings     []Warning `json:"warnings"`
}

// Encode schreibt die Knoten im Format des SAP-Favoriten-Exports: cp1252,
// CRLF, feste Spalten, jede Zeile 192 Zeichen. IDs werden fortlaufend ab 00002
// vergeben, Ordner stehen vor ihrem Inhalt.
func Encode(nodes []*WriteNode) (*Encoded, error) {
	out := &Encoded{Warnings: []Warning{}}
	var buf bytes.Buffer
	next := 2

	var write func(level []*WriteNode, parent int) error
	write = func(level []*WriteNode, parent int) error {
		for _, n := range level {
			warn := func(reason string) {
				out.Warnings = append(out.Warnings, Warning{TCode: n.TCode, Title: n.Title, Reason: reason})
			}

			prefix := "  "
			tcode := bytes.Repeat([]byte(" "), tcodeWidth)
			if n.Kind == KindTx {
				b, replaced := encodeCP1252(n.TCode)
				if len(b) > tcodeWidth {
					warn(fmt.Sprintf("T-Code länger als %d Zeichen – nicht exportiert", tcodeWidth))
					continue
				}
				if replaced {
					warn("T-Code enthält Zeichen außerhalb von cp1252 – durch ? ersetzt")
				}
				prefix = "TR"
				copy(tcode, b)
			}

			title, replaced := encodeCP1252(n.Title)
			if replaced {
				warn("Kurztext enthält Zeichen außerhalb von cp1252 – durch ? ersetzt")
			}
			if len(title) > titleWidth {
				title = title[:titleWidth]
				warn(fmt.Sprintf("Kurztext auf %d Zeichen gekürzt", titleWidth))
			}

			if next > maxID {
				return fmt.Errorf("mehr als %d Einträge passen nicht in das SAP-Format", maxID-1)
			}
			id := next
			next++

			fmt.Fprintf(&buf, "%s%05d%05d", prefix, parent, id)
			buf.Write(tcode)
			buf.Write(title)
			buf.Write(bytes.Repeat([]byte(" "), titleWidth-len(title)))
			buf.WriteString("\r\n")

			if n.Kind == KindFolder {
				out.Folders++
				if err := write(n.Children, id); err != nil {
					return err
				}
			} else {
				out.Transactions++
			}
		}
		return nil
	}

	if err := write(nodes, 1); err != nil {
		return nil, err
	}
	out.Data = buf.Bytes()
	return out, nil
}

// encodeCP1252 kodiert s Zeichen für Zeichen. Nicht darstellbare Zeichen werden
// zu '?', Steuerzeichen (z. B. Zeilenumbrüche) zu Leerzeichen, damit die feste
// Zeilenstruktur erhalten bleibt.
func encodeCP1252(s string) (b []byte, replaced bool) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b = append(b, ' ')
			continue
		}
		c, ok := charmap.Windows1252.EncodeRune(r)
		if !ok {
			c, replaced = '?', true
		}
		b = append(b, c)
	}
	return b, replaced
}
