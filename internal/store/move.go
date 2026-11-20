package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Bringing reviews in from another box (Pocket Move). These keep what the
// export says (numbers, dates, states) instead of starting fresh like the
// usual create calls do.

// FindImportedReview finds a review the person owns with this title and
// creation time: a review already brought in once.
func (s *Store) FindImportedReview(ctx context.Context, ownerID int64, title, createdAt string) (int64, bool) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM reviews WHERE owner_id = ? AND title = ? AND created_at = ? LIMIT 1`, ownerID, title, createdAt).Scan(&id)
	return id, err == nil
}

// InsertImportedReview adds a review with its own status and dates, the
// person as its owner (and only participant).
func (s *Store) InsertImportedReview(ctx context.Context, r *Review) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	if r.CreatedAt == "" {
		r.CreatedAt = now
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO reviews (title, description, kind, owner_id, status, due_date, require_resolved, started_at, decided_at, created_at, updated_at,
		source_app, source_ref, source_url, source_label) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		r.Title, r.Description, r.Kind, r.OwnerID, r.Status, r.DueDate, boolInt(r.RequireResolved), r.StartedAt, r.DecidedAt, r.CreatedAt, now,
		r.SourceApp, r.SourceURL, r.SourceLabel)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO participants (review_id, user_id, role, state, added_by, added_at) VALUES (?, ?, ?, 'reviewing', ?, ?)`,
		r.ID, r.OwnerID, RoleOwner, r.OwnerID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertImportedRevision adds a revision with its own number and date and
// makes the newest one current.
func (s *Store) InsertImportedRevision(ctx context.Context, rv *Revision) error {
	if rv.CreatedAt == "" {
		rv.CreatedAt = Now()
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO revisions (review_id, number, kind, filename, mime, size, sha256, storage_path, source_url, page_count, note, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rv.ReviewID, rv.Number, rv.Kind, rv.Filename, rv.Mime, rv.Size, rv.SHA256, rv.StoragePath, rv.SourceURL,
		rv.PageCount, rv.Note, nullID(rv.UploadedBy), rv.CreatedAt)
	if err != nil {
		return err
	}
	rv.ID, _ = res.LastInsertId()
	_, err = s.DB.ExecContext(ctx, `UPDATE reviews SET current_revision_id = (SELECT id FROM revisions WHERE review_id = ? ORDER BY number DESC LIMIT 1) WHERE id = ?`,
		rv.ReviewID, rv.ReviewID)
	return err
}

// InsertImportedImage adds a comment picture's record.
func (s *Store) InsertImportedImage(ctx context.Context, im *Image) error {
	if im.CreatedAt == "" {
		im.CreatedAt = Now()
	}
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM images WHERE review_id = ? AND sha256 = ?`, im.ReviewID, im.SHA256).Scan(&im.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO images (review_id, mime, size, sha256, storage_path, uploaded_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		im.ReviewID, im.Mime, im.Size, im.SHA256, im.StoragePath, nullID(im.UploadedBy), im.CreatedAt)
	if err != nil {
		return err
	}
	im.ID, _ = res.LastInsertId()
	return nil
}

// InsertImportedComment adds a comment with its own state and dates.
func (s *Store) InsertImportedComment(ctx context.Context, c *Comment) error {
	anchor := ""
	if c.Anchor != nil && (len(c.Anchor.Rects) > 0 || c.Anchor.Quote != "") {
		b, _ := json.Marshal(c.Anchor)
		anchor = string(b)
	}
	if c.CreatedAt == "" {
		c.CreatedAt = Now()
	}
	if c.Status == "" {
		c.Status = "open"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO comments (review_id, revision_id, parent_id, author_id, kind, severity, category, body, page, anchor, images,
		status, status_at, status_note, deleted, created_at, edited_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ReviewID, nullID(c.RevisionID), nullID(c.ParentID), nullID(c.AuthorID), c.Kind, c.Severity, c.Category, c.Body, c.Page, anchor, imagesJSON(c.Images),
		c.Status, c.StatusAt, c.StatusNote, boolInt(c.Deleted), c.CreatedAt, c.EditedAt)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}
