package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-review/internal/config"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// moveHarness is the usual harness behind a fake Core whose
// pcx_<who>_<export|import|read> tokens are Pocket Move links acting for
// <who>@example.com (a member to Core); pcx_<who>_<search|day|widget|capture|write>
// carry that one scope (capabilities_test.go).
func moveHarness(t *testing.T) *harness {
	t.Helper()
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			io.WriteString(w, `{}`)
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		parts := strings.SplitN(strings.TrimPrefix(in.Token, "pcx_"), "_", 2)
		if len(parts) != 2 {
			io.WriteString(w, `{"active":false}`)
			return
		}
		scope := map[string]string{"export": core.ScopeMoveExport, "import": core.ScopeMoveImport, "read": "reviews:read", "write": "reviews:write",
			"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture}[parts[1]]
		email := parts[0] + "@example.com"
		fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"review","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":7,"slug":"move","name":"Pocket Move"}}`, scope, email, email, parts[0])
	}))
	t.Cleanup(coreSrv.Close)
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", FilesDir: dir + "/files", AuthMode: "local", MaxUploadMB: 10, SessionTTL: time.Hour,
		IdleAfter: 30 * time.Second, SessionGap: 10 * time.Minute, TickInterval: 5 * time.Second, CoreURL: coreSrv.URL}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, tok: map[string]string{}, ids: map[string]int64{}}
	for _, n := range []string{"adam", "olga", "ben", "rita", "oscar"} {
		role := "member"
		if n == "adam" {
			role = "admin"
		}
		u := &store.User{Email: n + "@example.com", Name: n, Role: role}
		if err := st.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		h.ids[n] = u.ID
		h.tok[n], _, _ = st.CreateAPIToken(context.Background(), u.ID, n)
	}
	h.srv = New(cfg, st, log, nil)
	h.http = httptest.NewServer(h.srv.Router())
	t.Cleanup(h.http.Close)
	return h
}

func (h *harness) moveRaw(tok, method, path, ctype string, body []byte) (int, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.http.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (h *harness) moveImport(tok string, data []byte) map[string]any {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "review.zip")
	_, _ = fw.Write(data)
	_ = mw.Close()
	code, out := h.moveRaw(tok, "POST", "/api/v1/import", mw.FormDataContentType(), buf.Bytes())
	if code != 200 {
		h.t.Fatalf("import → %d %s", code, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m
}

func zipDoc(t *testing.T, b []byte) (moveDoc, map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v (%.200s)", err, b)
	}
	files := map[string][]byte{}
	var doc moveDoc
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = data
		if f.Name == moveZipJSON {
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
		}
	}
	return doc, files
}

func TestMoveLinkScopes(t *testing.T) {
	h := moveHarness(t)
	exp, imp, read := "pcx_rita_export", "pcx_rita_import", "pcx_rita_read"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{exp, "GET", core.PortablePath, 200},
		{exp, "GET", "/api/v1/export.zip", 200},
		{exp, "POST", "/api/v1/import", 403},
		{exp, "GET", "/api/v1/reviews", 403},
		{exp, "GET", "/api/v1/settings", 403},
		{exp, "GET", "/api/v1/notifications", 403},
		{imp, "GET", core.PortablePath, 200},
		{imp, "GET", "/api/v1/export.zip", 403},
		{imp, "POST", "/api/v1/reviews", 403},
		{imp, "POST", "/api/v1/reviews/import", 403},
		{imp, "PUT", "/api/v1/settings", 403},
		{read, "GET", core.PortablePath, 403},
		{read, "GET", "/api/v1/export.zip", 403},
	} {
		if code, body := h.moveRaw(c.tok, c.method, c.path, "application/json", []byte("{}")); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%s)", c.tok, c.method, c.path, code, c.want, body)
		}
	}
	_, body := h.moveRaw(exp, "GET", core.PortablePath, "", nil)
	var p core.Portable
	if err := json.Unmarshal(body, &p); err != nil || len(p.Exports) != 1 || len(p.Imports) != 1 || p.Exports[0].Kind != core.KindPocket {
		t.Fatalf("portable: %s", body)
	}
}

// Olga takes her review to Adam's account: the document, the comments (with
// their writers) and the pictures come along once; nobody else's reviews do.
func TestMoveReviewsOutAndIn(t *testing.T) {
	h := moveHarness(t)
	rid := h.setup()
	p := func(s string) string { return fmt.Sprintf("/reviews/%d%s", rid, s) }
	code, issue := h.do("rita", "POST", p("/comments"), map[string]any{"kind": "issue", "severity": "major", "body": "Liability is uncapped", "page": 2,
		"anchor": map[string]any{"rects": []map[string]float64{{"x": 0.1, "y": 0.2, "w": 0.3, "h": 0.05}}, "quote": "liability"}})
	must(t, code, 201, "issue")
	code, _ = h.do("olga", "POST", p("/comments"), map[string]any{"body": "Will fix", "parent_id": issue["id"]})
	must(t, code, 201, "reply")

	code, out := h.moveRaw("pcx_olga_export", "GET", "/api/v1/export.zip", "", nil)
	if code != 200 {
		t.Fatalf("export: %d %s", code, out)
	}
	doc, files := zipDoc(t, out)
	if len(doc.Reviews) != 1 || doc.Reviews[0].Title != "Contract" || len(doc.Reviews[0].Comments) != 2 || len(doc.Reviews[0].Revisions) != 1 {
		t.Fatalf("olga's export: %+v", doc)
	}
	if pdf := files[doc.Reviews[0].Revisions[0].File]; !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("the document is not in the zip: %v", doc.Reviews[0].Revisions[0])
	}
	// Rita is on the review but did not start it: nothing of it is hers to take
	if _, rita := h.moveRaw("pcx_rita_export", "GET", "/api/v1/export.zip", "", nil); len(func() []moveReview { d, _ := zipDoc(t, rita); return d.Reviews }()) != 0 {
		t.Fatal("rita's export holds olga's review")
	}

	first := h.moveImport("pcx_adam_import", out)
	if first["reviews"] != 1.0 || first["revisions"] != 1.0 || first["comments"] != 2.0 {
		t.Fatalf("first import: %v", first)
	}
	second := h.moveImport("pcx_adam_import", out)
	if second["reviews"] != 0.0 || second["comments"] != 0.0 || second["already"] != 1.0 {
		t.Fatalf("second import: %v", second)
	}
	ctx := context.Background()
	mine, _ := h.st.ListReviews(ctx, store.ReviewFilter{UserID: h.ids["adam"]})
	if len(mine) != 1 || mine[0].OwnerID != h.ids["adam"] || mine[0].Status != store.StatusInReview || len(mine[0].Participants) != 1 {
		t.Fatalf("adam's reviews: %+v", mine)
	}
	threads, _ := h.st.ListThreads(ctx, mine[0].ID)
	if len(threads) != 1 || threads[0].AuthorID != h.ids["rita"] || threads[0].Anchor == nil || len(threads[0].Replies) != 1 || threads[0].Replies[0].AuthorID != h.ids["olga"] {
		t.Fatalf("imported threads: %+v", threads)
	}
	code, _ = h.do("adam", "GET", fmt.Sprintf("/reviews/%d/revisions/%d/file", mine[0].ID, mine[0].CurrentRevisionID), nil)
	must(t, code, 200, "the imported document opens")
	olgas, _ := h.st.ListReviews(ctx, store.ReviewFilter{UserID: h.ids["olga"]})
	if len(olgas) != 1 {
		t.Fatalf("olga has %d reviews", len(olgas))
	}
}
