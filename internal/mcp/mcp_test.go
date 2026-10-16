package mcp

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxkhl/sapfav/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type rpcResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// modernBody baut eine Anfrage der Revision 2026-07-28 mit Metadaten im Body.
func modernBody(method string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		metaProtocolVersion:                          VersionModern,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
}

// send schickt einen Body an den Endpunkt; tweak darf Header verbiegen.
func send(t *testing.T, s *Server, body any, tweak func(*http.Request)) (*httptest.ResponseRecorder, rpcResult) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://sapfav.example/mcp", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if m, ok := body.(map[string]any); ok {
		if method, ok := m["method"].(string); ok {
			req.Header.Set("MCP-Protocol-Version", VersionModern)
			req.Header.Set("Mcp-Method", method)
			if params, ok := m["params"].(map[string]any); ok {
				if name, ok := params["name"].(string); ok {
					req.Header.Set("Mcp-Name", name)
				}
			}
		}
	}
	if tweak != nil {
		tweak(req)
	}
	rec := httptest.NewRecorder()
	s.Handle(rec, req)
	var out rpcResult
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("Antwort nicht lesbar: %v (%s)", err, rec.Body)
		}
	}
	return rec, out
}

// modern schickt eine moderne Anfrage mit passenden Headern.
func modern(t *testing.T, s *Server, method string, params map[string]any) (*httptest.ResponseRecorder, rpcResult) {
	t.Helper()
	return send(t, s, modernBody(method, params), nil)
}

