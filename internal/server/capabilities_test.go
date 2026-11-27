package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// What Pocket Search, Pocket Day and Pocket Hub ask (capabilities.go). The
// cast is the harness's: Olga owns "Contract" with Ben approving, Rita
// reviewing and Oscar observing; Adam is on none of it.

// ask calls a route as who (a harness name or a pcx_ token) and reads its JSON answer into out.
func (h *harness) ask(who, path string, out any) int {
	h.t.Helper()
	tok := who
	if t, ok := h.tok[who]; ok {
		tok = t
	}
	code, body := h.moveRaw(tok, "GET", path, "", nil)
	if out != nil && code == 200 {
		reflect.ValueOf(out).Elem().SetZero() // nothing of an earlier answer stays
		if err := json.Unmarshal(body, out); err != nil {
			h.t.Fatalf("GET %s: %d %s", path, code, body)
		}
	}
	return code
}

// review makes a review as who and returns its id.
func (h *harness) review(who string, body map[string]any) int64 {
	h.t.Helper()
	code, rv := h.do(who, "POST", "/reviews", body)
	must(h.t, code, 201, "create review")
	return int64(rv["id"].(float64))
}

func searchTitles(items []core.SearchItem) string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Title)
	}
	return strings.Join(out, " | ")
}

func TestPocketSearch(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	budget := h.review("olga", map[string]any{"title": "Budget", "description": "The contract appendix"})
	h.review("adam", map[string]any{"title": "Adam's contract notes"})
	today := shortDate(time.Now(), time.Now())

	var got core.SearchResults
	if code := h.ask("olga", "/api/v1/pocket/search?q=contract", &got); code != 200 || len(got.Items) != 2 {
		t.Fatalf("search: %d %+v", code, got)
	}
	// a title with the words first, then a description; the snippet says where it stands
	if it := got.Items[0]; it.ID != fmt.Sprint(rid) || it.Title != "Contract" || it.Kind != "review" || it.URL != fmt.Sprintf("/reviews/%d", rid) ||
		it.Snippet != "Waiting for 1 reviewer and 1 approver · updated "+today || it.Score != 2 {
		t.Errorf("contract: %+v", it)
	}
	if it := got.Items[1]; it.ID != fmt.Sprint(budget) || it.Snippet != "Draft · updated "+today || it.Score != 1 {
		t.Errorf("found by its description: %+v", it)
	}
	if _, err := time.Parse(time.RFC3339, got.Items[0].Date); err != nil {
		t.Errorf("date %q", got.Items[0].Date)
	}
	if h.ask("olga", "/api/v1/pocket/search?q=contract&limit=1", &got); len(got.Items) != 1 {
		t.Errorf("limit=1: %d items", len(got.Items))
	}
	// no words: nothing found (never every review)
	for _, q := range []string{"", "%3F%3F", "%25", "_", "-"} {
		got = core.SearchResults{}
		if code := h.ask("olga", "/api/v1/pocket/search?q="+q, &got); code != 200 || got.Items == nil || len(got.Items) != 0 {
			t.Errorf("q=%q: %d %+v", q, code, got)
		}
	}

	// a review shared with someone is theirs to find; one they are not on never is
	if h.ask("rita", "/api/v1/pocket/search?q=contract", &got); searchTitles(got.Items) != "Contract" {
		t.Errorf("rita finds: %s", searchTitles(got.Items))
	}
	if h.ask("adam", "/api/v1/pocket/search?q=contract", &got); searchTitles(got.Items) != "Adam's contract notes" {
		t.Errorf("adam finds: %s", searchTitles(got.Items))
	}

	// once Rita is done only the approver is left; once he approves, it is approved
	code, _ := h.do("rita", "POST", fmt.Sprintf("/reviews/%d/verdict", rid), map[string]any{"verdict": "approved"})
	must(t, code, 200, "rita's verdict")
	if h.ask("oscar", "/api/v1/pocket/search?q=contract", &got); len(got.Items) != 1 || !strings.HasPrefix(got.Items[0].Snippet, "Waiting for 1 approver · ") {
		t.Errorf("after rita: %+v", got.Items)
	}
	code, _ = h.do("ben", "POST", fmt.Sprintf("/reviews/%d/verdict", rid), map[string]any{"verdict": "approved"})
	must(t, code, 200, "ben approves")
	if h.ask("oscar", "/api/v1/pocket/search?q=contract", &got); len(got.Items) != 1 || !strings.HasPrefix(got.Items[0].Snippet, "Approved · ") {
		t.Errorf("approved: %+v", got.Items)
	}
	if code := h.ask("", "/api/v1/pocket/search?q=contract", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

func dayLines(d core.Day) string {
	out := []string{}
	for _, it := range d.Items {
		out = append(out, it.Title+" "+it.Kind)
	}
	return strings.Join(out, " | ")
}

func TestPocketDay(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	p := func(s string) string { return fmt.Sprintf("/reviews/%d%s", rid, s) }
	for _, body := range []string{"Clause 4 is unclear", "And so is 5"} {
		code, _ := h.do("rita", "POST", p("/comments"), map[string]any{"body": body, "page": 1})
		must(t, code, 201, "rita comments")
	}
	draft := h.review("olga", map[string]any{"title": "Budget"})
	// Honolulu is ten hours behind UTC: 09:30 UTC on the 21st is 23:30 there on the 20th
	if _, err := h.st.DB.Exec(`UPDATE events SET created_at = '2026-11-21T09:30:00.5Z' WHERE review_id = ?`, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB.Exec(`UPDATE events SET created_at = '2026-11-23T08:00:00Z' WHERE review_id = ?`, draft); err != nil {
		t.Fatal(err)
	}

	var d core.Day
	if code := h.ask("olga", "/api/v1/pocket/day/2026-11-20?tz=Pacific/Honolulu", &d); code != 200 || dayLines(d) != "Contract created" {
		t.Fatalf("olga's day: %d %+v", code, d)
	}
	if it := d.Items[0]; it.ID != fmt.Sprint(rid) || it.Time != "23:30" || it.Sub != "In review" || it.URL != fmt.Sprintf("/reviews/%d", rid) || d.Summary != "1 review created" {
		t.Errorf("olga's line: %+v (%s)", it, d.Summary)
	}
	if h.ask("olga", "/api/v1/pocket/day/2026-11-21?tz=Pacific/Honolulu", &d); len(d.Items) != 0 || d.Summary != "" {
		t.Errorf("the next day in Honolulu: %+v", d)
	}
	if h.ask("olga", "/api/v1/pocket/day/2026-11-21?tz=UTC", &d); dayLines(d) != "Contract created" || d.Items[0].Time != "09:30" {
		t.Errorf("in UTC: %+v", d)
	}
	if h.ask("rita", "/api/v1/pocket/day/2026-11-21?tz=UTC", &d); dayLines(d) != "Contract commented" || d.Items[0].Sub != "In review · 2 comments" ||
		d.Summary != "1 review commented on" || len(d.Metrics) != 1 || d.Metrics[0] != (core.Metric{Label: "Comments", Value: "2"}) {
		t.Errorf("rita's day: %+v", d)
	}
	// opening it is not something done; a review one is not on never shows
	for _, who := range []string{"oscar", "adam"} {
		if h.ask(who, "/api/v1/pocket/day/2026-11-21?tz=UTC", &d); len(d.Items) != 0 || d.Summary != "" {
			t.Errorf("%s's day: %+v", who, d)
		}
	}
	if h.ask("olga", "/api/v1/pocket/day/2026-11-23?tz=UTC", &d); dayLines(d) != "Budget created" || d.Items[0].Sub != "Draft" {
		t.Errorf("the draft's day: %+v", d)
	}

	// Rita finishes (reviewed), Ben approves (the decision): the owner's day shows it approved
	code, _ := h.do("rita", "POST", p("/verdict"), map[string]any{"verdict": "changes"})
	must(t, code, 200, "rita's verdict")
	code, _ = h.do("ben", "POST", p("/verdict"), map[string]any{"verdict": "approved"})
	must(t, code, 200, "ben approves")
	now := time.Now().Format("2006-01-02")
	for who, want := range map[string]string{"rita": "Contract reviewed", "ben": "Contract approved", "olga": "Contract approved", "oscar": ""} {
		if h.ask(who, "/api/v1/pocket/day/"+now, &d); dayLines(d) != want {
			t.Errorf("%s today: %q, want %q", who, dayLines(d), want)
		}
	}
	if h.ask("olga", "/api/v1/pocket/day/"+now, &d); !d.Items[0].Done || d.Items[0].Sub != "Approved" || d.Summary != "1 review approved" || d.Metrics[0].Label != "Approved" {
		t.Errorf("olga's approved day: %+v", d)
	}
	if code := h.ask("olga", "/api/v1/pocket/day/2026-13-01", nil); code != 400 {
		t.Errorf("not a day: %d", code)
	}
}

func TestPocketWidget(t *testing.T) {
	h := newHarness(t)
	rid := h.setup()
	if _, err := h.st.DB.Exec(`UPDATE reviews SET due_date = ? WHERE id = ?`, time.Now().Format("2006-01-02"), rid); err != nil {
		t.Fatal(err)
	}
	var w core.WidgetData
	if code := h.ask("rita", "/api/v1/pocket/widget", &w); code != 200 || w.Line != "1 review waits for you" || len(w.Items) != 1 ||
		w.Items[0] != (core.WidgetItem{ID: fmt.Sprint(rid), Title: "Contract", Detail: "Your review · due today", URL: fmt.Sprintf("/reviews/%d", rid)}) || w.URL != "/home" {
		t.Fatalf("rita: %d %+v", code, w)
	}
	if h.ask("ben", "/api/v1/pocket/widget", &w); w.Line != "1 review waits for you" || w.Items[0].Detail != "Your decision · due today" {
		t.Errorf("ben: %+v", w)
	}
	if h.ask("olga", "/api/v1/pocket/widget", &w); w.Line != "1 of your reviews waits for others" || w.Items[0].Detail != "Waiting for 1 reviewer and 1 approver · due today" {
		t.Errorf("olga: %+v", w)
	}
	// nothing waits for an observer, nor for someone on no review
	for _, who := range []string{"oscar", "adam"} {
		w = core.WidgetData{}
		if h.ask(who, "/api/v1/pocket/widget", &w); w.Line != "" || len(w.Items) != 0 || len(w.Metrics) != 0 || w.Items == nil {
			t.Errorf("%s: %+v", who, w)
		}
	}

	// Olga's draft, and Adam's review she must look at: hers to look at first, then her draft, then hers waiting for others
	h.review("olga", map[string]any{"title": "Budget"})
	adams := h.review("adam", map[string]any{"title": "Floor plan", "people": []map[string]any{{"user_id": h.ids["olga"], "role": "reviewer"}}})
	doc, _ := demoContract(1)
	must(t, h.upload("adam", adams, doc.pdf), 201, "upload")
	code, _ := h.do("adam", "POST", fmt.Sprintf("/reviews/%d/status", adams), map[string]any{"action": "start"})
	must(t, code, 200, "start")
	h.ask("olga", "/api/v1/pocket/widget", &w)
	got := []string{}
	for _, it := range w.Items {
		got = append(got, it.Title+": "+it.Detail)
	}
	if strings.Join(got, " | ") != "Floor plan: Your review | Budget: Draft, start it | Contract: Waiting for 1 reviewer and 1 approver · due today" ||
		w.Line != "2 reviews wait for you, 1 of yours waits for others" {
		t.Errorf("olga's widget: %s (%s)", strings.Join(got, " | "), w.Line)
	}
	want := []core.Metric{{Label: "To review", Value: "1"}, {Label: "To decide", Value: "0"}, {Label: "Waiting for others", Value: "1"}, {Label: "Open tasks", Value: "0"}}
	if fmt.Sprint(w.Metrics) != fmt.Sprint(want) {
		t.Errorf("metrics: %+v", w.Metrics)
	}
	// reading the widget changes nothing: Rita has still not opened the review
	var opened int
	_ = h.st.DB.QueryRow(`SELECT COUNT(1) FROM participants WHERE first_opened_at IS NOT NULL`).Scan(&opened)
	if opened != 0 {
		t.Errorf("%d participants marked as having opened a review", opened)
	}
}

func TestPocketLinkTokens(t *testing.T) {
	h := moveHarness(t)
	rid := h.setup()
	h.review("adam", map[string]any{"title": "Adam's contract notes"})
	now := time.Now().Format("2006-01-02")
	routes := []struct{ scope, path string }{
		{"search", "/api/v1/pocket/search?q=contract"},
		{"day", "/api/v1/pocket/day/" + now + "?tz=Europe/Sarajevo"},
		{"widget", "/api/v1/pocket/widget"},
	}
	for _, scope := range []string{"search", "day", "widget", "capture", "read", "write", "export"} {
		tok := "pcx_olga_" + scope
		for _, rt := range routes {
			want := 403 // the app's own scopes do not open the new routes either
			if rt.scope == scope {
				want = 200
			}
			code, body := h.moveRaw(tok, "GET", rt.path, "", nil)
			if code != want {
				t.Errorf("%s GET %s: %d, want %d (%s)", tok, rt.path, code, want, body)
			}
			if strings.Contains(string(body), "Adam") {
				t.Errorf("%s %s: another person's review: %s", tok, rt.path, body)
			}
		}
		if code, _ := h.moveRaw(tok, "GET", "/api/v1/auth/me", "", nil); code != 200 {
			t.Errorf("%s /auth/me: %d", tok, code)
		}
		if scope == "read" || scope == "write" || scope == "export" {
			continue
		}
		// a capability opens its one route with its one method, nothing else of the app
		for _, rt := range []struct{ method, path string }{
			{"GET", "/api/v1/reviews"}, {"GET", "/api/v1/dashboard"}, {"GET", "/api/v1/reviews/lookup"}, {"GET", fmt.Sprintf("/api/v1/reviews/%d", rid)},
			{"GET", fmt.Sprintf("/api/v1/reviews/%d/comments", rid)}, {"POST", fmt.Sprintf("/api/v1/reviews/%d/comments", rid)},
			{"POST", fmt.Sprintf("/api/v1/reviews/%d/verdict", rid)}, {"GET", "/api/v1/activity"}, {"GET", "/api/v1/notifications"},
			{"GET", "/api/v1/export.zip"}, {"GET", core.PortablePath},
		} {
			if code, body := h.moveRaw(tok, rt.method, rt.path, "application/json", []byte(`{"body":"nope","verdict":"approved"}`)); code != 403 {
				t.Errorf("%s %s %s: %d, want 403 (%s)", tok, rt.method, rt.path, code, body)
			}
		}
	}
	// no capture here, and the other method on a capability's own route is no way in either
	for _, tok := range []string{"pcx_olga_capture", "pcx_olga_search"} {
		for _, rt := range []struct{ method, path string }{{"POST", core.CapturePath}, {"POST", "/api/v1/pocket/search?q=contract"}, {"DELETE", "/api/v1/pocket/widget"}} {
			if code, _ := h.moveRaw(tok, rt.method, rt.path, "application/json", []byte(`{"request_id":"hub-0000000000000001","text":"x"}`)); code != 403 && code != 404 && code != 405 {
				t.Errorf("%s %s %s: %d", tok, rt.method, rt.path, code)
			}
		}
	}
	// the app's own scopes still open what they opened
	if code, _ := h.moveRaw("pcx_olga_read", "GET", "/api/v1/dashboard", "", nil); code != 200 {
		t.Errorf("reviews:read on /dashboard: %d", code)
	}

	// what came back is what Olga may see
	var found core.SearchResults
	_, body := h.moveRaw("pcx_olga_search", "GET", "/api/v1/pocket/search?q=contract", "", nil)
	if _ = json.Unmarshal(body, &found); searchTitles(found.Items) != "Contract" {
		t.Errorf("link search: %s", body)
	}
	var d core.Day
	_, body = h.moveRaw("pcx_olga_day", "GET", "/api/v1/pocket/day/"+now, "", nil)
	if _ = json.Unmarshal(body, &d); dayLines(d) != "Contract created" {
		t.Errorf("link day: %s", body)
	}
	var w core.WidgetData
	_, body = h.moveRaw("pcx_olga_widget", "GET", "/api/v1/pocket/widget", "", nil)
	if _ = json.Unmarshal(body, &w); w.Line != "1 of your reviews waits for others" {
		t.Errorf("link widget: %s", body)
	}
	// nothing was approved, commented or marked read on the way
	var n int
	_ = h.st.DB.QueryRow(`SELECT (SELECT COUNT(1) FROM comments) + (SELECT COUNT(1) FROM participants WHERE verdict <> '') + (SELECT COUNT(1) FROM notifications WHERE read_at IS NOT NULL)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d things changed by reading", n)
	}
}

func TestManifestOffersCapabilities(t *testing.T) {
	h := newHarness(t)
	b, err := json.Marshal(h.srv.manifest())
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(b, &sent)
	if sent["slug"] != "review" || sent["category"] != "Work" {
		t.Errorf("manifest: slug %v, category %v", sent["slug"], sent["category"])
	}
	for _, k := range []string{"search", "day", "widget", "portable"} {
		if sent[k] == nil {
			t.Errorf("manifest has no %q", k)
		}
	}
	if sent["capture"] != nil {
		t.Errorf("manifest offers a capture: %v", sent["capture"])
	}
	bangs, _ := sent["search"].(map[string]any)["bangs"].([]any)
	if len(bangs) != 2 || bangs[0] != "review" || bangs[1] != "reviews" {
		t.Errorf("bangs: %v", bangs)
	}
	if feed, _ := sent["day"].(map[string]any); feed["label"] != "Review" || feed["one"] != "review" || feed["many"] != "reviews" {
		t.Errorf("day feed: %v", sent["day"])
	}
	if wd, _ := sent["widget"].(map[string]any); wd["title"] != "Reviews" {
		t.Errorf("widget: %v", sent["widget"])
	}
	// Register adds one offer per capability, next to the app's own
	offered := map[string]bool{}
	for _, o := range capabilities.Offers() {
		offered[o.Scope] = true
	}
	if !offered[core.ScopeSearch] || !offered[core.ScopeDay] || !offered[core.ScopeWidget] || offered[core.ScopeCapture] {
		t.Errorf("offers: %v", offered)
	}
	for _, o := range linkOffers {
		if strings.HasPrefix(o.Scope, "pocket:") {
			t.Errorf("the app's own offers name %s", o.Scope)
		}
	}
}
