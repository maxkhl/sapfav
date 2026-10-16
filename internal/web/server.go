// Package web stellt die REST-API und das eingebettete Frontend bereit.
package web

import (
	"log/slog"
	"net/http"

	"github.com/maxkhl/sapfav/internal/mcp"
	"github.com/maxkhl/sapfav/internal/store"
)

type server struct {
	store *store.Store
	log   *slog.Logger
}

// New liefert den HTTP-Handler der Anwendung. Ist mcpToken leer, wird der
// MCP-Endpunkt nicht registriert.
func New(st *store.Store, log *slog.Logger, mcpToken string) http.Handler {
	s := &server{store: st, log: log}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /api/tree", s.tree)
	mux.HandleFunc("GET /api/export", s.export)
	mux.HandleFunc("GET /api/export/sap", s.sapExport)
	mux.HandleFunc("GET /api/export/sap/preview", s.sapExportPreview)

	mux.HandleFunc("GET /api/customers", s.customers)
	mux.HandleFunc("POST /api/customers", requireJSON(s.createCustomer))
	mux.HandleFunc("PUT /api/customers/{id}", requireJSON(s.updateCustomer))
	mux.HandleFunc("DELETE /api/customers/{id}", requireJSON(s.deleteCustomer))

	mux.HandleFunc("POST /api/nodes", requireJSON(s.createNode))
	mux.HandleFunc("PUT /api/nodes/{id}", requireJSON(s.updateNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", requireJSON(s.deleteNode))
	mux.HandleFunc("POST /api/nodes/{id}/move", requireJSON(s.moveNode))
	mux.HandleFunc("POST /api/nodes/{id}/reorder", requireJSON(s.reorderNode))

	mux.HandleFunc("POST /api/import/preview", requireUpload(s.importPreview))
	mux.HandleFunc("POST /api/import", requireUpload(s.importRun))

	if mcpToken != "" {
		m := mcp.New(st, log)
		mux.HandleFunc("POST /mcp", requireToken(log, mcpToken, m.Handle))
		// Die Session- und SSE-Mechanismen älterer Revisionen gibt es nicht.
		mux.HandleFunc("GET /mcp", requireToken(log, mcpToken, m.MethodNotAllowed))
		mux.HandleFunc("DELETE /mcp", requireToken(log, mcpToken, m.MethodNotAllowed))
	}

	mux.Handle("GET /", staticHandler())

	return mux
}
