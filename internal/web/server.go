// Package web stellt die REST-API und das eingebettete Frontend bereit.
package web

import (
	"log/slog"
	"net/http"

	"github.com/maxkhl/sapfav/internal/store"
)

type server struct {
	store *store.Store
	log   *slog.Logger
}

// New liefert den HTTP-Handler der Anwendung.
func New(st *store.Store, log *slog.Logger) http.Handler {
	s := &server{store: st, log: log}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /api/tree", s.tree)
	mux.HandleFunc("GET /api/export", s.export)

	mux.HandleFunc("POST /api/nodes", requireJSON(s.createNode))
	mux.HandleFunc("PUT /api/nodes/{id}", requireJSON(s.updateNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", requireJSON(s.deleteNode))
	mux.HandleFunc("POST /api/nodes/{id}/move", requireJSON(s.moveNode))
	mux.HandleFunc("POST /api/nodes/{id}/reorder", requireJSON(s.reorderNode))

	mux.HandleFunc("POST /api/import/preview", requireUpload(s.importPreview))
	mux.HandleFunc("POST /api/import", requireUpload(s.importRun))

	return mux
}
