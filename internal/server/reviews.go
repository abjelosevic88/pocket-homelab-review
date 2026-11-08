package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-review/internal/report"
	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// ---- access ----
//
// Who may do what on a review:
//   owner     everything: edit, people, revisions, start/cancel/reopen, moderate threads
//   approver  comment, moderate threads, tasks, and the decision (approve / request changes / reject)
//   reviewer  comment, tasks, close their own threads, finish with a verdict
//   observer  read only
// Everyone else gets 404, admins included.

const reviewKey ctxKey = 2

func reviewFrom(ctx context.Context) *store.Review {
	r, _ := ctx.Value(reviewKey).(*store.Review)
	return r
}

// withReview loads {rid} as the caller sees it, or answers 404.
func (s *Server) withReview(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rv, err := s.store.GetReview(r.Context(), idParam(r, "rid"), userFrom(r.Context()).ID)
		if err != nil {
			s.fail(w, err, "get review")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reviewKey, rv)))
	})
}

func canComment(rv *store.Review) bool { return rv.MyRole != store.RoleObserver && rv.MyRole != "" }
func canModerate(rv *store.Review) bool {
	return rv.MyRole == store.RoleOwner || rv.MyRole == store.RoleApprover
}

func approvers(rv *store.Review) []store.Participant {
	var out []store.Participant
	for _, p := range rv.Participants {
		if p.Role == store.RoleApprover {
			out = append(out, p)
		}
	}
	return out
}

// canDecide: approvers decide; without any approver on the review the owner does.
func canDecide(rv *store.Review) bool {
	return rv.MyRole == store.RoleApprover || (rv.MyRole == store.RoleOwner && len(approvers(rv)) == 0)
}

func (s *Server) mustOwn(w http.ResponseWriter, rv *store.Review) bool {
	if rv.MyRole != store.RoleOwner {
		writeErr(w, http.StatusForbidden, "only the review's owner can do that")
		return false
	}
	return true
}

func (s *Server) mustBeOpen(w http.ResponseWriter, rv *store.Review) bool {
	if rv.Locked() {
		writeErr(w, http.StatusConflict, "the review is "+statusLabel(rv.Status)+"; reopen it first")
		return false
	}
	return true
}

var statusLabels = map[string]string{
	store.StatusDraft: "a draft", store.StatusInReview: "in review", store.StatusChangesRequested: "waiting for changes",
	store.StatusApproved: "approved", store.StatusRejected: "rejected", store.StatusCancelled: "cancelled",
}

func statusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

// event logs to the review and tells everyone with it open what changed.
func (s *Server) event(ctx context.Context, rv *store.Review, e store.Event, changed ...string) {
	e.ReviewID = rv.ID
	if e.UserID == 0 {
		e.UserID = actorID(ctx)
	}
	s.store.LogEvent(ctx, e)
	s.store.TouchReview(ctx, rv.ID)
	s.hub.notify(rv.ID, e.UserID, append(changed, "events")...)
}

// ---- reviews ----

func (s *Server) handleListReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.store.ListReviews(r.Context(), store.ReviewFilter{UserID: userFrom(r.Context()).ID, Status: q.Get("status"), Role: q.Get("role"), Query: q.Get("q")})
	if err != nil {
		s.fail(w, err, "list reviews")
		return
	}
	type row struct {
		*store.Review
		LiveNow []presence `json:"live_now"`
	}
	out := make([]row, len(list))
	for i, rv := range list {
		out[i] = row{rv, s.hub.people(rv.ID)}
	}
	writeJSON(w, http.StatusOK, out)
}

type reviewInput struct {
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	DueDate         string   `json:"due_date"`
	RequireResolved *bool    `json:"require_resolved"`
	People          []invite `json:"people"`
	// Source: set by apps that send documents here (see import.go).
	SourceApp   *string `json:"source_app"`
	SourceRef   *string `json:"source_ref"`
	SourceURL   *string `json:"source_url"`
	SourceLabel *string `json:"source_label"`
}

