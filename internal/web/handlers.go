package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maxkhl/sapfav/internal/sapfile"
	"github.com/maxkhl/sapfav/internal/store"
)

const (
	maxJSONBody   = 1 << 20 // 1 MB
	maxUploadBody = 2 << 20 // 2 MB, der echte Export hat rund 46 KB
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// storeError übersetzt Store-Fehler in HTTP-Status.
func (s *server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.log.Error("Anfrage fehlgeschlagen", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "interner Fehler")
	}
}

// decodeJSON liest einen JSON-Body. Ein leerer Body gilt als {}.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "ungültiges JSON: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "ungültige ID")
		return 0, false
	}
	return id, true
}

// customerParam liest eine Kunden-ID aus Formular oder Query. Leer bedeutet
// Allgemein (nil). Ein unbekannter Kunde ist ein 400.
func (s *server) customerParam(w http.ResponseWriter, r *http.Request, value string) (*store.Customer, bool) {
	if value == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "ungültige Kunden-ID")
		return nil, false
	}
	c, err := s.store.GetCustomer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Kunde %d existiert nicht", id))
		return nil, false
	}
	if err != nil {
		s.storeError(w, r, err)
		return nil, false
	}
	return c, true
}

func customerID(c *store.Customer) *int64 {
	if c == nil {
		return nil
	}
	return &c.ID
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func (s *server) tree(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Tree(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

func (s *server) export(w http.ResponseWriter, r *http.Request) {
	customers, err := s.store.Customers(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	tree, err := s.store.Tree(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	now := time.Now().UTC()
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="sapfav-%s.json"`, now.Format("2006-01-02")))
	writeJSON(w, http.StatusOK, map[string]any{
		"exported_at": now.Format(time.RFC3339),
		"customers":   customers,
		"nodes":       tree,
	})
}

// ---------- SAP-Export ----------

// sapEncode baut die SAP-Datei für ?customer=<id> (leer = Allgemein).
// Mit with_general=1 stehen die allgemeinen Einträge flach davor.
func (s *server) sapEncode(w http.ResponseWriter, r *http.Request) (*sapfile.Encoded, string, bool) {
	q := r.URL.Query()
	c, ok := s.customerParam(w, r, q.Get("customer"))
	if !ok {
		return nil, "", false
	}
	tree, err := s.store.Tree(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return nil, "", false
	}

	var top []*store.Node
	filename := "favoriten-allgemein.txt"
	if c == nil || q.Get("with_general") == "1" {
		top = append(top, topLevel(tree, nil)...)
	}
	if c != nil {
		top = append(top, topLevel(tree, &c.ID)...)
		filename = "favoriten-" + slug(c.Name) + ".txt"
	}

	enc, err := sapfile.Encode(toWriteNodes(top))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, "", false
	}
	return enc, filename, true
}

func topLevel(tree []*store.Node, customerID *int64) []*store.Node {
	var out []*store.Node
	for _, n := range tree {
		if (n.CustomerID == nil && customerID == nil) ||
			(n.CustomerID != nil && customerID != nil && *n.CustomerID == *customerID) {
			out = append(out, n)
		}
	}
	return out
}

func toWriteNodes(nodes []*store.Node) []*sapfile.WriteNode {
	out := make([]*sapfile.WriteNode, 0, len(nodes))
	for _, n := range nodes {
		wn := &sapfile.WriteNode{Kind: sapfile.Kind(n.Kind), Title: n.Title, Children: toWriteNodes(n.Children)}
		if n.TCode != nil {
			wn.TCode = *n.TCode
		}
		out = append(out, wn)
	}
	return out
}

// slug macht aus einem Kundennamen einen ASCII-Dateinamen.
func slug(name string) string {
	repl := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "ae", "Ö", "oe", "Ü", "ue", "ß", "ss")
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(repl.Replace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	if out := strings.TrimRight(b.String(), "-"); out != "" {
		return out
	}
	return "kunde"
}

func (s *server) sapExport(w http.ResponseWriter, r *http.Request) {
	enc, filename, ok := s.sapEncode(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=windows-1252")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Write(enc.Data)
}

func (s *server) sapExportPreview(w http.ResponseWriter, r *http.Request) {
	enc, filename, ok := s.sapEncode(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"folders":      enc.Folders,
		"transactions": enc.Transactions,
		"warnings":     enc.Warnings,
		"filename":     filename,
	})
}

// ---------- Kunden ----------

func (s *server) customers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Customers(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var in store.CustomerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := s.store.CreateCustomer(r.Context(), in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) updateCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.CustomerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := s.store.UpdateCustomer(r.Context(), id, in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Nodes string `json:"nodes"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.DeleteCustomer(r.Context(), id, in.Nodes); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Knoten ----------

func (s *server) createNode(w http.ResponseWriter, r *http.Request) {
	var in store.NodeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	n, err := s.store.Create(r.Context(), in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *server) updateNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.NodeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	n, err := s.store.Update(r.Context(), id, in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Children string `json:"children"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.Delete(r.Context(), id, in.Children); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) moveNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// parent_id muss explizit angegeben sein (null = oberste Ebene), damit ein
	// leerer Body nicht versehentlich nach oben verschiebt. customer_id wählt
	// oben den Bereich (null = Allgemein); unter einem Ordner gilt dessen Kunde.
	var in struct {
		ParentID   json.RawMessage `json:"parent_id"`
		CustomerID *int64          `json:"customer_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var parentID *int64
	if len(in.ParentID) == 0 {
		writeError(w, http.StatusBadRequest, "parent_id fehlt (null = oberste Ebene)")
		return
	}
	if !bytes.Equal(in.ParentID, []byte("null")) {
		if err := json.Unmarshal(in.ParentID, &parentID); err != nil {
			writeError(w, http.StatusBadRequest, "parent_id muss eine Zahl oder null sein")
			return
		}
	}
	n, err := s.store.Move(r.Context(), id, parentID, in.CustomerID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *server) reorderNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Direction string `json:"direction"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.Reorder(r.Context(), id, in.Direction); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Import ----------

// parseUpload liest Datei, Encoding und Zielbereich aus dem Multipart-Formular.
func (s *server) parseUpload(w http.ResponseWriter, r *http.Request) (*sapfile.Result, *store.Customer, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(maxUploadBody); err != nil {
		writeError(w, http.StatusBadRequest, "Upload fehlerhaft oder größer als 2 MB")
		return nil, nil, false
	}
	c, ok := s.customerParam(w, r, r.FormValue("customer"))
	if !ok {
		return nil, nil, false
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Datei fehlt (Feld 'file')")
		return nil, nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Datei nicht lesbar")
		return nil, nil, false
	}
	res, err := sapfile.Parse(data, r.FormValue("encoding"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	return res, c, true
}

func (s *server) importPreview(w http.ResponseWriter, r *http.Request) {
	res, c, ok := s.parseUpload(w, r)
	if !ok {
		return
	}
	existing, err := s.store.Count(r.Context(), customerID(c))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"folders":      res.Folders,
		"transactions": res.Transactions,
		"fallbacks":    res.Fallbacks,
		"unparsable":   res.Unparsable,
		"orphans":      res.Orphans,
		"existing":     existing,
	})
}

func (s *server) importRun(w http.ResponseWriter, r *http.Request) {
	res, c, ok := s.parseUpload(w, r)
	if !ok {
		return
	}
	var replace bool
	switch r.FormValue("mode") {
	case "append":
	case "replace":
		replace = true
	default:
		writeError(w, http.StatusBadRequest, "mode muss 'append' oder 'replace' sein")
		return
	}
	if len(res.Nodes) == 0 {
		writeError(w, http.StatusBadRequest, "keine importierbaren Einträge in der Datei")
		return
	}
	stats, err := s.store.Import(r.Context(), res.Tree(), customerID(c), replace)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
