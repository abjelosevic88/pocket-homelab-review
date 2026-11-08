package server

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListThreads(r.Context(), reviewFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "comments")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type commentInput struct {
	RevisionID int64         `json:"revision_id"`
	ParentID   int64         `json:"parent_id"`
	Kind       string        `json:"kind"`
	Severity   string        `json:"severity"`
	Category   string        `json:"category"`
	Body       string        `json:"body"`
	Page       int           `json:"page"`
	Anchor     *store.Anchor `json:"anchor"`
	Images     *[]int64      `json:"images"` // nil on an edit keeps the pictures as they are
}

// cleanAnchor keeps boxes inside the page and the quote to a sane length.
func cleanAnchor(a *store.Anchor) *store.Anchor {
	if a == nil {
		return nil
	}
	clamp := func(v float64) float64 {
		if math.IsNaN(v) {
			return 0
		}
		return math.Max(0, math.Min(1, v))
	}
	out := &store.Anchor{Quote: strings.TrimSpace(a.Quote)}
	if utf8.RuneCountInString(out.Quote) > 1000 {
		out.Quote = string([]rune(out.Quote)[:1000]) + "…"
	}
	for i, r := range a.Rects {
		if i >= 50 {
			break
		}
		x, y := clamp(r.X), clamp(r.Y)
		out.Rects = append(out.Rects, store.Rect{X: x, Y: y, W: math.Min(clamp(r.W), 1-x), H: math.Min(clamp(r.H), 1-y)})
	}
	if len(out.Rects) == 0 && out.Quote == "" {
		return nil
	}
	return out
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !canComment(rv) {
		writeErr(w, http.StatusForbidden, "observers can read but not comment")
		return
	}
	if !s.mustBeOpen(w, rv) {
		return
	}
	var in commentInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	var images []int64
	if in.Images != nil {
		var ok bool
		if images, ok = s.cleanImages(w, r, rv, *in.Images); !ok {
			return
		}
	}
	if (in.Body == "" && len(images) == 0) || utf8.RuneCountInString(in.Body) > 20000 {
		writeErr(w, http.StatusBadRequest, "a comment needs text (up to 20,000 characters) or a picture")
		return
	}
	c := &store.Comment{ReviewID: rv.ID, AuthorID: actorID(ctx), Body: in.Body, Images: images}
	var parent *store.Comment
	if in.ParentID != 0 {
		p, err := s.store.GetComment(ctx, rv.ID, in.ParentID)
		if err != nil {
			s.fail(w, err, "parent comment")
			return
		}
		if p.ParentID != 0 {
			p, err = s.store.GetComment(ctx, rv.ID, p.ParentID) // reply to the thread, not to a reply
			if err != nil {
				s.fail(w, err, "parent comment")
				return
			}
		}
		parent = p
		c.ParentID, c.RevisionID, c.Page, c.Kind = p.ID, p.RevisionID, p.Page, "comment"
	} else {
		if in.Kind == "" {
			in.Kind = "comment"
		}
		if !store.OneOf(in.Kind, store.CommentKinds) {
			writeErr(w, http.StatusBadRequest, "kind must be comment, question or issue")
			return
		}
		if in.Kind == "issue" {
			if in.Severity == "" {
				in.Severity = "major"
			}
			if !store.OneOf(in.Severity, store.Severities) {
				writeErr(w, http.StatusBadRequest, "severity must be minor, major or critical")
				return
			}
		} else {
			in.Severity = ""
		}
		if in.RevisionID == 0 {
			in.RevisionID = rv.CurrentRevisionID
		}
		rev, err := s.store.GetRevision(ctx, rv.ID, in.RevisionID)
		if err != nil {
			s.fail(w, err, "revision")
			return
		}
		if in.Page < 0 || (rev.PageCount > 0 && in.Page > rev.PageCount) {
			writeErr(w, http.StatusBadRequest, "page is out of range")
			return
		}
		c.RevisionID, c.Page, c.Kind, c.Severity = rev.ID, in.Page, in.Kind, in.Severity
		c.Category = snippet(in.Category, 40)
		c.Anchor = cleanAnchor(in.Anchor)
	}
	if err := s.store.CreateComment(ctx, c, func(tx *sql.Tx) error {
		return s.queueEvent(ctx, tx, "review.comment_added", fmt.Sprint(c.ID), s.eventPayload(ctx, rv, map[string]any{"comment_id": c.ID, "kind": c.Kind, "severity": c.Severity, "page": c.Page, "text": snippet(commentText(c), 200)}))
	}); err != nil {
		s.fail(w, err, "create comment")
		return
	}
	kind, summary := "comment.added", ""
	switch {
	case parent != nil:
		kind = "comment.replied"
		summary = fmt.Sprintf("Replied to #%d: %s", parent.ID, snippet(commentText(c), 120))
	case c.Kind == "issue":
		kind = "issue.raised"
		summary = fmt.Sprintf("Raised a %s issue #%d: %s", c.Severity, c.ID, snippet(commentText(c), 120))
	case c.Kind == "question":
		kind = "question.asked"
		summary = fmt.Sprintf("Asked #%d: %s", c.ID, snippet(commentText(c), 120))
	default:
		summary = fmt.Sprintf("Commented #%d: %s", c.ID, snippet(commentText(c), 120))
	}
	s.event(ctx, rv, store.Event{Kind: kind, CommentID: c.ID, RevisionID: c.RevisionID, Page: c.Page, Summary: summary}, "comments", "review")
	s.tellComment(ctx, rv, c, parent)

	out, _ := s.store.GetComment(ctx, rv.ID, c.ID)
	writeJSON(w, http.StatusCreated, out)
}