type invite struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var in reviewInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 200 {
		writeErr(w, http.StatusBadRequest, "a title (up to 200 characters) is required")
		return
	}
	if !validDate(in.DueDate) {
		writeErr(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
		return
	}
	rv := &store.Review{Title: in.Title, Description: strings.TrimSpace(in.Description), Kind: "pdf", OwnerID: u.ID, DueDate: in.DueDate, RequireResolved: true}
	if in.RequireResolved != nil {
		rv.RequireResolved = *in.RequireResolved
	}
	if err := s.store.CreateReview(r.Context(), rv); err != nil {
		s.fail(w, err, "create review")
		return
	}
	rv.MyRole = store.RoleOwner
	s.store.LogEvent(r.Context(), store.Event{ReviewID: rv.ID, UserID: u.ID, Kind: "review.created", Summary: rv.Title})
	for _, p := range in.People {
		if _, err := s.addPerson(r.Context(), rv, p); err != nil {
			s.log.Warn("create review: add person", "email", p.Email, "user", p.UserID, "err", err)
		}
	}
	out, err := s.store.GetReview(r.Context(), rv.ID, u.ID)
	if err != nil {
		s.fail(w, err, "get review")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	type out struct {
		*store.Review
		LiveNow []presence `json:"live_now"`
	}
	writeJSON(w, http.StatusOK, out{rv, s.hub.people(rv.ID)})
}

func (s *Server) handleUpdateReview(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	if !s.mustOwn(w, rv) {
		return
	}
	var in reviewInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	var changes []string
	if t := strings.TrimSpace(in.Title); t != "" && t != rv.Title {
		if len(t) > 200 {
			writeErr(w, http.StatusBadRequest, "the title is too long")
			return
		}
		changes = append(changes, "title")
		rv.Title = t
	}
	if d := strings.TrimSpace(in.Description); d != rv.Description {
		changes = append(changes, "description")
		rv.Description = d
	}
	if in.DueDate != rv.DueDate {
		if !validDate(in.DueDate) {
			writeErr(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
			return
		}
		changes = append(changes, "due date")
		rv.DueDate = in.DueDate
	}
	if in.RequireResolved != nil && *in.RequireResolved != rv.RequireResolved {
		changes = append(changes, "approval rule")
		rv.RequireResolved = *in.RequireResolved
	}
	if in.SourceApp != nil || in.SourceRef != nil || in.SourceURL != nil || in.SourceLabel != nil {
		changes = append(changes, "source")
		if in.SourceApp != nil {
			rv.SourceApp = strings.TrimSpace(*in.SourceApp)
		}
		if in.SourceRef != nil {
			rv.SourceRef = strings.TrimSpace(*in.SourceRef)
		}
		if in.SourceURL != nil {
			rv.SourceURL = strings.TrimSpace(*in.SourceURL)
		}
		if in.SourceLabel != nil {
			rv.SourceLabel = strings.TrimSpace(*in.SourceLabel)
		}
	}
	if len(changes) == 0 {
		writeJSON(w, http.StatusOK, rv)
		return
	}
	if err := s.store.UpdateReview(r.Context(), rv); err != nil {
		s.fail(w, err, "update review")
		return
	}
	s.event(r.Context(), rv, store.Event{Kind: "review.updated", Summary: "Changed " + strings.Join(changes, ", ")}, "review")
	writeJSON(w, http.StatusOK, rv)
}

func (s *Server) handleDeleteReview(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	if !s.mustOwn(w, rv) {
		return
	}
	if err := s.store.DeleteReview(r.Context(), rv.ID); err != nil {
		s.fail(w, err, "delete review")
		return
	}
	for _, v := range rv.Revisions {
		s.removeFileIfUnused(r.Context(), v.StoragePath)
	}
	_ = os.RemoveAll(filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10), "img"))
	_ = os.Remove(filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10)))
	s.hub.notify(rv.ID, actorID(r.Context()), "deleted")
	s.log.Info("review deleted", "id", rv.ID, "by", userFrom(r.Context()).Email)
	w.WriteHeader(http.StatusNoContent)
}

