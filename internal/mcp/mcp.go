// Package mcp stellt die Favoriten als MCP-Server über Streamable HTTP bereit.
//
// Protokoll: JSON-RPC 2.0 auf einem einzigen POST-Endpunkt. Unterstützt werden
// die Revision 2026-07-28 (Protokollversion und Identität als Metadaten je
// Anfrage, keine Sessions, kein Handshake) und ältere Clients, die den
// initialize-Handshake erwarten. Die Spezifikation nennt Server, die beides
// beherrschen, "dual-era" — welche Revision ein Claude-Client spricht, steht
// vorher nicht fest.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/maxkhl/sapfav/internal/store"
)

// VersionModern ist die Revision ohne Handshake und ohne Sessions.
const VersionModern = "2026-07-28"

// legacyVersions sind Revisionen mit initialize-Handshake, absteigend.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const (
	serverName    = "sapfav"
	serverVersion = "1"
	maxBody       = 1 << 20 // 1 MB

	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaServerInfo      = "io.modelcontextprotocol/serverInfo"

	instructions = "Verwaltet SAP-Transaktionscode-Favoriten als Baum aus Ordnern " +
		"und Transaktionen. Jeder Eintrag liegt entweder im Bereich Allgemein " +
		"(customer_id null) oder bei genau einem Kunden. Unter einem Ordner gilt " +
		"immer dessen Bereich. Mit set_children_order lässt sich eine ganze Ebene " +
		"in einem Aufruf sortieren."
)

// JSON-RPC-Fehlercodes. -32020 und -32022 sind von MCP vergeben.
const (
	codeParse              = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeInternal           = -32603
	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
)

// Server beantwortet MCP-Anfragen auf dem Datenbestand.
type Server struct {
	store *store.Store
	log   *slog.Logger
	tools map[string]*tool
	order []string // deterministische Reihenfolge für tools/list
}

func New(st *store.Store, log *slog.Logger) *Server {
	s := &Server{store: st, log: log, tools: map[string]*tool{}}
	s.register()
	return s
}

func supportedVersions() []string {
	return append([]string{VersionModern}, legacyVersions...)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handle beantwortet einen POST auf den MCP-Endpunkt.
func (s *Server) Handle(w http.ResponseWriter, r *http.Request) {
	// Pflicht gegen DNS-Rebinding: ein vorhandener, fremder Origin wird abgewiesen.
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
		s.log.Warn("MCP-Anfrage mit fremdem Origin abgelehnt", "origin", origin, "host", r.Host)
		s.write(w, http.StatusForbidden, &rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: codeInvalidRequest, Message: "Origin nicht erlaubt"}})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.write(w, http.StatusBadRequest, &rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: codeParse, Message: "Body nicht lesbar oder größer als 1 MB"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.write(w, http.StatusBadRequest, &rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: codeParse, Message: "ungültiges JSON: " + err.Error()}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		s.write(w, http.StatusBadRequest, &rpcResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: codeInvalidRequest, Message: `jsonrpc muss "2.0" sein und method gesetzt`}})
		return
	}
	// Ohne id ist es eine Benachrichtigung (z. B. notifications/initialized).
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	version, modern := requestVersion(req.Params)
	if modern {
		if rpcErr := validateHeaders(r, &req, version); rpcErr != nil {
			s.write(w, http.StatusBadRequest, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
	}

	result, status, rpcErr := s.dispatch(r.Context(), &req, modern)
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
	s.write(w, status, resp)
}

// MethodNotAllowed bedient GET und DELETE auf dem Endpunkt: die
// Session- und SSE-Mechanismen älterer Revisionen gibt es hier nicht.
func (s *Server) MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	s.write(w, http.StatusMethodNotAllowed, &rpcResponse{JSONRPC: "2.0",
		Error: &rpcError{Code: codeInvalidRequest, Message: "nur POST wird unterstützt"}})
}

func (s *Server) dispatch(ctx context.Context, req *rpcRequest, modern bool) (any, int, *rpcError) {
	switch req.Method {
	case "server/discover":
		return s.discover(modern), http.StatusOK, nil
	case "initialize":
		if modern {
			break // Handshake gehört nicht zu dieser Revision
		}
		return s.initialize(req.Params), http.StatusOK, nil
	case "ping":
		return map[string]any{}, http.StatusOK, nil
	case "tools/list":
		return s.listTools(modern), http.StatusOK, nil
	case "tools/call":
		return s.callTool(ctx, req.Params, modern)
	}
	// Benachrichtigungen gehören ohne id gesendet (dann greift der Zweig in
	// Handle). Schickt ein Client sie trotzdem mit id, wird das nicht zum
	// Fehler: ein 404 auf notifications/initialized würde den Handshake
	// älterer Clients abbrechen.
	if strings.HasPrefix(req.Method, "notifications/") {
		return map[string]any{}, http.StatusOK, nil
	}
	// Unbekannte Methode: 404 mit JSON-RPC-Fehler, damit Clients einen
	// modernen Server von einem alten HTTP+SSE-Server unterscheiden können.
	return nil, http.StatusNotFound, &rpcError{Code: codeMethodNotFound,
		Message: "Method not found: " + req.Method}
}