// handleEditComment: authors edit their own words; a root's kind and
// severity may change with it (an issue downgraded to a comment is logged).
func (s *Server) handleEditComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustBeOpen(w, rv) {
		return
	}
	c, err := s.store.GetComment(ctx, rv.ID, idParam(r, "cid"))
	if err != nil {
		s.fail(w, err, "comment")
		return
	}
	if c.AuthorID != actorID(ctx) || c.Deleted {
		writeErr(w, http.StatusForbidden, "only the author can edit a comment")
		return
	}
	var in commentInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body := strings.TrimSpace(in.Body)
	images := c.Images
	if in.Images != nil {
		var ok bool
		if images, ok = s.cleanImages(w, r, rv, *in.Images); !ok {
			return
		}
	}
	if (body == "" && len(images) == 0) || utf8.RuneCountInString(body) > 20000 {
		writeErr(w, http.StatusBadRequest, "a comment needs text (up to 20,000 characters) or a picture")
		return
	}
	before, oldBody := c.Kind+"/"+c.Severity, c.Body
	c.Body, c.Images = body, images
	if c.ParentID == 0 {
		if in.Kind != "" {
			if !store.OneOf(in.Kind, store.CommentKinds) {
				writeErr(w, http.StatusBadRequest, "kind must be comment, question or issue")
				return
			}
			c.Kind = in.Kind
		}
		if c.Kind == "issue" {
			if in.Severity != "" {
				if !store.OneOf(in.Severity, store.Severities) {
					writeErr(w, http.StatusBadRequest, "severity must be minor, major or critical")
					return
				}
				c.Severity = in.Severity
			}
			if c.Severity == "" {
				c.Severity = "major"
			}
		} else {
			c.Severity = ""
		}
		c.Category = snippet(in.Category, 40)
	}
	if err := s.store.EditComment(ctx, c); err != nil {
		s.fail(w, err, "edit comment")
		return
	}
	summary := fmt.Sprintf("Edited #%d", c.ID)
	if after := c.Kind + "/" + c.Severity; after != before {
		summary += fmt.Sprintf(" (%s → %s)", strings.TrimSuffix(before, "/"), strings.TrimSuffix(after, "/"))
	}
	s.event(ctx, rv, store.Event{Kind: "comment.edited", CommentID: c.ID, RevisionID: c.RevisionID, Page: c.Page, Summary: summary}, "comments", "review")
	s.tellEdit(ctx, rv, c, oldBody)
	out, _ := s.store.GetComment(ctx, rv.ID, c.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustBeOpen(w, rv) {
		return
	}
	c, err := s.store.GetComment(ctx, rv.ID, idParam(r, "cid"))
	if err != nil {
		s.fail(w, err, "comment")
		return
	}
	if c.AuthorID != actorID(ctx) {
		writeErr(w, http.StatusForbidden, "only the author can delete a comment")
		return
	}
	if err := s.store.DeleteComment(ctx, rv.ID, c.ID); err != nil {
		s.fail(w, err, "delete comment")
		return
	}
	s.event(ctx, rv, store.Event{Kind: "comment.deleted", CommentID: c.ID, RevisionID: c.RevisionID, Page: c.Page,
		Summary: fmt.Sprintf("Deleted #%d: %s", c.ID, snippet(commentText(c), 120))}, "comments", "review")
	w.WriteHeader(http.StatusNoContent)
}