// handleReviewStatus moves the review along: start, resume, cancel, reopen,
// or complete (owner, when nobody is set to approve).
func (s *Server) handleReviewStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	var in struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !s.mustOwn(w, rv) {
		return
	}
	note := strings.TrimSpace(in.Note)
	var to, kind, summary string
	switch in.Action {
	case "start":
		if rv.Status != store.StatusDraft {
			writeErr(w, http.StatusConflict, "the review has already started")
			return
		}
		if rv.CurrentRevisionID == 0 {
			writeErr(w, http.StatusConflict, "upload the document first")
			return
		}
		to, kind, summary = store.StatusInReview, "review.started", "Started the review"
	case "resume":
		if rv.Status != store.StatusChangesRequested {
			writeErr(w, http.StatusConflict, "the review is not waiting for changes")
			return
		}
		to, kind, summary = store.StatusInReview, "review.resumed", "Back in review"
	case "cancel":
		if rv.Locked() {
			writeErr(w, http.StatusConflict, "the review is already "+statusLabel(rv.Status))
			return
		}
		to, kind, summary = store.StatusCancelled, "review.cancelled", "Cancelled the review"
	case "reopen":
		if !rv.Locked() {
			writeErr(w, http.StatusConflict, "the review is still open")
			return
		}
		to, kind, summary = store.StatusInReview, "review.reopened", "Reopened the review"
		if err := s.store.ResetVerdicts(ctx, rv.ID); err != nil {
			s.fail(w, err, "reset verdicts")
			return
		}
	case "complete":
		if len(approvers(rv)) > 0 {
			writeErr(w, http.StatusConflict, "the approvers decide this review")
			return
		}
		if rv.Status != store.StatusInReview {
			writeErr(w, http.StatusConflict, "the review is not in review")
			return
		}
		if rv.RequireResolved && rv.OpenIssues > 0 {
			writeErr(w, http.StatusConflict, fmt.Sprintf("%d issue(s) are still open; resolve or reject them first", rv.OpenIssues))
			return
		}
		to, kind, summary = store.StatusApproved, "review.approved", "Completed the review"
	default:
		writeErr(w, http.StatusBadRequest, "action must be start, resume, cancel, reopen or complete")
		return
	}
	if note != "" {
		summary += ": " + note
	}
	if err := s.store.SetReviewStatus(ctx, rv.ID, to, func(tx *sql.Tx) error {
		switch to {
		case store.StatusInReview:
			if in.Action == "start" {
				return s.queueEvent(ctx, tx, "review.review_started", fmt.Sprint(rv.ID), s.eventPayload(ctx, rv, map[string]any{"title": rv.Title}))
			}
		case store.StatusApproved, store.StatusCancelled:
			return s.queueEvent(ctx, tx, "review.review_decided", fmt.Sprint(rv.ID), s.eventPayload(ctx, rv, map[string]any{"title": rv.Title, "status": to}))
		}
		return nil
	}); err != nil {
		s.fail(w, err, "set status")
		return
	}
	s.event(ctx, rv, store.Event{Kind: kind, Summary: summary, RevisionID: rv.CurrentRevisionID}, "review", "people")
	s.tellStatus(ctx, rv, in.Action, note)
	out, _ := s.store.GetReview(ctx, rv.ID, actorID(ctx))
	writeJSON(w, http.StatusOK, out)
}