func (s *Server) discover(modern bool) map[string]any {
	res := map[string]any{
		"supportedVersions": supportedVersions(),
		"capabilities":      map[string]any{"tools": map[string]any{}},
		"instructions":      instructions,
		"_meta": map[string]any{
			metaServerInfo: map[string]any{"name": serverName, "version": serverVersion},
		},
	}
	return withResultType(res, modern)
}

// initialize bedient den Handshake älterer Clients.
func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := p.ProtocolVersion
	if !slices.Contains(legacyVersions, version) {
		// Der Client bekommt die neueste Revision, die wir mit Handshake sprechen.
		version = legacyVersions[0]
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		"instructions":    instructions,
	}
}

func (s *Server) listTools(modern bool) map[string]any {
	list := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		list = append(list, s.tools[name].definition())
	}
	return withResultType(map[string]any{"tools": list}, modern)
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage, modern bool) (any, int, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, http.StatusOK, &rpcError{Code: codeInvalidParams, Message: "params nicht lesbar: " + err.Error()}
	}
	t := s.tools[p.Name]
	if t == nil {
		return nil, http.StatusOK, &rpcError{Code: codeInvalidParams, Message: "Unknown tool: " + p.Name}
	}

	out, err := t.run(ctx, p.Arguments)
	if err != nil {
		// Fehler, aus denen das Modell lernen kann, gehen als Tool-Ergebnis
		// zurück; alles andere ist ein Serverfehler.
		if errors.Is(err, errBadArgs) || errors.Is(err, store.ErrInvalid) ||
			errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return toolResult(modern, err.Error(), nil, true), http.StatusOK, nil
		}
		s.log.Error("MCP-Tool fehlgeschlagen", "tool", p.Name, "err", err)
		return nil, http.StatusOK, &rpcError{Code: codeInternal, Message: "interner Fehler"}
	}
	text, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		s.log.Error("MCP-Ergebnis nicht serialisierbar", "tool", p.Name, "err", err)
		return nil, http.StatusOK, &rpcError{Code: codeInternal, Message: "interner Fehler"}
	}
	return toolResult(modern, string(text), out, false), http.StatusOK, nil
}

// toolResult baut ein Tool-Ergebnis. Der Text enthält dieselben Daten als JSON,
// damit auch Clients ohne structuredContent damit arbeiten können.
func toolResult(modern bool, text string, structured any, isError bool) map[string]any {
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
	if structured != nil {
		res["structuredContent"] = structured
	}
	return withResultType(res, modern)
}

// withResultType ergänzt das Feld, das erst die moderne Revision kennt.
func withResultType(res map[string]any, modern bool) map[string]any {
	if modern {
		res["resultType"] = "complete"
	}
	return res
}

// requestVersion liest die Protokollversion aus den Metadaten der Anfrage.
// Fehlt sie, spricht der Client eine Revision mit Handshake.
func requestVersion(params json.RawMessage) (string, bool) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", false
	}
	raw, ok := p.Meta[metaProtocolVersion]
	if !ok {
		return "", false
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil || version == "" {
		return "", false
	}
	return version, true
}

// validateHeaders prüft die Header, die die moderne Revision verlangt. Sie
// spiegeln Felder aus dem Body, damit Proxys routen können, ohne ihn zu lesen —
// und müssen deshalb zum Body passen.
func validateHeaders(r *http.Request, req *rpcRequest, version string) *rpcError {
	header := r.Header.Get("MCP-Protocol-Version")
	if header == "" {
		return &rpcError{Code: codeHeaderMismatch, Message: "Header MCP-Protocol-Version fehlt"}
	}
	if header != version {
		return mismatch("MCP-Protocol-Version", header, version)
	}
	if version != VersionModern {
		return &rpcError{Code: codeUnsupportedVersion, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": supportedVersions(), "requested": version}}
	}
	if got := r.Header.Get("Mcp-Method"); got != req.Method {
		return mismatch("Mcp-Method", got, req.Method)
	}
	if want, ok := bodyName(req); ok {
		got, err := decodeHeaderValue(r.Header.Get("Mcp-Name"))
		if err != nil {
			return &rpcError{Code: codeHeaderMismatch, Message: "Mcp-Name ist nicht korrekt kodiert"}
		}
		if got != want {
			return mismatch("Mcp-Name", got, want)
		}
	}
	return nil
}

func mismatch(header, got, want string) *rpcError {
	return &rpcError{Code: codeHeaderMismatch, Message: fmt.Sprintf(
		"Header mismatch: %s header value %q does not match body value %q", header, got, want)}
}

// bodyName liefert den Wert, den der Header Mcp-Name spiegeln muss.
func bodyName(req *rpcRequest) (string, bool) {
	var field string
	switch req.Method {
	case "tools/call", "prompts/get":
		field = "name"
	case "resources/read":
		field = "uri"
	default:
		return "", false
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return "", false
	}
	raw, ok := p[field]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// decodeHeaderValue löst die Base64-Sonderform =?base64?…?= auf, mit der
// Clients Werte übertragen, die nicht als ASCII-Header zulässig sind.
func decodeHeaderValue(value string) (string, error) {
	const prefix, suffix = "=?base64?", "?="
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return value, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// sameOrigin vergleicht den Origin-Header mit dem Host der Anfrage.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func (s *Server) write(w http.ResponseWriter, status int, resp *rpcResponse) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		s.log.Error("MCP-Antwort nicht serialisierbar", "err", err)
		http.Error(w, "interner Fehler", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}