// handleCommentStatus resolves, rejects or reopens a thread. The owner and
// approvers moderate every thread; whoever opened a thread may close or
// reopen their own. The note becomes part of the thread.
func (s *Server) handleCommentStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustBeOpen(w, rv) {
		return
	}
	c, err := s.store.GetComment(ctx, rv.ID, idParam(r, "cid"))
	if err != nil {
		s.fail(w, err, "comment")
		return
	}
	if c.ParentID != 0 {
		writeErr(w, http.StatusBadRequest, "resolve the thread, not a reply")
		return
	}
	if !canModerate(rv) && c.AuthorID != actorID(ctx) {
		writeErr(w, http.StatusForbidden, "only the owner, an approver or whoever opened the thread can change its state")
		return
	}
	var in struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := decode(r, &in); err != nil || !store.OneOf(in.Status, store.CommentStates) {
		writeErr(w, http.StatusBadRequest, "status must be open, resolved or rejected")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Status == c.Status {
		writeJSON(w, http.StatusOK, c)
		return
	}
	if in.Status == "rejected" && in.Note == "" {
		writeErr(w, http.StatusBadRequest, "say why the thread is rejected")
		return
	}
	if err := s.store.SetCommentStatus(ctx, rv.ID, c.ID, in.Status, in.Note, actorID(ctx)); err != nil {
		s.fail(w, err, "set status")
		return
	}
	verb := map[string]string{"open": "Reopened", "resolved": "Resolved", "rejected": "Rejected"}[in.Status]
	what := "thread"
	if c.Kind == "issue" {
		what = c.Severity + " issue"
	} else if c.Kind == "question" {
		what = "question"
	}
	summary := fmt.Sprintf("%s %s #%d", verb, what, c.ID)
	if in.Note != "" {
		summary += " — " + snippet(in.Note, 160)
	}
	kind := "comment." + map[string]string{"open": "reopened", "resolved": "resolved", "rejected": "rejected"}[in.Status]
	s.event(ctx, rv, store.Event{Kind: kind, CommentID: c.ID, RevisionID: c.RevisionID, Page: c.Page, Summary: summary}, "comments", "review")
	s.tellThreadStatus(ctx, rv, c, in.Status, in.Note)
	out, _ := s.store.GetComment(ctx, rv.ID, c.ID)
	writeJSON(w, http.StatusOK, out)
}

// ---- tasks ----

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListTasks(r.Context(), reviewFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "tasks")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type taskInput struct {
	CommentID  int64  `json:"comment_id"`
	Title      string `json:"title"`
	Notes      string `json:"notes"`
	AssigneeID int64  `json:"assignee_id"`
	DueDate    string `json:"due_date"`
	Status     string `json:"status"`
}

func (s *Server) validTask(w http.ResponseWriter, rv *store.Review, in *taskInput) bool {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 300 {
		writeErr(w, http.StatusBadRequest, "a task needs a title (up to 300 characters)")
		return false
	}
	if !validDate(in.DueDate) {
		writeErr(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
		return false
	}
	if in.AssigneeID != 0 {
		ok := false
		for _, p := range rv.Participants {
			ok = ok || p.UserID == in.AssigneeID
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "tasks go to people on the review")
			return false
		}
	}
	return true
}