// handleVerdict records the caller's verdict. A reviewer's verdict closes
// their part; an approver's decides the review: approved once every approver
// approved, changes requested or rejected as soon as one says so.
func (s *Server) handleVerdict(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	u := userFrom(ctx)
	var in struct {
		Verdict string `json:"verdict"`
		Note    string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Verdict != "" && !store.OneOf(in.Verdict, store.Verdicts) {
		writeErr(w, http.StatusBadRequest, "verdict must be approved, changes, rejected or empty")
		return
	}
	if rv.Status != store.StatusInReview {
		writeErr(w, http.StatusConflict, "verdicts are given while the review is in review (it is "+statusLabel(rv.Status)+")")
		return
	}
	decider := canDecide(rv)
	if !decider && rv.MyRole != store.RoleReviewer {
		writeErr(w, http.StatusForbidden, "observers do not give a verdict")
		return
	}
	if decider && in.Verdict == "approved" && rv.RequireResolved && rv.OpenIssues > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf("%d issue(s) are still open; this review only approves once every issue is resolved or rejected", rv.OpenIssues))
		return
	}
	if err := s.store.SetVerdict(ctx, rv.ID, u.ID, in.Verdict, in.Note); err != nil {
		s.fail(w, err, "verdict")
		return
	}
	labels := map[string]string{"approved": "Approved", "changes": "Requested changes", "rejected": "Rejected", "": "Withdrew their verdict"}
	summary := labels[in.Verdict]
	if !decider && in.Verdict != "" {
		summary = "Finished reviewing: " + map[string]string{"approved": "looks good", "changes": "needs changes", "rejected": "should be rejected"}[in.Verdict]
	}
	if in.Note != "" {
		summary += " — " + in.Note
	}
	s.event(ctx, rv, store.Event{Kind: "verdict." + firstNonEmpty(in.Verdict, "withdrawn"), Summary: summary, RevisionID: rv.CurrentRevisionID}, "people")
	told := false // the status change below tells everyone, the owner included

	if decider {
		to := ""
		switch in.Verdict {
		case "changes":
			to = store.StatusChangesRequested
		case "rejected":
			to = store.StatusRejected
		case "approved":
			to = store.StatusApproved
			if rv.MyRole == store.RoleApprover {
				fresh, _ := s.store.ListParticipants(ctx, rv.ID)
				for _, p := range fresh {
					if p.Role == store.RoleApprover && p.Verdict != "approved" {
						to = "" // still waiting for another approver
					}
				}
			}
		}
		if to != "" {
			if err := s.store.SetReviewStatus(ctx, rv.ID, to, func(tx *sql.Tx) error {
				if to != store.StatusChangesRequested {
					return s.queueEvent(ctx, tx, "review.review_decided", fmt.Sprint(rv.ID), s.eventPayload(ctx, rv, map[string]any{"title": rv.Title, "status": to}))
				}
				return nil
			}); err != nil {
				s.fail(w, err, "set status")
				return
			}
			s.event(ctx, rv, store.Event{Kind: "review." + to, Summary: "The review is now " + statusLabel(to), RevisionID: rv.CurrentRevisionID}, "review")
			s.tellStatus(ctx, rv, map[string]string{store.StatusApproved: "approved", store.StatusChangesRequested: "changes", store.StatusRejected: "rejected"}[to], in.Note)
			told = true
		}
	}
	if !told {
		s.tellVerdict(ctx, rv, decider, in.Verdict, in.Note)
	}
	out, _ := s.store.GetReview(ctx, rv.ID, u.ID)
	writeJSON(w, http.StatusOK, out)
}

// eventPayload records the actor and review participants for consumers.
func (s *Server) eventPayload(ctx context.Context, rv *store.Review, p map[string]any) map[string]any {
	p["review_id"] = rv.ID
	p["user_email"] = userFrom(ctx).Email
	var emails []string
	for _, x := range rv.Participants {
		emails = append(emails, x.Email)
	}
	p["audience_emails"] = emails
	if s.cfg.BaseURL != "" {
		p["url"] = fmt.Sprintf("%s/reviews/%d", s.cfg.BaseURL, rv.ID)
	}
	return p
}

// ---- participants ----

