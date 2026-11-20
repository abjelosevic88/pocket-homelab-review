package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// Moving reviews out and in (Pocket Move). A person takes the reviews they
// started: one zip with review.json (each review, its revisions, every
// comment with who wrote it, the pictures in comments, tasks and the people
// who were on it) and the documents and pictures next to it. Bringing the
// zip in adds each review once (same title and start time = already here),
// with the person as its owner; comments keep their writer when that person
// has an account on this box, and carry the writer's name otherwise.

const moveZipJSON = "review.json"

type moveDoc struct {
	App        string       `json:"app"`
	Version    string       `json:"version"`
	ExportedAt string       `json:"exported_at"`
	Reviews    []moveReview `json:"reviews"`
}

type moveReview struct {
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Kind            string         `json:"kind"`
	Status          string         `json:"status"`
	DueDate         string         `json:"due_date"`
	RequireResolved bool           `json:"require_resolved"`
	StartedAt       *string        `json:"started_at"`
	DecidedAt       *string        `json:"decided_at"`
	CreatedAt       string         `json:"created_at"`
	SourceApp       string         `json:"source_app,omitempty"`
	SourceURL       string         `json:"source_url,omitempty"`
	SourceLabel     string         `json:"source_label,omitempty"`
	People          []movePerson   `json:"people"`
	Revisions       []moveRevision `json:"revisions"`
	Images          []moveImage    `json:"images,omitempty"`
	Comments        []moveComment  `json:"comments"`
	Tasks           []moveTask     `json:"tasks,omitempty"`
}

type movePerson struct {
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	Verdict     string  `json:"verdict,omitempty"`
	VerdictNote string  `json:"verdict_note,omitempty"`
	VerdictAt   *string `json:"verdict_at,omitempty"`
}