func (s *Server) assigneeName(rv *store.Review, id int64) string {
	for _, p := range rv.Participants {
		if p.UserID == id {
			return firstNonEmpty(p.Name, p.Email)
		}
	}
	return "nobody"
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !canComment(rv) {
		writeErr(w, http.StatusForbidden, "observers can read but not add tasks")
		return
	}
	if !s.mustBeOpen(w, rv) {
		return
	}
	var in taskInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !s.validTask(w, rv, &in) {
		return
	}
	if in.CommentID != 0 {
		if _, err := s.store.GetComment(ctx, rv.ID, in.CommentID); err != nil {
			s.fail(w, err, "comment")
			return
		}
	}
	t := &store.Task{ReviewID: rv.ID, CommentID: in.CommentID, Title: in.Title, Notes: strings.TrimSpace(in.Notes), AssigneeID: in.AssigneeID, DueDate: in.DueDate, CreatedBy: actorID(ctx)}
	if err := s.store.CreateTask(ctx, t); err != nil {
		s.fail(w, err, "create task")
		return
	}
	summary := fmt.Sprintf("Added task #%d: %s", t.ID, snippet(t.Title, 120))
	if t.AssigneeID != 0 {
		summary += " → " + s.assigneeName(rv, t.AssigneeID)
	}
	if t.CommentID != 0 {
		summary += fmt.Sprintf(" (from #%d)", t.CommentID)
	}
	s.event(ctx, rv, store.Event{Kind: "task.added", TaskID: t.ID, CommentID: t.CommentID, Summary: summary}, "tasks", "review")
	s.tellTask(ctx, rv, t)
	out, _ := s.store.GetTask(ctx, rv.ID, t.ID)
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateTask: the owner, approvers, the task's creator and its
// assignee may change it; ticking it off is logged on its own.
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustBeOpen(w, rv) {
		return
	}
	t, err := s.store.GetTask(ctx, rv.ID, idParam(r, "tid"))
	if err != nil {
		s.fail(w, err, "task")
		return
	}
	me := actorID(ctx)
	if !canModerate(rv) && t.CreatedBy != me && t.AssigneeID != me {
		writeErr(w, http.StatusForbidden, "only the owner, an approver, the task's creator or its assignee can change it")
		return
	}
	in := taskInput{Title: t.Title, Notes: t.Notes, AssigneeID: t.AssigneeID, DueDate: t.DueDate, Status: t.Status}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !s.validTask(w, rv, &in) {
		return
	}
	if in.Status != "open" && in.Status != "done" {
		writeErr(w, http.StatusBadRequest, "status must be open or done")
		return
	}
	was := *t
	t.Title, t.Notes, t.AssigneeID, t.DueDate, t.Status = in.Title, strings.TrimSpace(in.Notes), in.AssigneeID, in.DueDate, in.Status
	if was.Status == "done" && t.Status == "done" {
		t.DoneAt = was.DoneAt
	} else {
		t.DoneAt = nil
	}
	if err := s.store.UpdateTask(ctx, t, me); err != nil {
		s.fail(w, err, "update task")
		return
	}
	var parts []string
	kind := "task.updated"
	switch {
	case was.Status != "done" && t.Status == "done":
		kind, parts = "task.done", []string{fmt.Sprintf("Completed task #%d: %s", t.ID, snippet(t.Title, 120))}
	case was.Status == "done" && t.Status == "open":
		kind, parts = "task.reopened", []string{fmt.Sprintf("Reopened task #%d: %s", t.ID, snippet(t.Title, 120))}
	default:
		parts = []string{fmt.Sprintf("Updated task #%d", t.ID)}
	}
	if was.AssigneeID != t.AssigneeID {
		parts = append(parts, "assigned to "+s.assigneeName(rv, t.AssigneeID))
	}
	if was.DueDate != t.DueDate {
		parts = append(parts, "due "+firstNonEmpty(t.DueDate, "—"))
	}
	if was.Title != t.Title && kind == "task.updated" {
		parts = append(parts, "renamed to "+snippet(t.Title, 80))
	}
	s.event(ctx, rv, store.Event{Kind: kind, TaskID: t.ID, CommentID: t.CommentID, Summary: strings.Join(parts, ", ")}, "tasks", "review")
	if was.AssigneeID != t.AssigneeID {
		s.tellTask(ctx, rv, t)
	}
	out, _ := s.store.GetTask(ctx, rv.ID, t.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustBeOpen(w, rv) {
		return
	}
	t, err := s.store.GetTask(ctx, rv.ID, idParam(r, "tid"))
	if err != nil {
		s.fail(w, err, "task")
		return
	}
	if !canModerate(rv) && t.CreatedBy != actorID(ctx) {
		writeErr(w, http.StatusForbidden, "only the owner, an approver or the task's creator can delete it")
		return
	}
	if err := s.store.DeleteTask(ctx, rv.ID, t.ID); err != nil {
		s.fail(w, err, "delete task")
		return
	}
	s.event(ctx, rv, store.Event{Kind: "task.deleted", TaskID: t.ID, Summary: fmt.Sprintf("Deleted task #%d: %s", t.ID, snippet(t.Title, 120))}, "tasks", "review")
	w.WriteHeader(http.StatusNoContent)
}
