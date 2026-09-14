package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/maxkhl/sapfav/internal/sapimport"
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
		"nodes":       tree,
	})
}

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
	// leerer Body nicht versehentlich nach oben verschiebt.
	var in struct {
		ParentID json.RawMessage `json:"parent_id"`
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
	n, err := s.store.Move(r.Context(), id, parentID)
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

// parseUpload liest Datei und Encoding aus dem Multipart-Formular und parst sie.
func (s *server) parseUpload(w http.ResponseWriter, r *http.Request) (*sapimport.Result, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(maxUploadBody); err != nil {
		writeError(w, http.StatusBadRequest, "Upload fehlerhaft oder größer als 2 MB")
		return nil, false
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Datei fehlt (Feld 'file')")
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Datei nicht lesbar")
		return nil, false
	}
	res, err := sapimport.Parse(data, r.FormValue("encoding"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return res, true
}

func (s *server) importPreview(w http.ResponseWriter, r *http.Request) {
	res, ok := s.parseUpload(w, r)
	if !ok {
		return
	}
	existing, err := s.store.Count(r.Context())
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
	res, ok := s.parseUpload(w, r)
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
	stats, err := s.store.Import(r.Context(), res.Tree(), replace)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
