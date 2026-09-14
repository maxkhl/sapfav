package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxkhl/sapfav/internal/store"
)

func newServer(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, h http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return do(t, h, method, path, "application/json", body)
}

func upload(t *testing.T, h http.Handler, path string, fields map[string]string, xhr bool) *httptest.ResponseRecorder {
	t.Helper()
	data, err := os.ReadFile("../../testdata/sap-favorites-export.txt")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "favoriten")
	fw.Write(data)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()

	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if xhr {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWriteEndpointsRequireJSON(t *testing.T) {
	h := newServer(t)
	cases := []struct{ method, path string }{
		{"POST", "/api/nodes"},
		{"PUT", "/api/nodes/1"},
		{"DELETE", "/api/nodes/1"},
		{"POST", "/api/nodes/1/move"},
		{"POST", "/api/nodes/1/reorder"},
	}
	for _, c := range cases {
		for _, ct := range []string{"application/x-www-form-urlencoded", "text/plain", "multipart/form-data; boundary=x", ""} {
			rec := do(t, h, c.method, c.path, ct, "kind=folder&title=x")
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s mit %q: %d, want 415", c.method, c.path, ct, rec.Code)
			}
		}
	}
	if rec := do(t, h, "POST", "/api/nodes", "application/json; charset=utf-8", `{"kind":"folder","title":"x"}`); rec.Code != http.StatusCreated {
		t.Errorf("JSON mit charset: %d %s", rec.Code, rec.Body)
	}
}

func TestImportRequiresXHRAndMultipart(t *testing.T) {
	h := newServer(t)
	for _, path := range []string{"/api/import/preview", "/api/import"} {
		if rec := upload(t, h, path, map[string]string{"mode": "replace"}, false); rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne X-Requested-With: %d", path, rec.Code)
		}
		if rec := doJSON(t, h, "POST", path, `{}`); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s mit JSON: %d", path, rec.Code)
		}
	}
}

func TestImportFlow(t *testing.T) {
	h := newServer(t)
	doJSON(t, h, "POST", "/api/nodes", `{"kind":"folder","title":"Bestand"}`)

	rec := upload(t, h, "/api/import/preview", map[string]string{"encoding": "cp1252"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var preview struct {
		Folders, Transactions, Existing int
		Unparsable, Orphans             []any
	}
	json.Unmarshal(rec.Body.Bytes(), &preview)
	if preview.Folders != 15 || preview.Transactions != 222 || preview.Existing != 1 ||
		len(preview.Unparsable) != 0 || len(preview.Orphans) != 0 {
		t.Errorf("preview: %+v", preview)
	}
	if rec := upload(t, h, "/api/import/preview", map[string]string{"encoding": "utf-8"}, true); rec.Code != http.StatusBadRequest {
		t.Errorf("cp1252-Datei als UTF-8: %d", rec.Code)
	}

	if rec := upload(t, h, "/api/import", nil, true); rec.Code != http.StatusBadRequest {
		t.Errorf("ohne mode: %d", rec.Code)
	}
	if rec := upload(t, h, "/api/import", map[string]string{"mode": "replace"}, true); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, "GET", "/api/tree", "", "")
	var tree []*store.Node
	json.Unmarshal(rec.Body.Bytes(), &tree)
	count := 0
	var walk func([]*store.Node)
	walk = func(level []*store.Node) {
		for _, n := range level {
			count++
			walk(n.Children)
		}
	}
	walk(tree)
	if count != 237 {
		t.Errorf("Baum nach replace: %d Knoten", count)
	}

	rec = do(t, h, "GET", "/api/export", "", "")
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(rec.Body.String(), "Verfügbarkeitsübersicht") {
		t.Errorf("export: %v", rec.Header())
	}
}

func TestNodeEndpoints(t *testing.T) {
	h := newServer(t)
	var folder, tx store.Node
	json.Unmarshal(doJSON(t, h, "POST", "/api/nodes", `{"kind":"folder","title":"Admin"}`).Body.Bytes(), &folder)
	rec := doJSON(t, h, "POST", "/api/nodes", `{"kind":"tx","tcode":"SM37","title":"Jobs","parent_id":`+itoa(folder.ID)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tx: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &tx)

	checks := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/nodes", `{"kind":"tx","title":"ohne tcode"}`, 400},
		{"POST", "/api/nodes", `{"kind":"folder","title":"x","unbekannt":1}`, 400},
		{"PUT", "/api/nodes/" + itoa(tx.ID), `{"tcode":"SM37","title":"Jobübersicht","description":"Variante /MEINE"}`, 200},
		{"PUT", "/api/nodes/999", `{"title":"x"}`, 404},
		{"PUT", "/api/nodes/abc", `{"title":"x"}`, 400},
		{"POST", "/api/nodes/" + itoa(tx.ID) + "/move", `{}`, 400},
		{"POST", "/api/nodes/" + itoa(folder.ID) + "/move", `{"parent_id":` + itoa(folder.ID) + `}`, 400},
		{"POST", "/api/nodes/" + itoa(tx.ID) + "/move", `{"parent_id":null}`, 200},
		{"POST", "/api/nodes/" + itoa(tx.ID) + "/reorder", `{"direction":"up"}`, 204},
		{"POST", "/api/nodes/" + itoa(tx.ID) + "/move", `{"parent_id":` + itoa(folder.ID) + `}`, 200},
		{"DELETE", "/api/nodes/" + itoa(folder.ID), ``, 409},
		{"DELETE", "/api/nodes/" + itoa(folder.ID), `{"children":"lift"}`, 204},
		{"DELETE", "/api/nodes/" + itoa(tx.ID), `{}`, 204},
	}
	for _, c := range checks {
		if rec := doJSON(t, h, c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", c.method, c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
	if rec := do(t, h, "GET", "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("healthz: %d", rec.Code)
	}
}

func itoa(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
