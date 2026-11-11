package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-review/internal/config"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// harness: Adam is the box admin and on no review; Olga owns; Ben approves;
// Rita reviews; Oscar observes.
type harness struct {
	t    *testing.T
	srv  *Server
	http *httptest.Server
	st   *store.Store
	tok  map[string]string
	ids  map[string]int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", FilesDir: dir + "/files", AuthMode: "local", MaxUploadMB: 10, SessionTTL: time.Hour,
		IdleAfter: 30 * time.Second, SessionGap: 10 * time.Minute, TickInterval: 5 * time.Second}
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

func (h *harness) do(who, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.http.URL+"/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+h.tok[who])
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func (h *harness) upload(who string, rid int64, pdf []byte) int {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "doc.pdf")
	fw.Write(pdf)
	mw.WriteField("page_count", "5")
	mw.Close()
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/reviews/%d/revisions", h.http.URL, rid), &buf)
	req.Header.Set("Authorization", "Bearer "+h.tok[who])
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func must(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: HTTP %d, want %d", what, got, want)
	}
}

// setup: Olga's review with a PDF, Ben approver, Rita reviewer, Oscar observer, started.
func (h *harness) setup() int64 {
	code, rv := h.do("olga", "POST", "/reviews", map[string]any{"title": "Contract", "people": []map[string]any{
		{"user_id": h.ids["ben"], "role": "approver"}, {"user_id": h.ids["rita"], "role": "reviewer"}, {"email": "oscar@example.com", "role": "observer"}}})
	must(h.t, code, 201, "create review")
	rid := int64(rv["id"].(float64))
	doc, _ := demoContract(1)
	must(h.t, h.upload("olga", rid, doc.pdf), 201, "upload")
	code, _ = h.do("olga", "POST", fmt.Sprintf("/reviews/%d/status", rid), map[string]any{"action": "start"})
	must(h.t, code, 200, "start")
	return rid
}

func TestReviewsAreStrictlyPerPerson(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	p := func(s string) string { return fmt.Sprintf("/reviews/%d%s", rid, s) }
	// the admin is on no review: everything about it is 404, including the file
	for _, path := range []string{"", "/comments", "/tasks", "/events", "/report", "/report.pdf", "/revisions/1/file", "/events.csv", "/presence"} {
		code, _ := h.do("adam", "GET", p(path), nil)
		must(t, code, 404, "admin GET "+path)
	}
	code, _ := h.do("adam", "POST", p("/comments"), map[string]any{"body": "hi", "page": 1})
	must(t, code, 404, "admin comment")
	code, _ = h.do("adam", "DELETE", p(""), nil)
	must(t, code, 404, "admin delete")
	code, list := h.doList("adam", "/reviews")
	must(t, code, 200, "admin list")
	if len(list) != 0 {
		t.Fatalf("admin sees %d reviews, want 0", len(list))
	}
	// everyone on it sees it
	for _, who := range []string{"olga", "ben", "rita", "oscar"} {
		code, _ := h.do(who, "GET", p(""), nil)
		must(t, code, 200, who+" GET review")
	}
	// taken off → 404 again
	code, _ = h.do("olga", "DELETE", p(fmt.Sprintf("/participants/%d", h.ids["rita"])), nil)
	must(t, code, 204, "remove rita")
	code, _ = h.do("rita", "GET", p(""), nil)
	must(t, code, 404, "rita after removal")
}

