package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// The live channel: one WebSocket per open document. It carries presence (who
// is here, on which page, active or idle), cursors, "something changed"
// notices so every screen refetches, and the signal review time is measured
// from.
//
// Review time is the server's to measure, not the browser's to report. Every
// TickInterval the hub credits each person on a review with the time since
// their last credit (capped at two ticks), but only while at least one of
// their tabs is visible and they did something (scrolled, moved the mouse,
// typed, zoomed) within IdleAfter. Several tabs count once. The time goes to
// the page they are on and to their current reading session; a pause longer
// than SessionGap starts a new session.
//
// Browser → server:
//   {"t":"state","page":3,"visible":true,"input":true}   on change and every few seconds
//   {"t":"cursor","page":3,"x":0.41,"y":0.2}              page 0 hides the cursor
//   {"t":"selection","page":3,"rects":[{x,y,w,h}]}         text selected, not yet commented; page 0 clears it
// Server → browser:
//   {"t":"hello","conn":"c7","color":"#2563eb"}
//   {"t":"presence","people":[…]}
//   {"t":"cursor","conn":"c7","user_id":2,"name":"Ben","color":"…","page":3,"x":…,"y":…}
//   {"t":"selection","conn":"c7","user_id":2,"name":"Ben","color":"…","page":3,"rects":[…]}
//   {"t":"changed","what":["comments","events"],"by":2}
//   {"t":"bye","reason":"removed"}
//   {"t":"inbox"}                                         a new notification for you (on any review)

var liveColors = []string{"#2563eb", "#db2777", "#059669", "#d97706", "#7c3aed", "#dc2626", "#0891b2", "#65a30d", "#c026d3", "#475569"}

type liveConn struct {
	id         string
	user       *store.User
	reviewID   int64
	revisionID int64
	color      string
	page       int
	visible    bool
	lastInput  time.Time
	joined     time.Time
	send       chan []byte
	close      context.CancelFunc
}

func (c *liveConn) active(now time.Time, idle time.Duration) bool {
	return c.visible && now.Sub(c.lastInput) <= idle
}

type room struct {
	conns      map[*liveConn]struct{}
	lastCredit map[int64]time.Time // user → when they were last credited
	wasActive  map[int64]bool      // user → active at the last tick (presence changes)
}

type hub struct {
	s     *Server
	mu    sync.Mutex
	rooms map[int64]*room
	seq   int
}

// presence is one open tab, as others see it.
type presence struct {
	Conn       string `json:"conn"`
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	Page       int    `json:"page"`
	RevisionID int64  `json:"revision_id"`
	Active     bool   `json:"active"`
	Since      string `json:"since"`
}

func newHub(s *Server) *hub { return &hub{s: s, rooms: map[int64]*room{}} }

func (h *hub) connCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.rooms {
		n += len(r.conns)
	}
	return n
}

// people lists who has a review open right now, one entry per tab.
func (h *hub) people(reviewID int64) []presence {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peopleLocked(reviewID, time.Now())
}