// addPerson puts someone on the review by user id or email. An unknown email
// gets a member account without a password: they sign in through Pocket Core
// / single sign-on, or an admin sets a password.
func (s *Server) addPerson(ctx context.Context, rv *store.Review, in invite) (*store.User, error) {
	if !store.OneOf(in.Role, []string{store.RoleApprover, store.RoleReviewer, store.RoleObserver}) {
		return nil, errors.New("role must be approver, reviewer or observer")
	}
	var u *store.User
	var err error
	created := false
	if in.UserID != 0 {
		u, err = s.store.GetUser(ctx, in.UserID)
	} else {
		email := strings.ToLower(strings.TrimSpace(in.Email))
		if !strings.Contains(email, "@") {
			return nil, errors.New("a user or an email address is required")
		}
		u, err = s.store.GetUserByEmail(ctx, email)
		if errors.Is(err, store.ErrNotFound) {
			u = &store.User{Email: email, Name: strings.TrimSpace(in.Name), Role: "member"}
			err = s.store.CreateUser(ctx, u)
			created = true
		}
	}
	if err != nil {
		return nil, err
	}
	if p, err := s.store.GetParticipant(ctx, rv.ID, u.ID); err == nil && p.Role == store.RoleOwner {
		return nil, errors.New("that is the review's owner")
	}
	if err := s.store.AddParticipant(ctx, rv.ID, u.ID, in.Role, actorID(ctx)); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("Added %s as %s", displayName(u), in.Role)
	if created {
		summary += " (new account)"
		s.log.Info("account created for a review invite", "email", u.Email, "by", userFrom(ctx).Email)
	}
	s.event(ctx, rv, store.Event{Kind: "participant.added", Summary: summary}, "people")
	s.tellAdded(ctx, rv, u.ID, in.Role)
	return u, nil
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	if !s.mustOwn(w, rv) {
		return
	}
	var in invite
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if _, err := s.addPerson(r.Context(), rv, in); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, err, "add participant")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	list, _ := s.store.ListParticipants(r.Context(), rv.ID)
	writeJSON(w, http.StatusOK, list)
}

// handleUpdateParticipant changes someone's role. Making someone the owner
// hands the review over; the old owner stays on as a reviewer.
func (s *Server) handleUpdateParticipant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustOwn(w, rv) {
		return
	}
	uid := idParam(r, "uid")
	p, err := s.store.GetParticipant(ctx, rv.ID, uid)
	if err != nil {
		s.fail(w, err, "participant")
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := decode(r, &in); err != nil || !store.OneOf(in.Role, store.Roles) {
		writeErr(w, http.StatusBadRequest, "role must be owner, approver, reviewer or observer")
		return
	}
	if p.Role == in.Role {
		list, _ := s.store.ListParticipants(ctx, rv.ID)
		writeJSON(w, http.StatusOK, list)
		return
	}
	if p.Role == store.RoleOwner {
		writeErr(w, http.StatusBadRequest, "make someone else the owner instead")
		return
	}
	if in.Role == store.RoleOwner {
		me := userFrom(ctx)
		if err := s.store.SetParticipantRole(ctx, rv.ID, me.ID, store.RoleReviewer); err != nil {
			s.fail(w, err, "hand over")
			return
		}
		rv.OwnerID = uid
		if err := s.store.UpdateReview(ctx, rv); err != nil {
			s.fail(w, err, "hand over")
			return
		}
	}
	if err := s.store.SetParticipantRole(ctx, rv.ID, uid, in.Role); err != nil {
		s.fail(w, err, "set role")
		return
	}
	s.event(ctx, rv, store.Event{Kind: "participant.role", Summary: fmt.Sprintf("%s is now %s (was %s)", firstNonEmpty(p.Name, p.Email), in.Role, p.Role)}, "people", "review")
	list, _ := s.store.ListParticipants(ctx, rv.ID)
	writeJSON(w, http.StatusOK, list)
}

// handleRemoveParticipant takes someone off the review: the owner removes
// anyone else, anyone may leave.
func (s *Server) handleRemoveParticipant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	uid := idParam(r, "uid")
	self := uid == actorID(ctx)
	if !self && !s.mustOwn(w, rv) {
		return
	}
	p, err := s.store.GetParticipant(ctx, rv.ID, uid)
	if err != nil {
		s.fail(w, err, "participant")
		return
	}
	if p.Role == store.RoleOwner {
		writeErr(w, http.StatusBadRequest, "the owner cannot leave; hand the review over first")
		return
	}
	if err := s.store.RemoveParticipant(ctx, rv.ID, uid); err != nil {
		s.fail(w, err, "remove participant")
		return
	}
	summary := fmt.Sprintf("Removed %s", firstNonEmpty(p.Name, p.Email))
	if self {
		summary = "Left the review"
	}
	s.event(ctx, rv, store.Event{Kind: "participant.removed", Summary: summary}, "people")
	s.hub.kick(rv.ID, uid)
	w.WriteHeader(http.StatusNoContent)
}

