package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/abjelosevic88/pocket-homelab-review/internal/store"
)

// Pictures in comments: pasted screenshots and photos, up to maxImageMB each
// and maxCommentImages per comment.
const (
	maxImageMB       = 15
	maxCommentImages = 10
)

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// handleUploadImage stores a picture (multipart field "file") for a comment
// that is about to be posted, and returns its id.
func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rv := reviewFrom(ctx)
	if !canComment(rv) {
		writeErr(w, http.StatusForbidden, "observers can read but not comment")
		return
	}
	if !s.mustTakeComments(w, rv) {
		return
	}
	limit := int64(maxImageMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the picture failed to upload or is larger than %d MB", maxImageMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "a picture is required (field \"file\")")
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mime := http.DetectContentType(head[:n])
	ext, ok := imageExt[mime]
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "only PNG, JPEG, GIF and WebP pictures")
		return
	}
	dir := filepath.Join(s.cfg.FilesDir, strconv.FormatInt(rv.ID, 10), "img")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.fail(w, err, "store picture")
		return
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		s.fail(w, err, "store picture")
		return
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), io.MultiReader(bytes.NewReader(head[:n]), f))
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		s.fail(w, err, "store picture")
		return
	}
	if size > limit {
		os.Remove(tmp.Name())
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the picture is larger than %d MB", maxImageMB))
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	rel := filepath.Join(strconv.FormatInt(rv.ID, 10), "img", sum+ext)
	if err := os.Rename(tmp.Name(), filepath.Join(s.cfg.FilesDir, rel)); err != nil {
		os.Remove(tmp.Name())
		s.fail(w, err, "store picture")
		return
	}
	im := &store.Image{ReviewID: rv.ID, Mime: mime, Size: size, SHA256: sum, StoragePath: rel, UploadedBy: actorID(ctx)}
	if err := s.store.AddImage(ctx, im); err != nil {
		s.fail(w, err, "save picture")
		return
	}
	writeJSON(w, http.StatusCreated, im)
}

func (s *Server) handleImageFile(w http.ResponseWriter, r *http.Request) {
	rv := reviewFrom(r.Context())
	im, err := s.store.GetImage(r.Context(), rv.ID, idParam(r, "iid"))
	if err != nil {
		s.fail(w, err, "picture")
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.FilesDir, im.StoragePath))
	if err != nil {
		s.fail(w, fmt.Errorf("the picture is missing: %w", err), "open picture")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", im.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+im.SHA256+`"`)
	w.Header().Set("Content-Disposition", contentDisposition("inline", fmt.Sprintf("picture-%d%s", im.ID, imageExt[im.Mime])))
	http.ServeContent(w, r, "", time.Time{}, f)
}

// cleanImages drops duplicate ids and checks the rest are this review's
// pictures; it writes the error and returns false when they are not.
func (s *Server) cleanImages(w http.ResponseWriter, r *http.Request, rv *store.Review, ids []int64) ([]int64, bool) {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > maxCommentImages {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("up to %d pictures per comment", maxCommentImages))
		return nil, false
	}
	ok, err := s.store.ImagesBelong(r.Context(), rv.ID, out)
	if err != nil {
		s.fail(w, err, "pictures")
		return nil, false
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "a picture does not belong to this review")
		return nil, false
	}
	return out, true
}

// commentText is a comment's words for logs and events; a picture-only
// comment reads "(picture)".
func commentText(c *store.Comment) string {
	if c.Body != "" || len(c.Images) == 0 {
		return c.Body
	}
	if len(c.Images) == 1 {
		return "(picture)"
	}
	return fmt.Sprintf("(%d pictures)", len(c.Images))
}
