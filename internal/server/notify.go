package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// ---- notifications ----
//
// Someone is told when they are mentioned (@Name, @everyone), when a thread
// they wrote in gets a reply or is closed, when a new version is uploaded,
// when they are added to a review, it starts or is decided, when a reviewer
// finishes on their review, and when a task is given to them. Each lands in
// their inbox (the bell); browsers and phones that allowed it get a Web Push.
// Nobody is told about what they did themselves.

type notice struct {
	kind      string // one of store.NotifyKinds
	commentID int64
	title     string // who did what: "Sani mentioned you"
	body      string // the words, a file name, a note
}

// tell notifies people about something on a review.
func (s *Server) tell(ctx context.Context, rv *store.Review, uids []int64, n notice) {
	actor := actorID(ctx)
	seen := map[int64]bool{actor: true, 0: true}
	for _, uid := range uids {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		if s.store.NotifyOff(ctx, uid)[n.kind] {
			continue
		}
		row := &store.Notification{UserID: uid, ReviewID: rv.ID, Kind: n.kind, ActorID: actor, CommentID: n.commentID, Title: n.title, Body: n.body}
		if err := s.store.AddNotification(ctx, row); err != nil {
			s.log.Warn("notification", "err", err)
			continue
		}
		s.hub.inbox(uid)
		link := fmt.Sprintf("%s/reviews/%d", s.cfg.BaseURL, rv.ID)
		if n.commentID != 0 {
			link += fmt.Sprintf("?c=%d", n.commentID)
		}
		body := rv.Title
		if n.body != "" {
			body += ": " + n.body
		}
		go s.webPush(context.WithoutCancel(ctx), uid, n.title, body, link, fmt.Sprintf("review-%d", rv.ID))
	}
}

// everyone on a review.
func everyone(rv *store.Review) []int64 {
	out := make([]int64, 0, len(rv.Participants))
	for _, p := range rv.Participants {
		out = append(out, p.UserID)
	}
	return out
}

func without(ids []int64, drop []int64) []int64 {
	out := ids[:0:0]
	for _, id := range ids {
		keep := true
		for _, d := range drop {
			keep = keep && id != d
		}
		if keep {
			out = append(out, id)
		}
	}
	return out
}

func actorName(ctx context.Context) string {
	u := userFrom(ctx)
	if u == nil {
		return "Someone"
	}
	if u.Name != "" {
		return strings.Fields(u.Name)[0]
	}
	return displayName(u)
}

// ---- mentions ----

type handle struct {
	text string // lower case, without the @
	uids []int64
}

// handles are the ways people on a review can be @mentioned: their full name,
// their first name when nobody else on the review shares it, the part of
// their email before the @, and @everyone / @all.
func handles(people []store.Participant) []handle {
	byText := map[string][]int64{}
	first := map[string]int{}
	for _, p := range people {
		if f := strings.Fields(strings.ToLower(p.Name)); len(f) > 0 {
			first[f[0]]++
		}
	}
	add := func(t string, id int64) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			return
		}
		for _, x := range byText[t] {
			if x == id {
				return
			}
		}
		byText[t] = append(byText[t], id)
	}
	all := []int64{}
	for _, p := range people {
		all = append(all, p.UserID)
		add(p.Name, p.UserID)
		if f := strings.Fields(strings.ToLower(p.Name)); len(f) > 1 && first[f[0]] == 1 {
			add(f[0], p.UserID)
		}
		if at := strings.IndexByte(p.Email, '@'); at > 0 {
			add(p.Email[:at], p.UserID)
		}
	}
	out := []handle{{"everyone", all}, {"all", all}}
	for t, ids := range byText {
		if t != "everyone" && t != "all" {
			out = append(out, handle{t, ids})
		}
	}
	// the longest first, so "@Sani Bjelosevic" wins over "@Sani"
	sort.Slice(out, func(i, j int) bool { return len(out[i].text) > len(out[j].text) })
	return out
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// mentioned returns who a text @mentions among the people on a review.
func mentioned(text string, people []store.Participant) []int64 {
	if !strings.Contains(text, "@") {
		return nil
	}
	low := strings.ToLower(text)
	hs := handles(people)
	seen := map[int64]bool{}
	var out []int64
	for i := 0; i < len(low); i++ {
		if low[i] != '@' {
			continue
		}
		if i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(low[:i]); wordRune(r) || r == '.' {
				continue // an email address, not a mention
			}
		}
		rest := low[i+1:]
		for _, h := range hs {
			if !strings.HasPrefix(rest, h.text) {
				continue
			}
			if r, _ := utf8.DecodeRuneInString(rest[len(h.text):]); r != utf8.RuneError && wordRune(r) {
				continue
			}
			for _, id := range h.uids {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			break
		}
	}
	return out
}