// ---- revisions ----

// handleUploadRevision stores a new version of the document (multipart field
// "file", plus optional "page_count" measured by the browser and "note").
// A second or later revision clears everyone's verdict and, when changes
// were requested, puts the review back in review.
func (s *Server) handleUploadRevision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !s.mustOwn(w, rv) || !s.mustBeOpen(w, rv) {
		return
	}
	limit := int64(s.cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the upload failed or is larger than %d MB", s.cfg.MaxUploadMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "a file is required (field \"file\")")
		return
	}
	defer f.Close()
	head := make([]byte, 5)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("%PDF-")) {
		writeErr(w, http.StatusUnsupportedMediaType, "only PDF documents can be reviewed for now")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.fail(w, err, "read upload")
		return
	}
	dir := filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.fail(w, err, "store file")
		return
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		s.fail(w, err, "store file")
		return
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), f)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		s.fail(w, err, "store file")
		return
	}
	if size > limit {
		os.Remove(tmp.Name())
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the document is larger than %d MB", s.cfg.MaxUploadMB))
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	rel := filepath.Join(strconv.FormatInt(rv.ID, 10), sum+".pdf")
	if err := os.Rename(tmp.Name(), filepath.Join(s.cfg.FilesDir, rel)); err != nil {
		os.Remove(tmp.Name())
		s.fail(w, err, "store file")
		return
	}
	pages, _ := strconv.Atoi(r.FormValue("page_count"))
	if pages < 0 || pages > 100000 {
		pages = 0
	}
	name := filepath.Base(hdr.Filename)
	if name == "." || name == "/" {
		name = "document.pdf"
	}
	rev := &store.Revision{ReviewID: rv.ID, Kind: "pdf", Filename: name, Mime: "application/pdf", Size: size, SHA256: sum, StoragePath: rel,
		PageCount: pages, Note: strings.TrimSpace(r.FormValue("note")), UploadedBy: actorID(ctx)}
	if err := s.store.AddRevision(ctx, rev); err != nil {
		s.fail(w, err, "save revision")
		return
	}
	summary := fmt.Sprintf("Uploaded v%d: %s", rev.Number, name)
	if rev.Note != "" {
		summary += " — " + rev.Note
	}
	s.event(ctx, rv, store.Event{Kind: "revision.uploaded", RevisionID: rev.ID, Summary: summary}, "review")
	s.tellRevision(ctx, rv, rev)
	if rev.Number > 1 {
		_ = s.store.ResetVerdicts(ctx, rv.ID)
		if rv.Status == store.StatusChangesRequested {
			_ = s.store.SetReviewStatus(ctx, rv.ID, store.StatusInReview)
			s.event(ctx, rv, store.Event{Kind: "review.resumed", RevisionID: rev.ID, Summary: fmt.Sprintf("Back in review with v%d", rev.Number)}, "review", "people")
		}
	}
	writeJSON(w, http.StatusCreated, rev)
}

func (s *Server) handleRevisionFile(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	rev, err := s.store.GetRevision(r.Context(), rv.ID, idParam(r, "vid"))
	if err != nil {
		s.fail(w, err, "revision")
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.FilesDir, rev.StoragePath))
	if err != nil {
		s.fail(w, fmt.Errorf("the file of v%d is missing: %w", rev.Number, err), "open file")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", rev.Mime)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	disposition, name := "inline", rev.Filename
	if r.URL.Query().Get("download") != "" {
		disposition, name = "attachment", versionedFilename(rev.Filename, rev.Number)
	}
	w.Header().Set("Content-Disposition", contentDisposition(disposition, name))
	w.Header().Set("ETag", `"`+rev.SHA256+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}

// versionedFilename names a download after the uploaded file plus the revision
// number the app shows ("Contract.pdf", v2 → "Contract_v2.pdf").
func versionedFilename(name string, number int) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base = "document"
	}
	if ext == "" {
		ext = ".pdf"
	}
	return fmt.Sprintf("%s_v%d%s", base, number, ext)
}