func TestDiscover(t *testing.T) {
	s := newTestServer(t)
	rec, out := modern(t, s, "server/discover", nil)
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Fatalf("%d %+v", rec.Code, out.Error)
	}
	var res struct {
		ResultType        string              `json:"resultType"`
		SupportedVersions []string            `json:"supportedVersions"`
		Capabilities      map[string]any      `json:"capabilities"`
		Instructions      string              `json:"instructions"`
		Meta              map[string]struct { // serverInfo
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(out.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ResultType != "complete" {
		t.Errorf("resultType %q", res.ResultType)
	}
	if len(res.SupportedVersions) == 0 || res.SupportedVersions[0] != VersionModern {
		t.Errorf("supportedVersions %v", res.SupportedVersions)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("capabilities %v", res.Capabilities)
	}
	if res.Instructions == "" {
		t.Error("instructions fehlen")
	}
	if res.Meta[metaServerInfo].Name != serverName {
		t.Errorf("serverInfo %+v", res.Meta)
	}
}

func TestToolsListAndCall(t *testing.T) {
	s := newTestServer(t)
	_, out := modern(t, s, "tools/list", nil)
	var list struct {
		ResultType string `json:"resultType"`
		Tools      []struct {
			Name        string          `json:"name"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations struct {
				ReadOnly    bool `json:"readOnlyHint"`
				Destructive bool `json:"destructiveHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out.Result, &list); err != nil {
		t.Fatal(err)
	}
	if list.ResultType != "complete" {
		t.Errorf("resultType %q", list.ResultType)
	}
	if len(list.Tools) != 11 {
		t.Errorf("%d Tools, want 11", len(list.Tools))
	}
	seen := map[string]bool{}
	for _, tl := range list.Tools {
		if tl.Name == "" || tl.Title == "" || tl.Description == "" {
			t.Errorf("unvollständige Definition: %+v", tl)
		}
		if seen[tl.Name] {
			t.Errorf("Tool %s doppelt", tl.Name)
		}
		seen[tl.Name] = true
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil || schema.Type != "object" {
			t.Errorf("%s: inputSchema %s (%v)", tl.Name, tl.InputSchema, err)
		}
		if tl.Name == "delete_node" && !tl.Annotations.Destructive {
			t.Error("delete_node ist nicht als destruktiv markiert")
		}
		if tl.Name == "get_tree" && !tl.Annotations.ReadOnly {
			t.Error("get_tree ist nicht als lesend markiert")
		}
	}

	// Reihenfolge muss über Aufrufe stabil bleiben.
	_, again := modern(t, s, "tools/list", nil)
	if string(again.Result) != string(out.Result) {
		t.Error("tools/list ist nicht deterministisch")
	}

	rec, call := modern(t, s, "tools/call", map[string]any{"name": "list_customers", "arguments": map[string]any{}})
	if rec.Code != http.StatusOK || call.Error != nil {
		t.Fatalf("%d %+v", rec.Code, call.Error)
	}
	var result struct {
		ResultType        string `json:"resultType"`
		IsError           bool   `json:"isError"`
		StructuredContent struct {
			GeneralCount int           `json:"general_count"`
			Customers    []customerOut `json:"customers"`
		} `json:"structuredContent"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(call.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ResultType != "complete" || result.IsError {
		t.Errorf("%+v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || !strings.Contains(result.Content[0].Text, "general_count") {
		t.Errorf("content %+v", result.Content)
	}
	if result.StructuredContent.GeneralCount != 0 || len(result.StructuredContent.Customers) != 0 {
		t.Errorf("structuredContent %+v", result.StructuredContent)
	}
}

func TestHeaderValidation(t *testing.T) {
	s := newTestServer(t)
	body := modernBody("tools/call", map[string]any{"name": "list_customers", "arguments": map[string]any{}})

	cases := []struct {
		name  string
		tweak func(*http.Request)
	}{
		{"Version fehlt", func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }},
		{"Version weicht ab", func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-06-18") }},
		{"Methode weicht ab", func(r *http.Request) { r.Header.Set("Mcp-Method", "tools/list") }},
		{"Methode fehlt", func(r *http.Request) { r.Header.Del("Mcp-Method") }},
		{"Name weicht ab", func(r *http.Request) { r.Header.Set("Mcp-Name", "get_tree") }},
		{"Name fehlt", func(r *http.Request) { r.Header.Del("Mcp-Name") }},
		{"Name falsch kodiert", func(r *http.Request) { r.Header.Set("Mcp-Name", "=?base64?###?=") }},
	}
	for _, c := range cases {
		rec, out := send(t, s, body, c.tweak)
		if rec.Code != http.StatusBadRequest || out.Error == nil || out.Error.Code != codeHeaderMismatch {
			t.Errorf("%s: %d %+v, want 400 und %d", c.name, rec.Code, out.Error, codeHeaderMismatch)
		}
	}

	// Base64-Sonderform muss akzeptiert werden.
	rec, out := send(t, s, body, func(r *http.Request) {
		r.Header.Set("Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte("list_customers"))+"?=")
	})
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Errorf("Base64-Name: %d %+v", rec.Code, out.Error)
	}
}

func TestUnsupportedProtocolVersion(t *testing.T) {
	s := newTestServer(t)
	body := modernBody("tools/list", nil)
	body["params"].(map[string]any)["_meta"].(map[string]any)[metaProtocolVersion] = "1900-01-01"

	rec, out := send(t, s, body, func(r *http.Request) {
		r.Header.Set("MCP-Protocol-Version", "1900-01-01")
	})
	if rec.Code != http.StatusBadRequest || out.Error == nil || out.Error.Code != codeUnsupportedVersion {
		t.Fatalf("%d %+v", rec.Code, out.Error)
	}
	data, _ := json.Marshal(out.Error.Data)
	var d struct {
		Supported []string `json:"supported"`
		Requested string   `json:"requested"`
	}
	json.Unmarshal(data, &d)
	if len(d.Supported) == 0 || d.Supported[0] != VersionModern || d.Requested != "1900-01-01" {
		t.Errorf("data %+v", d)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	s := newTestServer(t)
	rec, out := modern(t, s, "resources/list", nil)
	if rec.Code != http.StatusNotFound || out.Error == nil || out.Error.Code != codeMethodNotFound {
		t.Errorf("unbekannte Methode: %d %+v", rec.Code, out.Error)
	}

	rec, out = modern(t, s, "tools/call", map[string]any{"name": "gibt_es_nicht", "arguments": map[string]any{}})
	if rec.Code != http.StatusOK || out.Error == nil || out.Error.Code != codeInvalidParams {
		t.Errorf("unbekanntes Tool: %d %+v", rec.Code, out.Error)
	}

	// initialize gehört nicht zur modernen Revision.
	rec, out = modern(t, s, "initialize", nil)
	if rec.Code != http.StatusNotFound || out.Error == nil || out.Error.Code != codeMethodNotFound {
		t.Errorf("modernes initialize: %d %+v", rec.Code, out.Error)
	}
}

func TestNotificationsAndBadRequests(t *testing.T) {
	s := newTestServer(t)
	rec, _ := send(t, s, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("Benachrichtigung: %d %q", rec.Code, rec.Body)
	}

	req := httptest.NewRequest("POST", "http://sapfav.example/mcp", strings.NewReader("{kein json"))
	recBad := httptest.NewRecorder()
	s.Handle(recBad, req)
	if recBad.Code != http.StatusBadRequest || !strings.Contains(recBad.Body.String(), "-32700") {
		t.Errorf("kaputtes JSON: %d %s", recBad.Code, recBad.Body)
	}

	rec, out := send(t, s, map[string]any{"id": 1, "method": "tools/list"}, nil)
	if rec.Code != http.StatusBadRequest || out.Error == nil || out.Error.Code != codeInvalidRequest {
		t.Errorf("fehlendes jsonrpc: %d %+v", rec.Code, out.Error)
	}
}

func TestOriginAndMethodNotAllowed(t *testing.T) {
	s := newTestServer(t)
	body := modernBody("tools/list", nil)

	rec, out := send(t, s, body, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if rec.Code != http.StatusForbidden || out.Error == nil {
		t.Errorf("fremder Origin: %d %+v", rec.Code, out.Error)
	}
	rec, out = send(t, s, body, func(r *http.Request) { r.Header.Set("Origin", "https://sapfav.example") })
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Errorf("eigener Origin: %d %+v", rec.Code, out.Error)
	}

	for _, method := range []string{"GET", "DELETE"} {
		req := httptest.NewRequest(method, "http://sapfav.example/mcp", nil)
		rec := httptest.NewRecorder()
		s.MethodNotAllowed(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s: %d %q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

// Ältere Clients erwarten den initialize-Handshake und kennen weder die
// Metadaten im Body noch die Header.
func TestLegacyEra(t *testing.T) {
	s := newTestServer(t)
	legacy := func(method string, params map[string]any) (*httptest.ResponseRecorder, rpcResult) {
		body := map[string]any{"jsonrpc": "2.0", "method": method}
		if !strings.HasPrefix(method, "notifications/") {
			body["id"] = 1 // Benachrichtigungen tragen keine id
		}
		if params != nil {
			body["params"] = params
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "http://sapfav.example/mcp", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handle(rec, req)
		var out rpcResult
		if rec.Body.Len() > 0 {
			json.Unmarshal(rec.Body.Bytes(), &out)
		}
		return rec, out
	}

	rec, out := legacy("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "alt", "version": "1"},
	})
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Fatalf("initialize: %d %+v", rec.Code, out.Error)
	}
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(out.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != serverName {
		t.Errorf("%+v", init)
	}
	if init.ResultType != "" {
		t.Error("resultType darf bei alten Clients nicht gesetzt sein")
	}
	if _, ok := init.Capabilities["tools"]; !ok {
		t.Errorf("capabilities %v", init.Capabilities)
	}

	// Unbekannte Version: der Server nennt seine neueste mit Handshake.
	_, out = legacy("initialize", map[string]any{"protocolVersion": "1900-01-01"})
	json.Unmarshal(out.Result, &init)
	if init.ProtocolVersion != legacyVersions[0] {
		t.Errorf("Rückfall-Version: %q", init.ProtocolVersion)
	}

	if rec, _ := legacy("notifications/initialized", nil); rec.Code != http.StatusAccepted {
		t.Errorf("notifications/initialized: %d", rec.Code)
	}
	// Nicht spezifikationstreue Clients schicken Benachrichtigungen mit id;
	// das darf kein 404 sein, sonst bricht ihr Handshake ab.
	withID := map[string]any{"jsonrpc": "2.0", "id": 9, "method": "notifications/initialized"}
	if rec, out := send(t, s, withID, func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }); rec.Code != http.StatusOK || out.Error != nil {
		t.Errorf("Benachrichtigung mit id: %d %+v", rec.Code, out.Error)
	}
	if _, out := legacy("ping", nil); out.Error != nil {
		t.Errorf("ping: %+v", out.Error)
	}

	rec, out = legacy("tools/list", nil)
	if rec.Code != http.StatusOK || out.Error != nil {
		t.Fatalf("tools/list: %d %+v", rec.Code, out.Error)
	}
	var list struct {
		Tools      []map[string]any `json:"tools"`
		ResultType string           `json:"resultType"`
	}
	json.Unmarshal(out.Result, &list)
	if len(list.Tools) != 11 || list.ResultType != "" {
		t.Errorf("%d Tools, resultType %q", len(list.Tools), list.ResultType)
	}

	_, out = legacy("tools/call", map[string]any{"name": "list_customers", "arguments": map[string]any{}})
	var result struct {
		IsError    bool   `json:"isError"`
		ResultType string `json:"resultType"`
	}
	json.Unmarshal(out.Result, &result)
	if result.IsError || result.ResultType != "" {
		t.Errorf("%+v", result)
	}
}
