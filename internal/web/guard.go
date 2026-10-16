package web

import (
	"crypto/subtle"
	"log/slog"
	"mime"
	"net/http"
)

// Die App hat keine eigene Authentifizierung, sie verlässt sich auf die
// Authentik-Session davor. Ein Cross-Site-Formular könnte diese Session
// mitschicken. Formulare können aber weder Content-Type application/json
// noch eigene Header setzen, und für beides verlangt der Browser bei
// Cross-Origin-Requests einen CORS-Preflight, den diese App nie erlaubt.

// requireJSON lässt nur Requests mit Content-Type application/json durch.
func requireJSON(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mediaType(r) != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type muss application/json sein")
			return
		}
		next(w, r)
	}
}

// requireUpload lässt nur multipart-Uploads mit X-Requested-With durch.
func requireUpload(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mediaType(r) != "multipart/form-data" {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type muss multipart/form-data sein")
			return
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			writeError(w, http.StatusForbidden, "Header X-Requested-With: XMLHttpRequest fehlt")
			return
		}
		next(w, r)
	}
}

func mediaType(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mt
}

// requireToken schützt den MCP-Endpunkt mit einem festen Bearer-Token.
// Die Oberfläche liegt hinter Authentik Forward Auth, aber ein MCP-Client kann
// keine Browser-Anmeldung durchlaufen. Deshalb bekommt /mcp einen eigenen
// Token und wird in Authentik als unauthenticated path eingetragen.
func requireToken(log *slog.Logger, token string, next http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + token)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			// Hilft beim Einrichten: fehlt der Header ganz oder passt er nicht?
			log.Warn("MCP-Anfrage ohne gültigen Token", "method", r.Method,
				"authorization_header", r.Header.Get("Authorization") != "",
				"hinweis", `Wert muss "Bearer <Token>" sein`)
			w.Header().Set("WWW-Authenticate", `Bearer realm="sapfav"`)
			writeError(w, http.StatusUnauthorized, "Authorization: Bearer <Token> fehlt oder ist falsch")
			return
		}
		next(w, r)
	}
}