// contentDisposition sets an ASCII fallback name plus the UTF-8 one (RFC 6266).
func contentDisposition(kind, name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	var enc strings.Builder
	for _, b := range []byte(name) {
		if b < 0x80 && (b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("!#$&+-.^_`|~", b) >= 0) {
			enc.WriteByte(b)
		} else {
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii, enc.String())
}

// handleSetPageCount lets the first viewer record how many pages the document has.
func (s *Server) handleSetPageCount(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	var in struct {
		PageCount int `json:"page_count"`
	}
	if err := decode(r, &in); err != nil || in.PageCount <= 0 || in.PageCount > 100000 {
		writeErr(w, http.StatusBadRequest, "page_count must be positive")
		return
	}
	if err := s.store.SetPageCount(r.Context(), rv.ID, idParam(r, "vid"), in.PageCount); err != nil {
		s.fail(w, err, "page count")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeFileIfUnused(ctx context.Context, rel string) {
	if rel == "" || s.store.StoragePathInUse(ctx, rel) {
		return
	}
	_ = os.Remove(filepath.Join(s.cfg.FilesDir, rel))
}

// ---- home ----

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	list, err := s.store.ListReviews(ctx, store.ReviewFilter{UserID: u.ID, Status: "open"})
	if err != nil {
		s.fail(w, err, "dashboard")
		return
	}
	type waiting struct {
		Review *store.Review `json:"review"`
		Why    string        `json:"why"` // review | approve | changes | draft | start
	}
	var todo []waiting
	toReview, toApprove := 0, 0
	for _, rv := range list {
		var me *store.Participant
		for i := range rv.Participants {
			if rv.Participants[i].UserID == u.ID {
				me = &rv.Participants[i]
			}
		}
		if me == nil {
			continue
		}
		why := ""
		switch {
		case rv.Status == store.StatusInReview && me.Role == store.RoleReviewer && me.State != "done":
			why = "review"
			toReview++
		case rv.Status == store.StatusInReview && canDecide(rv) && me.Verdict == "":
			why = "approve"
			toApprove++
		case rv.Status == store.StatusChangesRequested && me.Role == store.RoleOwner:
			why = "changes"
		case rv.Status == store.StatusDraft && me.Role == store.RoleOwner:
			why = "draft"
		}
		if why != "" {
			todo = append(todo, waiting{rv, why})
		}
	}
	if todo == nil {
		todo = []waiting{}
	}
	tasks, _ := s.store.MyTasks(ctx, u.ID, true)
	events, _ := s.store.ListEvents(ctx, store.EventFilter{UserID: u.ID, Limit: 15})
	weekAgo := time.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339Nano)
	week, _ := s.store.MyReviewSeconds(ctx, u.ID, weekAgo)
	type live struct {
		ReviewID int64      `json:"review_id"`
		Title    string     `json:"title"`
		People   []presence `json:"people"`
	}
	lives := []live{}
	for _, rv := range list {
		if p := s.hub.people(rv.ID); len(p) > 0 {
			lives = append(lives, live{rv.ID, rv.Title, p})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"waiting": todo, "to_review": toReview, "to_approve": toApprove, "open_reviews": len(list),
		"open_tasks": len(tasks), "tasks": tasks, "recent": events, "live": lives,
		"week_seconds": week, "week_text": report.Duration(week),
	})
}

func (s *Server) handleAllActivity(w http.ResponseWriter, r *http.Request) {
	ev, err := s.store.ListEvents(r.Context(), store.EventFilter{UserID: actorID(r.Context()), Before: queryInt(r, "before"), Limit: int(queryInt(r, "limit"))})
	if err != nil {
		s.fail(w, err, "activity")
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) handleMyTasks(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.MyTasks(r.Context(), actorID(r.Context()), r.URL.Query().Get("all") == "")
	if err != nil {
		s.fail(w, err, "tasks")
		return
	}
	writeJSON(w, http.StatusOK, t)
}