type moveRevision struct {
	Number    int    `json:"number"`
	Kind      string `json:"kind"`
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	SourceURL string `json:"source_url,omitempty"`
	PageCount int    `json:"page_count"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
	File      string `json:"file,omitempty"` // the document inside the zip
}

type moveImage struct {
	ID        int64  `json:"id"`
	Mime      string `json:"mime"`
	CreatedAt string `json:"created_at"`
	File      string `json:"file"`
}

type moveComment struct {
	ID          int64         `json:"id"`
	ParentID    int64         `json:"parent_id,omitempty"`
	Revision    int           `json:"revision,omitempty"` // the revision number it was made on
	AuthorName  string        `json:"author_name"`
	AuthorEmail string        `json:"author_email,omitempty"`
	Kind        string        `json:"kind"`
	Severity    string        `json:"severity,omitempty"`
	Category    string        `json:"category,omitempty"`
	Body        string        `json:"body"`
	Page        int           `json:"page"`
	Anchor      *store.Anchor `json:"anchor,omitempty"`
	Images      []int64       `json:"images,omitempty"`
	Status      string        `json:"status"`
	StatusAt    *string       `json:"status_at,omitempty"`
	StatusNote  string        `json:"status_note,omitempty"`
	Deleted     bool          `json:"deleted,omitempty"`
	CreatedAt   string        `json:"created_at"`
	EditedAt    *string       `json:"edited_at,omitempty"`
}

type moveTask struct {
	Title    string  `json:"title"`
	Notes    string  `json:"notes,omitempty"`
	Assignee string  `json:"assignee,omitempty"`
	DueDate  string  `json:"due_date,omitempty"`
	Status   string  `json:"status"`
	Created  string  `json:"created_at"`
	DoneAt   *string `json:"done_at,omitempty"`
}

// ownedReviews are the reviews the person started (and still owns).
func (s *Server) ownedReviews(r *http.Request, uid int64) ([]*store.Review, error) {
	list, err := s.store.ListReviews(r.Context(), store.ReviewFilter{UserID: uid, Role: store.RoleOwner})
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, rv := range list {
		if rv.OwnerID == uid {
			out = append(out, rv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// handleExportZip writes the person's own reviews as one zip.
func (s *Server) handleExportZip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	list, err := s.ownedReviews(r, uid)
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	emails := map[int64]string{}
	if users, err := s.store.ListUsers(ctx); err == nil {
		for _, u := range users {
			emails[u.ID] = u.Email
		}
	}
	doc := moveDoc{App: "pocket-review", Version: Version, ExportedAt: store.Now(), Reviews: []moveReview{}}
	files := map[string]string{} // name in the zip → path on disk
	for i, rv := range list {
		dir := fmt.Sprintf("reviews/%03d", i+1)
		mr := moveReview{Title: rv.Title, Description: rv.Description, Kind: rv.Kind, Status: rv.Status, DueDate: rv.DueDate, RequireResolved: rv.RequireResolved,
			StartedAt: rv.StartedAt, DecidedAt: rv.DecidedAt, CreatedAt: rv.CreatedAt, SourceApp: rv.SourceApp, SourceURL: rv.SourceURL, SourceLabel: rv.SourceLabel,
			People: []movePerson{}, Revisions: []moveRevision{}, Comments: []moveComment{}}
		for _, p := range rv.Participants {
			mr.People = append(mr.People, movePerson{Name: p.Name, Email: p.Email, Role: p.Role, Verdict: p.Verdict, VerdictNote: p.VerdictNote, VerdictAt: p.VerdictAt})
		}
		revNumber := map[int64]int{}
		for _, v := range rv.Revisions {
			revNumber[v.ID] = v.Number
			mv := moveRevision{Number: v.Number, Kind: v.Kind, Filename: v.Filename, Mime: v.Mime, SourceURL: v.SourceURL, PageCount: v.PageCount, Note: v.Note, CreatedAt: v.CreatedAt}
			if v.StoragePath != "" {
				mv.File = fmt.Sprintf("%s/v%d%s", dir, v.Number, firstNonEmpty(filepath.Ext(v.Filename), ".pdf"))
				files[mv.File] = filepath.Join(s.cfg.FilesDir, v.StoragePath)
			}
			mr.Revisions = append(mr.Revisions, mv)
		}
		threads, err := s.store.ListThreads(ctx, rv.ID)
		if err != nil {
			s.fail(w, err, "export")
			return
		}
		pictures := map[int64]bool{}
		add := func(c store.Comment) {
			mc := moveComment{ID: c.ID, ParentID: c.ParentID, Revision: revNumber[c.RevisionID], AuthorName: c.AuthorName, AuthorEmail: emails[c.AuthorID],
				Kind: c.Kind, Severity: c.Severity, Category: c.Category, Body: c.Body, Page: c.Page, Anchor: c.Anchor, Images: c.Images, Status: c.Status,
				StatusAt: c.StatusAt, StatusNote: c.StatusNote, Deleted: c.Deleted, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt}
			for _, id := range c.Images {
				pictures[id] = true
			}
			mr.Comments = append(mr.Comments, mc)
		}
		for _, t := range threads {
			add(t)
			for _, rep := range t.Replies {
				add(rep)
			}
		}
		ids := make([]int64, 0, len(pictures))
		for id := range pictures {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			im, err := s.store.GetImage(ctx, rv.ID, id)
			if err != nil {
				continue
			}
			mi := moveImage{ID: im.ID, Mime: im.Mime, CreatedAt: im.CreatedAt, File: fmt.Sprintf("%s/pictures/%d%s", dir, im.ID, imageExt[im.Mime])}
			files[mi.File] = filepath.Join(s.cfg.FilesDir, im.StoragePath)
			mr.Images = append(mr.Images, mi)
		}
		if tasks, err := s.store.ListTasks(ctx, rv.ID); err == nil {
			for _, t := range tasks {
				mr.Tasks = append(mr.Tasks, moveTask{Title: t.Title, Notes: t.Notes, Assignee: t.AssigneeName, DueDate: t.DueDate, Status: t.Status, Created: t.CreatedAt, DoneAt: t.DoneAt})
			}
		}
		doc.Reviews = append(doc.Reviews, mr)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", "pocket-review-"+time.Now().Format("2006-01-02")+".zip"))
	zw := zip.NewWriter(w)
	jw, _ := zw.Create(moveZipJSON)
	enc := json.NewEncoder(jw)
	enc.SetIndent("", " ")
	_ = enc.Encode(doc)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := os.Open(files[n])
		if err != nil {
			s.log.Warn("export: a file is missing", "file", files[n], "err", err)
			continue
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		if err == nil {
			_, _ = io.Copy(fw, f)
		}
		f.Close()
	}
	_ = zw.Close()
}

// handleImportZip brings reviews from an export of this app (multipart field
// "file", or the zip as the body). It only adds.
func (s *Server) handleImportZip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	limit := s.moveImportLimit()
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	var ra io.ReaderAt
	var size int64
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "the upload failed or is too large")
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "a Pocket Review export is required (field \"file\")")
			return
		}
		defer f.Close()
		ra, size = f, hdr.Size
	} else {
		tmp, err := os.CreateTemp("", "review-import-*.zip")
		if err != nil {
			s.fail(w, err, "import")
			return
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if size, err = io.Copy(tmp, r.Body); err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "the upload failed or is too large")
			return
		}
		ra = tmp
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "that is not a Pocket Review export (a zip)")
		return
	}
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	var doc moveDoc
	jf := entries[moveZipJSON]
	if jf == nil || readZipJSON(jf, &doc) != nil || doc.App != "pocket-review" {
		writeErr(w, http.StatusBadRequest, "that is not a Pocket Review export")
		return
	}
	counts := map[string]int{"reviews": 0, "revisions": 0, "comments": 0, "pictures": 0, "already": 0, "skipped": 0}
	for _, mr := range doc.Reviews {
		mr.Title = strings.TrimSpace(mr.Title)
		if mr.Title == "" || mr.CreatedAt == "" {
			counts["skipped"]++
			continue
		}
		if _, ok := s.store.FindImportedReview(ctx, u.ID, mr.Title, mr.CreatedAt); ok {
			counts["already"]++
			continue
		}
		n, err := s.importReview(r, u, mr, entries)
		if err != nil {
			s.log.Warn("import review", "title", mr.Title, "err", err)
			counts["skipped"]++
			continue
		}
		counts["reviews"]++
		counts["revisions"] += n["revisions"]
		counts["comments"] += n["comments"]
		counts["pictures"] += n["pictures"]
	}
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) moveImportLimit() int64 {
	return int64(s.cfg.MaxUploadMB) * 20 << 20 // a zip of several documents
}

func readZipJSON(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(v)
}

// importReview adds one review with its documents, pictures and comments; a
// review that cannot be brought whole is taken out again.
func (s *Server) importReview(r *http.Request, u *store.User, mr moveReview, entries map[string]*zip.File) (counts map[string]int, err error) {
	ctx := r.Context()
	counts = map[string]int{}
	status := mr.Status
	switch status {
	case store.StatusDraft, store.StatusInReview, store.StatusChangesRequested, store.StatusApproved, store.StatusRejected, store.StatusCancelled:
	default:
		status = store.StatusDraft
	}
	if len(mr.Title) > 200 {
		mr.Title = mr.Title[:200]
	}
	rv := &store.Review{Title: mr.Title, Description: mr.Description, Kind: firstNonEmpty(mr.Kind, "pdf"), OwnerID: u.ID, Status: status, DueDate: mr.DueDate,
		RequireResolved: mr.RequireResolved, StartedAt: mr.StartedAt, DecidedAt: mr.DecidedAt, CreatedAt: mr.CreatedAt,
		SourceApp: mr.SourceApp, SourceURL: mr.SourceURL, SourceLabel: mr.SourceLabel}
	if !validDate(rv.DueDate) {
		rv.DueDate = ""
	}
	if err := s.store.InsertImportedReview(ctx, rv); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10))
	defer func() {
		if err != nil {
			_ = s.store.DeleteReview(ctx, rv.ID)
			_ = os.RemoveAll(dir)
		}
	}()
	limit := int64(s.cfg.MaxUploadMB) << 20
	revID := map[int]int64{}
	for _, mv := range mr.Revisions {
		f := entries[mv.File]
		if f == nil {
			continue
		}
		data, err := readZipFile(f, limit)
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return nil, errNotPDF
		}
		sum, rel, err := s.writeMoved(dir, "", data, ".pdf")
		if err != nil {
			return nil, err
		}
		v := &store.Revision{ReviewID: rv.ID, Number: mv.Number, Kind: "pdf", Filename: filepath.Base(firstNonEmpty(mv.Filename, "document.pdf")), Mime: "application/pdf",
			Size: int64(len(data)), SHA256: sum, StoragePath: rel, SourceURL: mv.SourceURL, PageCount: mv.PageCount, Note: mv.Note, UploadedBy: u.ID, CreatedAt: mv.CreatedAt}
		if err := s.store.InsertImportedRevision(ctx, v); err != nil {
			return nil, err
		}
		revID[mv.Number] = v.ID
		counts["revisions"]++
	}
	picID := map[int64]int64{}
	for _, mi := range mr.Images {
		f := entries[mi.File]
		ext, ok := imageExt[mi.Mime]
		if f == nil || !ok {
			continue
		}
		data, err := readZipFile(f, int64(maxImageMB)<<20)
		if err != nil {
			return nil, err
		}
		if http.DetectContentType(data) != mi.Mime {
			continue
		}
		sum, rel, err := s.writeMoved(dir, "img", data, ext)
		if err != nil {
			return nil, err
		}
		im := &store.Image{ReviewID: rv.ID, Mime: mi.Mime, Size: int64(len(data)), SHA256: sum, StoragePath: rel, UploadedBy: u.ID, CreatedAt: mi.CreatedAt}
		if err := s.store.InsertImportedImage(ctx, im); err != nil {
			return nil, err
		}
		picID[mi.ID] = im.ID
		counts["pictures"]++
	}
	// writers who have an account here keep their comments; the others' carry their name
	writer := map[string]int64{}
	commentID := map[int64]int64{}
	sort.Slice(mr.Comments, func(i, j int) bool { return mr.Comments[i].ID < mr.Comments[j].ID })
	for _, mc := range mr.Comments {
		parent := int64(0)
		if mc.ParentID != 0 {
			p, ok := commentID[mc.ParentID]
			if !ok {
				continue
			}
			parent = p
		}
		author := u.ID
		if email := strings.ToLower(strings.TrimSpace(mc.AuthorEmail)); email != "" {
			id, seen := writer[email]
			if !seen {
				if wu, err := s.store.GetUserByEmail(ctx, email); err == nil {
					id = wu.ID
				}
				writer[email] = id
			}
			if id != 0 {
				author = id
			}
		}
		body := mc.Body
		if author == u.ID && !strings.EqualFold(mc.AuthorEmail, u.Email) && mc.AuthorName != "" && !mc.Deleted {
			body = mc.AuthorName + ": " + body
		}
		images := []int64{}
		for _, id := range mc.Images {
			if n, ok := picID[id]; ok {
				images = append(images, n)
			}
		}
		kind := mc.Kind
		if kind != "comment" && kind != "question" && kind != "issue" {
			kind = "comment"
		}
		c := &store.Comment{ReviewID: rv.ID, RevisionID: revID[mc.Revision], ParentID: parent, AuthorID: author, Kind: kind, Severity: mc.Severity, Category: mc.Category,
			Body: body, Page: mc.Page, Anchor: mc.Anchor, Images: images, Status: firstNonEmpty(mc.Status, "open"), StatusAt: mc.StatusAt, StatusNote: mc.StatusNote,
			Deleted: mc.Deleted, CreatedAt: mc.CreatedAt, EditedAt: mc.EditedAt}
		if err := s.store.InsertImportedComment(ctx, c); err != nil {
			return nil, err
		}
		commentID[mc.ID] = c.ID
		counts["comments"]++
	}
	s.store.LogEvent(ctx, store.Event{ReviewID: rv.ID, UserID: u.ID, Kind: "review.imported", Summary: rv.Title + " (brought from another box)"})
	return counts, nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s is larger than %d MB", f.Name, limit>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", f.Name, limit>>20)
	}
	return data, nil
}

// writeMoved stores a brought-in file under the review's folder by its hash.
func (s *Server) writeMoved(dir, sub string, data []byte, ext string) (sum, rel string, err error) {
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	full := filepath.Join(dir, sub)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(full, sum+ext), data, 0o644); err != nil {
		return "", "", err
	}
	rel, _ = filepath.Rel(s.cfg.FilesDir, filepath.Join(full, sum+ext))
	return sum, rel, nil
}
