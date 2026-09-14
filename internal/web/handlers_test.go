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

	"github.com/maxkhl/sapfav/internal/sapfile"
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

func countNodes(level []*store.Node) int {
	n := 0
	for _, x := range level {
		n += 1 + countNodes(x.Children)
	}
	return n
}

func itoa(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

func TestWriteEndpointsRequireJSON(t *testing.T) {
	h := newServer(t)
	cases := []struct{ method, path string }{
		{"POST", "/api/nodes"},
		{"PUT", "/api/nodes/1"},
		{"DELETE", "/api/nodes/1"},
		{"POST", "/api/nodes/1/move"},
		{"POST", "/api/nodes/1/reorder"},
		{"POST", "/api/customers"},
		{"PUT", "/api/customers/1"},
		{"DELETE", "/api/customers/1"},
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

	var tree []*store.Node
	json.Unmarshal(do(t, h, "GET", "/api/tree", "", "").Body.Bytes(), &tree)
	if n := countNodes(tree); n != 237 {
		t.Errorf("Baum nach replace: %d Knoten", n)
	}

	rec = do(t, h, "GET", "/api/export", "", "")
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(rec.Body.String(), "Verfügbarkeitsübersicht") ||
		!strings.Contains(rec.Body.String(), `"customers":[]`) {
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
		{"POST", "/api/nodes", `{"kind":"folder","title":"x","customer_id":999}`, 400},
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

func TestCustomerEndpoints(t *testing.T) {
	h := newServer(t)
	rec := doJSON(t, h, "POST", "/api/customers", `{"name":"Müller AG","notes":"S4P/100"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var c store.Customer
	json.Unmarshal(rec.Body.Bytes(), &c)
	cid := itoa(c.ID)

	var folder store.Node
	json.Unmarshal(doJSON(t, h, "POST", "/api/nodes", `{"kind":"folder","title":"Kundenordner","customer_id":`+cid+`}`).Body.Bytes(), &folder)
	if folder.CustomerID == nil || *folder.CustomerID != c.ID {
		t.Fatalf("Ordner beim Kunden: %+v", folder)
	}

	checks := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/customers", `{"name":"müller ag"}`, 409},
		{"POST", "/api/customers", `{"name":""}`, 400},
		{"PUT", "/api/customers/" + cid, `{"name":"Müller GmbH","notes":null}`, 200},
		{"PUT", "/api/customers/999", `{"name":"x"}`, 404},
		{"POST", "/api/nodes", `{"kind":"tx","tcode":"ZSD084","title":"x","parent_id":` + itoa(folder.ID) + `}`, 201},
		{"POST", "/api/nodes/" + itoa(folder.ID) + "/move", `{"parent_id":null,"customer_id":null}`, 200},
		{"POST", "/api/nodes/" + itoa(folder.ID) + "/move", `{"parent_id":null,"customer_id":` + cid + `}`, 200},
		{"DELETE", "/api/customers/" + cid, `{}`, 409},
		{"DELETE", "/api/customers/" + cid, `{"nodes":"general"}`, 204},
		{"DELETE", "/api/customers/" + cid, `{"nodes":"general"}`, 404},
	}
	for _, ch := range checks {
		if rec := doJSON(t, h, ch.method, ch.path, ch.body); rec.Code != ch.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", ch.method, ch.path, ch.body, rec.Code, ch.want, rec.Body)
		}
	}

	var list []store.Customer
	json.Unmarshal(do(t, h, "GET", "/api/customers", "", "").Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("Kunden nach Löschen: %+v", list)
	}
	var tree []*store.Node
	json.Unmarshal(do(t, h, "GET", "/api/tree", "", "").Body.Bytes(), &tree)
	if countNodes(tree) != 2 || tree[0].CustomerID != nil {
		t.Errorf("Favoriten nicht nach Allgemein verschoben: %+v", tree)
	}
}

func TestSAPExport(t *testing.T) {
	h := newServer(t)
	if rec := upload(t, h, "/api/import", map[string]string{"mode": "replace"}, true); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	var c store.Customer
	json.Unmarshal(doJSON(t, h, "POST", "/api/customers", `{"name":"Müller & Söhne"}`).Body.Bytes(), &c)
	cid := itoa(c.ID)
	doJSON(t, h, "POST", "/api/nodes", `{"kind":"tx","tcode":"ZCUST01","title":"Kunden-Cockpit →","customer_id":`+cid+`}`)

	parse := func(rec *httptest.ResponseRecorder) *sapfile.Result {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		res, err := sapfile.Parse(rec.Body.Bytes(), sapfile.EncodingCP1252)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	rec := do(t, h, "GET", "/api/export/sap", "", "")
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=windows-1252" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="favoriten-allgemein.txt"` {
		t.Errorf("Content-Disposition %q", cd)
	}
	if res := parse(rec); res.Folders != 15 || res.Transactions != 222 || res.Fallbacks != 0 {
		t.Errorf("Allgemein: %d/%d, Fallbacks %d", res.Folders, res.Transactions, res.Fallbacks)
	}

	rec = do(t, h, "GET", "/api/export/sap?customer="+cid, "", "")
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `favoriten-mueller-soehne.txt`) {
		t.Errorf("Dateiname %q", cd)
	}
	if res := parse(rec); res.Folders != 0 || res.Transactions != 1 {
		t.Errorf("nur Kunde: %d/%d", res.Folders, res.Transactions)
	}

	res := parse(do(t, h, "GET", "/api/export/sap?customer="+cid+"&with_general=1", "", ""))
	tree := res.Tree()
	if res.Folders != 15 || res.Transactions != 223 {
		t.Errorf("Kunde + Allgemein: %d/%d", res.Folders, res.Transactions)
	}
	if last := tree[len(tree)-1]; last.TCode != "ZCUST01" || last.ParentID != sapfile.RootID {
		t.Errorf("Kundeneintrag nicht flach am Ende: %+v", last.Node)
	}

	var preview struct {
		Transactions int
		Filename     string
		Warnings     []sapfile.Warning
	}
	json.Unmarshal(do(t, h, "GET", "/api/export/sap/preview?customer="+cid+"&with_general=1", "", "").Body.Bytes(), &preview)
	if preview.Transactions != 223 || len(preview.Warnings) != 1 || preview.Warnings[0].TCode != "ZCUST01" {
		t.Errorf("preview: %+v", preview)
	}

	if rec := do(t, h, "GET", "/api/export/sap?customer=999", "", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unbekannter Kunde: %d", rec.Code)
	}
	if rec := upload(t, h, "/api/import/preview", map[string]string{"customer": "999"}, true); rec.Code != http.StatusBadRequest {
		t.Errorf("Import in unbekannten Kunden: %d", rec.Code)
	}
}
