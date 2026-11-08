package server

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// Documents sent from other Pocket apps (Office, Documents) arrive here with
// the PDF and where it came from. A source reference the sending app keeps
// stable makes a second send a new revision of the same review; the lookup
// endpoint is what those apps call to show an "in review" badge.

// storeRevision writes a PDF into FILES_DIR and records it as the review's
// newest revision, with the same side effects as an upload in the UI.
func (s *Server) storeRevision(r *http.Request, rv *store.Review, f io.Reader, filename, note string, pages int) (*store.Revision, error) {
	ctx := r.Context()
	limit := int64(s.cfg.MaxUploadMB) << 20
	head := make([]byte, 5)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("%PDF-")) {
		return nil, errNotPDF
	}
	dir := filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	if _, err := w.Write(head); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	size, err := io.Copy(w, io.LimitReader(f, limit+1))
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	size += int64(len(head))
	if size > limit {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("the document is larger than %d MB", s.cfg.MaxUploadMB)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	rel := filepath.Join(strconv.FormatInt(rv.ID, 10), sum+".pdf")
	if err := os.Rename(tmp.Name(), filepath.Join(s.cfg.FilesDir, rel)); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if pages < 0 || pages > 100000 {
		pages = 0
	}
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "document.pdf"
	}
	rev := &store.Revision{ReviewID: rv.ID, Kind: "pdf", Filename: name, Mime: "application/pdf", Size: size, SHA256: sum, StoragePath: rel,
		PageCount: pages, Note: strings.TrimSpace(note), UploadedBy: actorID(ctx)}
	if err := s.store.AddRevision(ctx, rev); err != nil {
		return nil, err
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
	return rev, nil
}

var errNotPDF = fmt.Errorf("only PDF documents can be reviewed")

// handleImportReview takes a PDF plus where it came from (multipart: file,
// title, description, due_date, note, page_count, source_app, source_ref,
// source_url, source_label, mode=new|revision, start=1). With a source
// reference that already has an unfinished review of the caller's, the file
// becomes a new revision of it unless mode=new.
func (s *Server) handleImportReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	limit := int64(s.cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the upload failed or is larger than %d MB", s.cfg.MaxUploadMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "a PDF is required (field \"file\")")
		return
	}
	defer f.Close()
	v := r.FormValue
	ref, app := strings.TrimSpace(v("source_ref")), strings.TrimSpace(v("source_app"))
	if len(ref) > 300 || len(app) > 40 {
		writeErr(w, http.StatusBadRequest, "source_ref/source_app too long")
		return
	}
	pages, _ := strconv.Atoi(v("page_count"))
	created := false
	var rv *store.Review
	if ref != "" && v("mode") != "new" {
		if found, err := s.store.FindOpenBySource(ctx, u.ID, ref); err == nil {
			rv = found
		}
	}
	if rv == nil {
		title := strings.TrimSpace(v("title"))
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(hdr.Filename), filepath.Ext(hdr.Filename))
		}
		if title == "" || len(title) > 200 {
			writeErr(w, http.StatusBadRequest, "a title (up to 200 characters) is required")
			return
		}
		if !validDate(v("due_date")) {
			writeErr(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
			return
		}
		rv = &store.Review{Title: title, Description: strings.TrimSpace(v("description")), Kind: "pdf", OwnerID: u.ID, DueDate: v("due_date"), RequireResolved: true,
			SourceApp: app, SourceRef: ref, SourceURL: strings.TrimSpace(v("source_url")), SourceLabel: strings.TrimSpace(v("source_label"))}
		if err := s.store.CreateReview(ctx, rv); err != nil {
			s.fail(w, err, "create review")
			return
		}
		rv.MyRole = store.RoleOwner
		s.store.LogEvent(ctx, store.Event{ReviewID: rv.ID, UserID: u.ID, Kind: "review.created", Summary: rv.Title + " (from " + firstNonEmpty(app, "another app") + ")"})
		created = true
	} else {
		// keep the way back fresh (a draft that was saved somewhere, a renamed file)
		if su, sl := strings.TrimSpace(v("source_url")), strings.TrimSpace(v("source_label")); (su != "" && su != rv.SourceURL) || (sl != "" && sl != rv.SourceLabel) {
			rv.SourceURL, rv.SourceLabel = firstNonEmpty(su, rv.SourceURL), firstNonEmpty(sl, rv.SourceLabel)
			_ = s.store.UpdateReview(ctx, rv)
		}
		if rv.Locked() {
			writeErr(w, http.StatusConflict, "that review is finished; send with mode=new to start another")
			return
		}
	}
	rev, err := s.storeRevision(r, rv, f, hdr.Filename, v("note"), pages)
	if err != nil {
		if err == errNotPDF {
			writeErr(w, http.StatusUnsupportedMediaType, err.Error())
		} else {
			s.fail(w, err, "store file")
		}
		return
	}
	rv.CurrentRevisionID = rev.ID
	if v("start") == "1" && rv.Status == store.StatusDraft {
		if err := s.store.SetReviewStatus(ctx, rv.ID, store.StatusInReview, func(tx *sql.Tx) error {
			return s.queueEvent(ctx, tx, "review.review_started", fmt.Sprint(rv.ID), s.eventPayload(ctx, rv, map[string]any{"title": rv.Title}))
		}); err == nil {
			s.event(ctx, rv, store.Event{Kind: "review.started", Summary: "Started the review", RevisionID: rev.ID}, "review", "people")
			s.tellStatus(ctx, rv, "start", "")
		}
	}
	out, err := s.store.GetReview(ctx, rv.ID, u.ID)
	if err != nil {
		s.fail(w, err, "get review")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"review": out, "revision": rev, "created": created, "url": s.reviewURL(rv.ID)})
}

func (s *Server) reviewURL(id int64) string {
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/reviews/" + strconv.FormatInt(id, 10)
}

// handleLookup answers "is this file in review?" for a list of source
// references (?ref=a&ref=b), only among the caller's reviews.
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	refs := r.URL.Query()["ref"]
	if len(refs) > 500 {
		refs = refs[:500]
	}
	list, err := s.store.LookupBySource(r.Context(), userFrom(r.Context()).ID, refs)
	if err != nil {
		s.fail(w, err, "lookup")
		return
	}
	type hit struct {
		store.SourceSummary
		URL string `json:"url"`
	}
	out := map[string]hit{}
	for _, x := range list {
		if _, seen := out[x.SourceRef]; seen {
			continue // newest first: keep it
		}
		out[x.SourceRef] = hit{x, s.reviewURL(x.ID)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": out, "base_url": strings.TrimRight(s.cfg.BaseURL, "/")})
}