// ---- the inbox API ----

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	list, unread, err := s.store.ListNotifications(r.Context(), actorID(r.Context()), int(queryInt(r, "limit")))
	if err != nil {
		s.fail(w, err, "notifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "unread": unread})
}

func (s *Server) handleUnreadNotifications(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.UnreadNotifications(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "notifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread": n})
}

// handleReadNotifications marks notifications read: {"ids":[…]}, {"review_id":3,"comment_id":9}, or {} for all.
func (s *Server) handleReadNotifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		IDs       []int64 `json:"ids"`
		ReviewID  int64   `json:"review_id"`
		CommentID int64   `json:"comment_id"`
	}
	_ = decode(r, &in)
	var err error
	if in.ReviewID != 0 {
		err = s.store.ReadReviewNotifications(ctx, actorID(ctx), in.ReviewID, in.CommentID)
	} else {
		err = s.store.ReadNotifications(ctx, actorID(ctx), in.IDs)
	}
	if err != nil {
		s.fail(w, err, "read notifications")
		return
	}
	n, _ := s.store.UnreadNotifications(ctx, actorID(ctx))
	writeJSON(w, http.StatusOK, map[string]any{"unread": n})
}

func (s *Server) handleNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method == http.MethodPut {
		var in struct {
			Off []string `json:"off"`
		}
		if err := decode(r, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		var off []string
		for _, k := range in.Off {
			if store.OneOf(k, store.NotifyKinds) {
				off = append(off, k)
			}
		}
		if err := s.store.SaveNotifyOff(ctx, actorID(ctx), off); err != nil {
			s.fail(w, err, "notification settings")
			return
		}
	}
	off := []string{}
	for k := range s.store.NotifyOff(ctx, actorID(ctx)) {
		off = append(off, k)
	}
	sort.Strings(off)
	subs, _ := s.store.PushSubs(ctx, actorID(ctx))
	writeJSON(w, http.StatusOK, map[string]any{"off": off, "kinds": store.NotifyKinds, "browsers": len(subs)})
}

// ---- Web Push ----
//
// People on a review are rarely on this box's notification service, so
// Review sends to their browser or phone itself: they allow notifications
// once (Review added to the home screen on an iPhone) and the browser keeps a
// subscription that Review signs with its own key (VAPID, made on first use).

var vapidMu sync.Mutex

type vapidKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

func (s *Server) vapid(ctx context.Context) (vapidKeys, error) {
	vapidMu.Lock()
	defer vapidMu.Unlock()
	var k vapidKeys
	if v := s.store.Setting(ctx, "vapid"); v != "" && json.Unmarshal([]byte(v), &k) == nil && k.Public != "" {
		return k, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return k, err
	}
	k = vapidKeys{Public: pub, Private: priv}
	b, _ := json.Marshal(k)
	return k, s.store.SetSetting(ctx, "vapid", string(b))
}

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	k, err := s.vapid(r.Context())
	if err != nil {
		s.fail(w, err, "push key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_key": k.Public})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decode(r, &in); err != nil || !strings.HasPrefix(in.Endpoint, "https://") || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeErr(w, http.StatusBadRequest, "not a push subscription")
		return
	}
	p := &store.PushSub{UserID: actorID(ctx), Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, Agent: snippet(r.UserAgent(), 200)}
	if err := s.store.SavePushSub(ctx, p); err != nil {
		s.fail(w, err, "subscribe")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	_ = decode(r, &in)
	_ = s.store.DeletePushSub(r.Context(), actorID(r.Context()), in.Endpoint)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := s.webPush(ctx, actorID(ctx), "Pocket Review", "Notifications here work. You'll hear about mentions, replies, new versions and decisions.", s.cfg.BaseURL+"/", "test")
	if n == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no browser took it — allow notifications again"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": fmt.Sprintf("Sent to %d %s", n, map[bool]string{true: "browser", false: "browsers"}[n == 1])})
}