func (h *harness) doList(who, path string) (int, []any) {
	req, _ := http.NewRequest("GET", h.http.URL+"/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+h.tok[who])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var out []any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestRolesAndApproval(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	p := func(s string) string { return fmt.Sprintf("/reviews/%d%s", rid, s) }

	code, _ := h.do("oscar", "POST", p("/comments"), map[string]any{"body": "looks fine", "page": 1})
	must(t, code, 403, "observer comments")
	code, _ = h.do("rita", "POST", p("/status"), map[string]any{"action": "cancel"})
	must(t, code, 403, "reviewer cancels")

	code, issue := h.do("rita", "POST", p("/comments"), map[string]any{"kind": "issue", "severity": "critical", "body": "Liability is uncapped", "page": 2,
		"anchor": map[string]any{"rects": []map[string]float64{{"x": 0.1, "y": 0.2, "w": 2, "h": 0.05}}, "quote": "liability"}})
	must(t, code, 201, "reviewer raises issue")
	iid := int(issue["id"].(float64))
	if a := issue["anchor"].(map[string]any)["rects"].([]any)[0].(map[string]any); a["w"].(float64) > 0.9+1e-9 {
		t.Fatalf("anchor not clamped to the page: %v", a)
	}
	code, olgaNote := h.do("olga", "POST", p("/comments"), map[string]any{"body": "Note to self", "page": 1})
	must(t, code, 201, "owner comments")
	code, _ = h.do("rita", "POST", p(fmt.Sprintf("/comments/%d/status", int(olgaNote["id"].(float64)))), map[string]any{"status": "resolved"})
	must(t, code, 403, "reviewer resolves someone else's thread")
	code, _ = h.do("rita", "POST", p(fmt.Sprintf("/comments/%d/status", iid)), map[string]any{"status": "rejected"})
	must(t, code, 400, "reject without a reason")

	// the approver cannot approve while the critical issue is open
	code, _ = h.do("ben", "POST", p("/verdict"), map[string]any{"verdict": "approved"})
	must(t, code, 409, "approve with open issue")
	code, _ = h.do("ben", "POST", p(fmt.Sprintf("/comments/%d/status", iid)), map[string]any{"status": "resolved", "note": "capped in 8.2"})
	must(t, code, 200, "approver resolves")
	code, _ = h.do("rita", "POST", p("/verdict"), map[string]any{"verdict": "approved"})
	must(t, code, 200, "reviewer finishes")
	code, rv := h.do("ben", "POST", p("/verdict"), map[string]any{"verdict": "approved", "note": "ok"})
	must(t, code, 200, "approver approves")
	if rv["status"] != "approved" {
		t.Fatalf("status %v, want approved", rv["status"])
	}
	// a decided review still takes comments and replies; tasks wait for a reopen
	code, late := h.do("rita", "POST", p("/comments"), map[string]any{"body": "late", "page": 1})
	must(t, code, 201, "comment on approved review")
	code, _ = h.do("ben", "POST", p("/comments"), map[string]any{"body": "noted", "parent_id": late["id"]})
	must(t, code, 201, "reply on approved review")
	code, _ = h.do("oscar", "POST", p("/comments"), map[string]any{"body": "me too", "page": 1})
	must(t, code, 403, "observer comments on approved review")
	code, _ = h.do("rita", "POST", p("/tasks"), map[string]any{"title": "late task"})
	must(t, code, 409, "task on approved review")
	if _, rv = h.do("olga", "GET", p(""), nil); rv["status"] != "approved" {
		t.Fatalf("a late comment moved the review to %v", rv["status"])
	}
	code, _ = h.do("olga", "POST", p("/status"), map[string]any{"action": "reopen"})
	must(t, code, 200, "reopen")
	// a cancelled review is read-only
	code, _ = h.do("olga", "POST", p("/status"), map[string]any{"action": "cancel"})
	must(t, code, 200, "cancel")
	code, _ = h.do("rita", "POST", p("/comments"), map[string]any{"body": "after cancel", "page": 1})
	must(t, code, 409, "comment on cancelled review")
	code, _ = h.do("olga", "POST", p("/status"), map[string]any{"action": "reopen"})
	must(t, code, 200, "reopen after cancel")

	// everything above is in the log
	code, _ = h.do("olga", "GET", p("/events"), nil)
	must(t, code, 200, "events")
	evs, err := h.st.ListEvents(context.Background(), store.EventFilter{ReviewID: rid, UserID: h.ids["olga"], Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
	}
	for _, k := range []string{"review.created", "participant.added", "revision.uploaded", "review.started", "issue.raised", "comment.resolved", "verdict.approved", "review.approved", "review.reopened"} {
		if !kinds[k] {
			t.Errorf("log is missing %s", k)
		}
	}
}

func TestChangesRequestedThenNewRevision(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	p := func(s string) string { return fmt.Sprintf("/reviews/%d%s", rid, s) }
	code, rv := h.do("ben", "POST", p("/verdict"), map[string]any{"verdict": "changes", "note": "fix 8.2"})
	must(t, code, 200, "request changes")
	if rv["status"] != "changes_requested" {
		t.Fatalf("status %v", rv["status"])
	}
	doc, _ := demoContract(2)
	must(t, h.upload("olga", rid, doc.pdf), 201, "upload v2")
	code, rv = h.do("ben", "GET", p(""), nil)
	must(t, code, 200, "get")
	if rv["status"] != "in_review" || len(rv["revisions"].([]any)) != 2 {
		t.Fatalf("after v2: status %v, %d revisions", rv["status"], len(rv["revisions"].([]any)))
	}
	for _, x := range rv["participants"].([]any) {
		if pp := x.(map[string]any); pp["verdict"] != "" {
			t.Fatalf("verdict not cleared for %v", pp["name"])
		}
	}
	must(t, h.upload("rita", rid, doc.pdf), 403, "reviewer uploads")
}

// The clock: two visible tabs of one person count once; an idle or hidden
// tab counts nothing; time goes to the page in view.
func TestReviewClock(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	ctx := context.Background()
	rv, _ := h.st.GetReview(ctx, rid, h.ids["olga"])
	rita, _ := h.st.GetUser(ctx, h.ids["rita"])
	ben, _ := h.st.GetUser(ctx, h.ids["ben"])
	now := time.Now()
	mk := func(u *store.User, page int, visible bool, lastInput time.Time) *liveConn {
		c := &liveConn{user: u, reviewID: rid, revisionID: rv.CurrentRevisionID, page: page, visible: visible, lastInput: lastInput, joined: now, send: make(chan []byte, 64), close: func() {}}
		h.srv.hub.join(c)
		return c
	}
	mk(rita, 2, true, now)
	mk(rita, 2, true, now.Add(-time.Second)) // second tab
	mk(ben, 3, true, now.Add(-time.Hour))    // idle
	mk(ben, 4, false, now)                   // hidden
	for i := 1; i <= 3; i++ {
		h.srv.hub.tick(ctx, now.Add(time.Duration(i)*5*time.Second))
	}
	pts, _ := h.st.ListPageTime(ctx, rid)
	got := map[string]int{}
	for _, pt := range pts {
		got[fmt.Sprintf("%d/%d", pt.UserID, pt.Page)] = pt.Seconds
	}
	if s := got[fmt.Sprintf("%d/2", rita.ID)]; s != 15 {
		t.Fatalf("rita on page 2: %d s, want 15 (two tabs count once)", s)
	}
	for k, s := range got {
		if k != fmt.Sprintf("%d/2", rita.ID) {
			t.Fatalf("unexpected time %s = %d (idle/hidden tabs must not count)", k, s)
		}
	}
	sessions, _ := h.st.ListSessions(ctx, rid)
	if len(sessions) != 1 || sessions[0].ActiveSeconds != 15 {
		t.Fatalf("sessions %+v, want one of 15 s", sessions)
	}
	// a pause longer than SessionGap starts a new session
	h.srv.hub.tick(ctx, now.Add(20*time.Minute)) // rita now idle too → nothing
	for c := range h.srv.hub.rooms[rid].conns {
		if c.user.ID == rita.ID {
			c.lastInput = now.Add(30 * time.Minute)
		}
	}
	h.srv.hub.tick(ctx, now.Add(30*time.Minute))
	sessions, _ = h.st.ListSessions(ctx, rid)
	if len(sessions) != 2 {
		t.Fatalf("%d sessions after a long pause, want 2", len(sessions))
	}
}