func (h *hub) peopleLocked(reviewID int64, now time.Time) []presence {
	out := []presence{}
	r := h.rooms[reviewID]
	if r == nil {
		return out
	}
	for c := range r.conns {
		out = append(out, presence{Conn: c.id, UserID: c.user.ID, Name: displayName(c.user), Color: c.color, Page: c.page,
			RevisionID: c.revisionID, Active: c.active(now, h.s.cfg.IdleAfter), Since: c.joined.UTC().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

func (h *hub) colorFor(r *room, u *store.User) string {
	for c := range r.conns {
		if c.user.ID == u.ID {
			return c.color // the same person keeps one colour across tabs
		}
	}
	used := map[string]bool{}
	for c := range r.conns {
		used[c.color] = true
	}
	start := int(u.ID) % len(liveColors)
	for i := range liveColors {
		if col := liveColors[(start+i)%len(liveColors)]; !used[col] {
			return col
		}
	}
	return liveColors[start]
}

func (h *hub) join(c *liveConn) {
	h.mu.Lock()
	r := h.rooms[c.reviewID]
	if r == nil {
		r = &room{conns: map[*liveConn]struct{}{}, lastCredit: map[int64]time.Time{}, wasActive: map[int64]bool{}}
		h.rooms[c.reviewID] = r
	}
	h.seq++
	c.id = "c" + strconv.Itoa(h.seq)
	c.color = h.colorFor(r, c.user)
	r.conns[c] = struct{}{}
	h.mu.Unlock()
	c.push(map[string]any{"t": "hello", "conn": c.id, "color": c.color})
	h.broadcastPresence(c.reviewID)
}

func (h *hub) leave(c *liveConn) {
	h.mu.Lock()
	if r := h.rooms[c.reviewID]; r != nil {
		delete(r.conns, c)
		if len(r.conns) == 0 {
			delete(h.rooms, c.reviewID)
		}
	}
	h.mu.Unlock()
	h.broadcastPresence(c.reviewID)
	h.broadcast(c.reviewID, nil, map[string]any{"t": "cursor", "conn": c.id, "page": 0})
	h.broadcast(c.reviewID, nil, map[string]any{"t": "selection", "conn": c.id, "page": 0})
}

// push queues a message; a tab that cannot keep up loses cursor frames, not the server.
func (c *liveConn) push(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default:
	}
}

func (h *hub) broadcast(reviewID int64, except *liveConn, v any) {
	h.mu.Lock()
	r := h.rooms[reviewID]
	var targets []*liveConn
	if r != nil {
		for c := range r.conns {
			if c != except {
				targets = append(targets, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.push(v)
	}
}

func (h *hub) broadcastPresence(reviewID int64) {
	h.broadcast(reviewID, nil, map[string]any{"t": "presence", "people": h.people(reviewID)})
}

// notify tells everyone with the review open what to refetch.
func (h *hub) notify(reviewID, by int64, what ...string) {
	h.broadcast(reviewID, nil, map[string]any{"t": "changed", "what": what, "by": by})
}

// inbox tells a person's open documents that they have a new notification.
func (h *hub) inbox(userID int64) {
	h.mu.Lock()
	var targets []*liveConn
	for _, r := range h.rooms {
		for c := range r.conns {
			if c.user.ID == userID {
				targets = append(targets, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.push(map[string]any{"t": "inbox"})
	}
}

// kick closes a person's tabs on a review (they were taken off it).
func (h *hub) kick(reviewID, userID int64) {
	h.mu.Lock()
	var targets []*liveConn
	if r := h.rooms[reviewID]; r != nil {
		for c := range r.conns {
			if c.user.ID == userID {
				targets = append(targets, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.push(map[string]any{"t": "bye", "reason": "removed"})
		time.AfterFunc(200*time.Millisecond, c.close)
	}
}

type credit struct {
	reviewID, revisionID, userID int64
	page, seconds                int
}

// run is the review clock.
func (h *hub) run(ctx context.Context) {
	t := time.NewTicker(h.s.cfg.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.tick(ctx, now)
		}
	}
}

func (h *hub) tick(ctx context.Context, now time.Time) {
	cfg := h.s.cfg
	maxStep := 2 * cfg.TickInterval
	var credits []credit
	var changed []int64
	h.mu.Lock()
	for rid, r := range h.rooms {
		// per person: their most recently used active tab
		best := map[int64]*liveConn{}
		for c := range r.conns {
			if !c.active(now, cfg.IdleAfter) {
				continue
			}
			if b := best[c.user.ID]; b == nil || c.lastInput.After(b.lastInput) {
				best[c.user.ID] = c
			}
		}
		presenceChanged := false
		for uid, c := range best {
			step := cfg.TickInterval
			if last, ok := r.lastCredit[uid]; ok {
				step = min(now.Sub(last), maxStep)
			}
			r.lastCredit[uid] = now
			if secs := int(math.Round(step.Seconds())); secs > 0 {
				credits = append(credits, credit{rid, c.revisionID, uid, c.page, secs})
			}
			if !r.wasActive[uid] {
				presenceChanged = true
			}
			r.wasActive[uid] = true
		}
		for uid := range r.wasActive {
			if best[uid] == nil {
				delete(r.wasActive, uid)
				delete(r.lastCredit, uid)
				presenceChanged = true
			}
		}
		if presenceChanged {
			changed = append(changed, rid)
		}
	}
	h.mu.Unlock()

	for _, cr := range credits {
		fresh, err := h.s.store.CreditTime(ctx, cr.reviewID, cr.revisionID, cr.userID, cr.page, cr.seconds, now, cfg.SessionGap)
		if err != nil {
			h.s.log.Warn("credit review time", "review", cr.reviewID, "user", cr.userID, "err", err)
			continue
		}
		if fresh {
			e := store.Event{ReviewID: cr.reviewID, UserID: cr.userID, Kind: "session.started", RevisionID: cr.revisionID, Page: cr.page, Summary: "Started a reading session"}
			if rev, err := h.s.store.GetRevision(ctx, cr.reviewID, cr.revisionID); err == nil {
				e.Summary = fmt.Sprintf("Started a reading session on v%d, page %d", rev.Number, cr.page)
			}
			h.s.store.LogEvent(ctx, e)
			h.notify(cr.reviewID, cr.userID, "events", "people")
		}
	}
	for _, rid := range changed {
		h.broadcastPresence(rid)
	}
}

// ---- the WebSocket ----

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	u := userFrom(r.Context())
	revID := queryInt(r, "revision")
	if revID == 0 {
		revID = rv.CurrentRevisionID
	} else if _, err := s.store.GetRevision(r.Context(), rv.ID, revID); err != nil {
		s.fail(w, err, "revision")
		return
	}
	ws, err := websocket.Accept(w, r, nil) // same-origin only
	if err != nil {
		return
	}
	ws.SetReadLimit(16384)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	c := &liveConn{user: u, reviewID: rv.ID, revisionID: revID, page: int(max(queryInt(r, "page"), 1)), visible: true,
		lastInput: now, joined: now, send: make(chan []byte, 64), close: cancel}

	if s.store.MarkOpened(ctx, rv.ID, u.ID) {
		s.event(ctx, rv, store.Event{UserID: u.ID, Kind: "review.opened", RevisionID: revID, Summary: "Opened the document for the first time"}, "people")
	}
	s.hub.join(c)
	defer s.hub.leave(c)

	go func() { // writer
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-c.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	var lastCursor time.Time
	for {
		// the browser reports every few seconds; silence for 45s means it is gone
		rctx, rcancel := context.WithTimeout(ctx, 45*time.Second)
		_, data, err := ws.Read(rctx)
		rcancel()
		if err != nil {
			break
		}
		var m struct {
			T       string       `json:"t"`
			Page    int          `json:"page"`
			Visible *bool        `json:"visible"`
			Input   bool         `json:"input"`
			X       float64      `json:"x"`
			Y       float64      `json:"y"`
			Rects   []store.Rect `json:"rects"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "state":
			s.hub.mu.Lock()
			before := fmt.Sprint(c.page, c.visible, c.active(time.Now(), s.cfg.IdleAfter))
			if m.Page > 0 {
				c.page = m.Page
			}
			if m.Visible != nil {
				c.visible = *m.Visible
			}
			if m.Input {
				c.lastInput = time.Now()
			}
			after := fmt.Sprint(c.page, c.visible, c.active(time.Now(), s.cfg.IdleAfter))
			s.hub.mu.Unlock()
			if before != after {
				s.hub.broadcastPresence(rv.ID)
			}
		case "cursor":
			if time.Since(lastCursor) < 30*time.Millisecond && m.Page != 0 {
				continue
			}
			lastCursor = time.Now()
			s.hub.mu.Lock()
			c.lastInput = lastCursor
			if m.Page > 0 {
				c.page = m.Page
			}
			s.hub.mu.Unlock()
			s.hub.broadcast(rv.ID, c, map[string]any{"t": "cursor", "conn": c.id, "user_id": u.ID, "name": displayName(u), "color": c.color,
				"page": m.Page, "x": math.Max(0, math.Min(1, m.X)), "y": math.Max(0, math.Min(1, m.Y))})
		case "selection":
			// text someone has selected (not yet a comment): shown to the others, never stored
			out := map[string]any{"t": "selection", "conn": c.id, "user_id": u.ID, "name": displayName(u), "color": c.color, "page": 0}
			if a := cleanAnchor(&store.Anchor{Rects: m.Rects}); a != nil && m.Page > 0 {
				out["page"], out["rects"] = m.Page, a.Rects
				s.hub.mu.Lock()
				c.lastInput = time.Now()
				s.hub.mu.Unlock()
			}
			s.hub.broadcast(rv.ID, c, out)
		}
	}
	ws.Close(websocket.StatusNormalClosure, "")
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.people(reviewFrom(r.Context()).ID))
}
