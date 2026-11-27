package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// What Pocket Search, Pocket Day and Pocket Hub ask Review (the capabilities
// value in links.go): one route each, in the shape every Pocket app answers
// in. They read what the Reviews list, the review log and Home read, as the
// request's person and only the reviews that person is on (a review shared
// with them counts, exactly as GET /reviews lists it). Nothing here approves,
// comments, marks anything read or tells anyone anything.

// reviewPath opens a review in the web app.
func reviewPath(id int64) string { return "/reviews/" + strconv.FormatInt(id, 10) }

// dots joins the parts of a line that are there: "Waiting for 2 reviewers · updated 3 Nov".
func dots(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// countOf says a number with its word: "1 reviewer", "3 reviewers".
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// shortDate is "3 Nov", or "3 Nov 2025" in another year.
func shortDate(t, now time.Time) string {
	if t.Year() == now.Year() {
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// dueWords says when a review is due: "due today", "due tomorrow",
// "due 3 Dec", "was due 20 Nov"; nothing without a due date.
func dueWords(due string, now time.Time) string {
	d, err := store.ParseDate(due)
	if err != nil {
		return ""
	}
	today, _ := store.ParseDate(now.Format("2006-01-02"))
	on := shortDate(d, today)
	switch {
	case d.Before(today):
		return "was due " + on
	case d.Equal(today):
		return "due today"
	case d.Equal(today.AddDate(0, 0, 1)):
		return "due tomorrow"
	}
	return "due " + on
}

// statusWords says where a review stands in plain words: "Draft", "Waiting
// for 2 reviewers and 1 approver", "Waiting for changes", "Approved". Without
// the people on it an open review is just "In review".
func statusWords(status string, people []store.Participant) string {
	switch status {
	case store.StatusDraft:
		return "Draft"
	case store.StatusInReview:
		reviewers, approvers := 0, 0
		for _, p := range people {
			switch {
			case p.Role == store.RoleReviewer && p.State != "done":
				reviewers++
			case p.Role == store.RoleApprover && p.Verdict == "":
				approvers++
			}
		}
		var who []string
		if reviewers > 0 {
			who = append(who, countOf(reviewers, "reviewer", "reviewers"))
		}
		if approvers > 0 {
			who = append(who, countOf(approvers, "approver", "approvers"))
		}
		if len(who) == 0 {
			return "In review"
		}
		return "Waiting for " + strings.Join(who, " and ")
	case store.StatusChangesRequested:
		return "Waiting for changes"
	case store.StatusApproved:
		return "Approved"
	case store.StatusRejected:
		return "Rejected"
	case store.StatusCancelled:
		return "Cancelled"
	}
	return ""
}

// ---------- search ----------

// hasWord: a query of only signs (??, *) finds nothing here, though the
// Reviews list's LIKE would match every review with "%" or "_".
func hasWord(q string) bool {
	return strings.IndexFunc(q, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }) >= 0
}

// pocketSearch is the Reviews list's search (GET /reviews?q=: title and
// description) over every review the person is on, open or finished, the
// latest change first; a title with the words ranks above a description.
// Comments are not searched: the app's own search does not look at them.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	if !hasWord(q) {
		return nil, nil
	}
	ctx, now := r.Context(), time.Now()
	list, err := s.store.ListReviews(ctx, store.ReviewFilter{UserID: actorID(ctx), Query: q, Limit: limit})
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(q)
	out := make([]core.SearchItem, 0, len(list))
	for _, rv := range list {
		score := 1.0
		if strings.Contains(strings.ToLower(rv.Title), needle) {
			score = 2
		}
		updated := ""
		if t := store.ParseTime(rv.UpdatedAt); !t.IsZero() {
			updated = "updated " + shortDate(t.In(now.Location()), now)
		}
		out = append(out, core.SearchItem{ID: strconv.FormatInt(rv.ID, 10), Title: rv.Title, Kind: "review", Date: rv.UpdatedAt, URL: reviewPath(rv.ID),
			Snippet: dots(statusWords(rv.Status, rv.Participants), updated), Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// ---------- day ----------

// dayKinds are what a review's line in the day can say, the most telling
// first, with the word the day's summary uses for it.
var dayKinds = []struct{ kind, word string }{
	{"approved", "approved"}, {"rejected", "rejected"}, {"changes", "sent back for changes"}, {"created", "created"},
	{"started", "started"}, {"reviewed", "reviewed"}, {"commented", "commented on"}, {"updated", "updated"},
}

func dayRank(kind string) int {
	for i, k := range dayKinds {
		if k.kind == kind {
			return i
		}
	}
	return len(dayKinds)
}

// dayKindOf says what a line of the log means for the person's day: a
// decision on the review (theirs, or anyone's on a review they own), a
// verdict (an approver's or a deciding owner's is the decision itself), the
// review made or started, a comment, else some other change. Opening the
// document, reading it and downloading the report are not something done.
func dayKindOf(e store.DayEvent, uid int64) string {
	switch e.Kind {
	case "review.approved":
		return "approved"
	case "review.rejected":
		return "rejected"
	case "review.changes_requested":
		return "changes"
	}
	if e.UserID != uid {
		return ""
	}
	switch e.Kind {
	case "verdict.approved", "verdict.rejected", "verdict.changes":
		if e.Role == store.RoleApprover || e.Role == store.RoleOwner {
			return map[string]string{"verdict.approved": "approved", "verdict.rejected": "rejected", "verdict.changes": "changes"}[e.Kind]
		}
		return "reviewed"
	case "review.created", "review.imported":
		return "created"
	case "review.started":
		return "started"
	case "comment.added", "comment.replied":
		return "commented"
	case "review.opened", "session.started", "report.downloaded":
		return ""
	}
	return "updated"
}

// pocketDay says what happened with the person's reviews on one day, from
// the reviews' log: a line per review they created, started, commented on,
// gave a verdict on or otherwise changed, and per review of theirs someone
// decided. The day is the caller's time zone's when it said one, else the
// server's.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx, uid := r.Context(), actorID(r.Context())
	loc := time.Local
	if tz != nil {
		loc = tz
	}
	from, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, nil
	}
	to := from.AddDate(0, 0, 1)
	// stamps are UTC RFC 3339; without a zone letter the bounds sort right
	// against both "…:00Z" and "…:00.123Z"
	events, err := s.store.DayEvents(ctx, uid, from.UTC().Format("2006-01-02T15:04:05"), to.UTC().Format("2006-01-02T15:04:05"))
	if err != nil {
		return nil, err
	}
	type line struct {
		ev       store.DayEvent
		kind, at string
		comments int
	}
	var order []int64
	lines := map[int64]*line{}
	comments, approved := 0, 0
	for _, e := range events {
		kind := dayKindOf(e, uid)
		if kind == "" {
			continue
		}
		l := lines[e.ReviewID]
		if l == nil {
			l = &line{ev: e, kind: kind}
			lines[e.ReviewID] = l
			order = append(order, e.ReviewID)
		}
		if kind == "commented" {
			l.comments++
			comments++
		}
		// the time of the line is when its most telling thing happened (the last time, if more than once)
		if dayRank(kind) <= dayRank(l.kind) {
			l.kind = kind
			l.at = store.ParseTime(e.CreatedAt).In(loc).Format("15:04")
		}
	}
	if len(order) == 0 {
		return nil, nil
	}
	day := &core.Day{Date: date}
	count := map[string]int{}
	for _, id := range order {
		l := lines[id]
		count[l.kind]++
		if l.kind == "approved" {
			approved++
		}
		sub := statusWords(l.ev.Status, nil)
		if l.comments > 0 {
			sub = dots(sub, countOf(l.comments, "comment", "comments"))
		}
		day.Items = append(day.Items, core.DayItem{ID: strconv.FormatInt(id, 10), Time: l.at, Title: l.ev.ReviewTitle, Sub: sub, Kind: l.kind,
			Done: l.kind == "approved", URL: reviewPath(id)})
	}

	// "1 review approved, 2 commented on, 1 updated"
	var parts []string
	for _, k := range dayKinds {
		n := count[k.kind]
		switch {
		case n == 0:
		case len(parts) == 0:
			parts = append(parts, countOf(n, "review", "reviews")+" "+k.word)
		default:
			parts = append(parts, strconv.Itoa(n)+" "+k.word)
		}
	}
	day.Summary = strings.Join(parts, ", ")
	if approved > 0 {
		day.Metrics = append(day.Metrics, core.Metric{Label: "Approved", Value: strconv.Itoa(approved)})
	}
	if comments > 0 {
		day.Metrics = append(day.Metrics, core.Metric{Label: "Comments", Value: strconv.Itoa(comments)})
	}
	return day, nil
}

// ---------- widget ----------

// whyWords are Home's words for why a review waits for the person.
var whyWords = map[string]string{"review": "Your review", "approve": "Your decision", "changes": "Changes requested", "draft": "Draft, start it"}

// pocketWidget answers Pocket Hub with what Home shows under Waiting for you
// (the reviews to look at or decide first, then the owner's reviews sent
// back for changes and drafts), followed by the person's own reviews that
// wait for others, with Home's numbers.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx, uid, now := r.Context(), actorID(r.Context()), time.Now()
	list, err := s.store.ListReviews(ctx, store.ReviewFilter{UserID: uid, Status: "open"})
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.MyTasks(ctx, uid, true)
	if err != nil {
		return nil, err
	}
	type row struct {
		rv  *store.Review
		why string
	}
	var forYou, onOthers []row
	toReview, toDecide := 0, 0
	for _, rv := range list {
		why := waitingWhy(rv, uid)
		switch {
		case why == "review":
			toReview++
		case why == "approve":
			toDecide++
		case why == "" && rv.MyRole == store.RoleOwner && rv.Status == store.StatusInReview:
			onOthers = append(onOthers, row{rv, ""})
			continue
		}
		if why != "" {
			forYou = append(forYou, row{rv, why})
		}
	}
	if len(forYou)+len(onOthers)+len(tasks) == 0 {
		return nil, nil
	}
	// what to look at or decide first, then the owner's own to-dos; the latest change first within each
	sort.SliceStable(forYou, func(i, j int) bool {
		first := func(w string) bool { return w == "review" || w == "approve" }
		return first(forYou[i].why) && !first(forYou[j].why)
	})

	w := &core.WidgetData{URL: "/home", Metrics: []core.Metric{
		{Label: "To review", Value: strconv.Itoa(toReview)},
		{Label: "To decide", Value: strconv.Itoa(toDecide)},
		{Label: "Waiting for others", Value: strconv.Itoa(len(onOthers))},
		{Label: "Open tasks", Value: strconv.Itoa(len(tasks))},
	}}
	switch n, m := len(forYou), len(onOthers); {
	case n > 0 && m > 0:
		w.Line = countOf(n, "review waits for you", "reviews wait for you") + ", " + countOf(m, "of yours waits for others", "of yours wait for others")
	case n > 0:
		w.Line = countOf(n, "review waits for you", "reviews wait for you")
	case m > 0:
		w.Line = countOf(m, "of your reviews waits for others", "of your reviews wait for others")
	}
	for _, x := range append(forYou, onOthers...) {
		if len(w.Items) == 8 {
			break
		}
		detail := whyWords[x.why]
		if x.why == "" {
			detail = statusWords(x.rv.Status, x.rv.Participants)
		}
		w.Items = append(w.Items, core.WidgetItem{ID: strconv.FormatInt(x.rv.ID, 10), Title: x.rv.Title, Detail: dots(detail, dueWords(x.rv.DueDate, now)),
			URL: reviewPath(x.rv.ID)})
	}
	return w, nil
}