// webPush sends a note to all of someone's browsers; it returns how many took it.
func (s *Server) webPush(ctx context.Context, uid int64, title, body, link, tag string) int {
	subs, err := s.store.PushSubs(ctx, uid)
	if err != nil || len(subs) == 0 {
		return 0
	}
	k, err := s.vapid(ctx)
	if err != nil {
		return 0
	}
	payload, _ := json.Marshal(map[string]any{"title": title, "body": snippet(body, 300), "url": link, "tag": tag})
	subscriber := "mailto:review@pocket.local"
	if s.cfg.AdminEmail != "" {
		subscriber = "mailto:" + s.cfg.AdminEmail
	}
	sent := 0
	for _, p := range subs {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := webpush.SendNotificationWithContext(cctx, payload, &webpush.Subscription{Endpoint: p.Endpoint, Keys: webpush.Keys{P256dh: p.P256dh, Auth: p.Auth}},
			&webpush.Options{Subscriber: subscriber, VAPIDPublicKey: k.Public, VAPIDPrivateKey: k.Private, TTL: 3600 * 24, Urgency: webpush.UrgencyNormal})
		cancel()
		if err != nil {
			s.store.PushResult(ctx, p.ID, false, false)
			continue
		}
		res.Body.Close()
		switch {
		case res.StatusCode == http.StatusGone || res.StatusCode == http.StatusNotFound:
			s.store.PushResult(ctx, p.ID, false, true)
		case res.StatusCode >= 200 && res.StatusCode < 300:
			s.store.PushResult(ctx, p.ID, true, false)
			sent++
		default:
			s.store.PushResult(ctx, p.ID, false, false)
		}
	}
	return sent
}

// ---- what is worth telling ----

// people on a review as it is now (the request's copy can be stale).
func (s *Server) peopleOn(ctx context.Context, rv *store.Review) []int64 {
	list, err := s.store.ListParticipants(ctx, rv.ID)
	if err != nil {
		return everyone(rv)
	}
	out := make([]int64, 0, len(list))
	for _, p := range list {
		out = append(out, p.UserID)
	}
	return out
}

// tellComment: the people @mentioned, then whoever wrote in the thread (a
// reply) or the owner (a new thread).
func (s *Server) tellComment(ctx context.Context, rv *store.Review, c *store.Comment, parent *store.Comment) {
	who := actorName(ctx)
	text := snippet(commentText(c), 200)
	root := c.ID
	if parent != nil {
		root = parent.ID
	}
	ment := mentioned(c.Body, rv.Participants)
	s.tell(ctx, rv, ment, notice{kind: "mention", commentID: root, title: who + " mentioned you", body: text})
	if parent != nil {
		people, _ := s.store.ThreadPeople(ctx, parent.ID)
		s.tell(ctx, rv, without(people, ment), notice{kind: "reply", commentID: root, title: who + " replied", body: text})
		return
	}
	what := who + " commented"
	switch c.Kind {
	case "issue":
		what = fmt.Sprintf("%s raised a %s issue", who, c.Severity)
	case "question":
		what = who + " asked a question"
	}
	if c.Page > 0 {
		what += fmt.Sprintf(" on page %d", c.Page)
	}
	s.tell(ctx, rv, without([]int64{rv.OwnerID}, ment), notice{kind: "thread", commentID: root, title: what, body: text})
}

// tellEdit: only people newly @mentioned by an edit.
func (s *Server) tellEdit(ctx context.Context, rv *store.Review, c *store.Comment, oldBody string) {
	ment := without(mentioned(c.Body, rv.Participants), mentioned(oldBody, rv.Participants))
	root := c.ID
	if c.ParentID != 0 {
		root = c.ParentID
	}
	s.tell(ctx, rv, ment, notice{kind: "mention", commentID: root, title: actorName(ctx) + " mentioned you", body: snippet(commentText(c), 200)})
}

// tellThreadStatus: the thread's opener hears it was resolved, rejected or reopened.
func (s *Server) tellThreadStatus(ctx context.Context, rv *store.Review, c *store.Comment, status, note string) {
	what := "thread"
	if c.Kind == "issue" || c.Kind == "question" {
		what = c.Kind
	}
	verb := map[string]string{"open": "reopened", "resolved": "resolved", "rejected": "rejected"}[status]
	body := note
	if body == "" {
		body = snippet(commentText(c), 160)
	}
	s.tell(ctx, rv, []int64{c.AuthorID}, notice{kind: "reply", commentID: c.ID, title: fmt.Sprintf("%s %s your %s", actorName(ctx), verb, what), body: body})
}

// tellRevision: a new version of a review that is under way.
func (s *Server) tellRevision(ctx context.Context, rv *store.Review, rev *store.Revision) {
	if rv.Status == store.StatusDraft {
		return
	}
	body := rev.Filename
	if rev.Note != "" {
		body = rev.Note + " (" + rev.Filename + ")"
	}
	s.tell(ctx, rv, s.peopleOn(ctx, rv), notice{kind: "revision", title: fmt.Sprintf("%s uploaded v%d", actorName(ctx), rev.Number), body: body})
}

// tellStatus: everyone hears that the review started, ended or came back.
func (s *Server) tellStatus(ctx context.Context, rv *store.Review, action, note string) {
	who := actorName(ctx)
	title := map[string]string{
		"start":    who + " started the review — over to you",
		"resume":   who + " put the review back in review",
		"cancel":   who + " cancelled the review",
		"reopen":   who + " reopened the review",
		"complete": who + " completed the review: approved",
		"approved": "The review is approved",
		"changes":  who + " requested changes",
		"rejected": "The review is rejected",
	}[action]
	if title == "" {
		return
	}
	s.tell(ctx, rv, s.peopleOn(ctx, rv), notice{kind: "review", title: title, body: note})
}

// tellVerdict: the owner hears that someone finished or decided.
func (s *Server) tellVerdict(ctx context.Context, rv *store.Review, decider bool, verdict, note string) {
	if verdict == "" {
		return
	}
	who := actorName(ctx)
	title := who + " finished reviewing: " + map[string]string{"approved": "looks good", "changes": "needs changes", "rejected": "should be rejected"}[verdict]
	if decider {
		title = who + " " + map[string]string{"approved": "approved", "changes": "requested changes", "rejected": "rejected"}[verdict]
	}
	s.tell(ctx, rv, []int64{rv.OwnerID}, notice{kind: "verdict", title: title, body: note})
}

// tellAdded: someone was put on a review.
func (s *Server) tellAdded(ctx context.Context, rv *store.Review, uid int64, role string) {
	body := rv.Description
	if rv.Status == store.StatusDraft {
		body = "It has not started yet."
	}
	s.tell(ctx, rv, []int64{uid}, notice{kind: "review", title: fmt.Sprintf("%s added you as %s", actorName(ctx), role), body: snippet(body, 200)})
}

// tellTask: a task was given to someone.
func (s *Server) tellTask(ctx context.Context, rv *store.Review, t *store.Task) {
	body := t.Title
	if t.DueDate != "" {
		body += " (due " + t.DueDate + ")"
	}
	s.tell(ctx, rv, []int64{t.AssigneeID}, notice{kind: "task", commentID: t.CommentID, title: actorName(ctx) + " gave you a task", body: body})
}
