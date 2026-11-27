package store

import (
	"context"
	"database/sql"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"strings"
)

// A review is visible to the people on it and to nobody else, admins included.
const reviewCols = `r.id, r.title, r.description, r.kind, COALESCE(r.owner_id, 0), r.status, r.due_date, r.require_resolved,
	COALESCE(r.current_revision_id, 0), r.started_at, r.decided_at, r.created_at, r.updated_at, r.source_app, r.source_ref, r.source_url, r.source_label`

func scanReview(row interface{ Scan(...any) error }, extra ...any) (*Review, error) {
	var r Review
	var req int
	dst := append([]any{&r.ID, &r.Title, &r.Description, &r.Kind, &r.OwnerID, &r.Status, &r.DueDate, &req,
		&r.CurrentRevisionID, &r.StartedAt, &r.DecidedAt, &r.CreatedAt, &r.UpdatedAt, &r.SourceApp, &r.SourceRef, &r.SourceURL, &r.SourceLabel}, extra...)
	if err := row.Scan(dst...); err != nil {
		return nil, notFound(err)
	}
	r.RequireResolved = req == 1
	return &r, nil
}

// CreateReview inserts a review and makes the creator its owner.
func (s *Store) CreateReview(ctx context.Context, r *Review) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO reviews (title, description, kind, owner_id, status, due_date, require_resolved, created_at, updated_at, source_app, source_ref, source_url, source_label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Title, r.Description, r.Kind, r.OwnerID, StatusDraft, r.DueDate, boolInt(r.RequireResolved), now, now, r.SourceApp, r.SourceRef, r.SourceURL, r.SourceLabel)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO participants (review_id, user_id, role, state, added_by, added_at) VALUES (?, ?, ?, 'reviewing', ?, ?)`,
		r.ID, r.OwnerID, RoleOwner, r.OwnerID, now); err != nil {
		return err
	}
	r.Status, r.CreatedAt, r.UpdatedAt = StatusDraft, now, now
	return tx.Commit()
}

// UpdateReview saves title, description, due date, approval rule and owner.
func (s *Store) UpdateReview(ctx context.Context, r *Review) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE reviews SET title = ?, description = ?, due_date = ?, require_resolved = ?, owner_id = ?, updated_at = ?,
		source_app = ?, source_ref = ?, source_url = ?, source_label = ? WHERE id = ?`,
		r.Title, r.Description, r.DueDate, boolInt(r.RequireResolved), r.OwnerID, Now(), r.SourceApp, r.SourceRef, r.SourceURL, r.SourceLabel, r.ID)
	return err
}

// FindOpenBySource returns the newest unfinished review the person owns for a
// source reference, so a document sent again becomes a new revision.
func (s *Store) FindOpenBySource(ctx context.Context, ownerID int64, ref string) (*Review, error) {
	var role string
	r, err := scanReview(s.DB.QueryRowContext(ctx, `SELECT `+reviewCols+`, p.role FROM reviews r
		JOIN participants p ON p.review_id = r.id AND p.user_id = ? AND p.role = 'owner'
		WHERE r.source_ref = ? AND r.status NOT IN ('approved', 'rejected', 'cancelled') ORDER BY r.created_at DESC LIMIT 1`, ownerID, ref), &role)
	if err != nil {
		return nil, err
	}
	r.MyRole = role
	return r, s.fillReview(ctx, r)
}

// SourceSummary is what a sending app shows as a badge next to its file.
type SourceSummary struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	SourceRef   string `json:"source_ref"`
	Revisions   int    `json:"revisions"`
	OpenThreads int    `json:"open_threads"`
	OpenIssues  int    `json:"open_issues"`
	UpdatedAt   string `json:"updated_at"`
	MyRole      string `json:"my_role"`
}

// LookupBySource lists the reviews a person is on whose source reference is
// one of refs, newest first (an app asks for a whole folder at once).
func (s *Store) LookupBySource(ctx context.Context, userID int64, refs []string) ([]SourceSummary, error) {
	if len(refs) == 0 {
		return []SourceSummary{}, nil
	}
	args := []any{userID}
	ph := make([]string, len(refs))
	for i, ref := range refs {
		ph[i] = "?"
		args = append(args, ref)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.title, r.status, r.source_ref, r.updated_at, p.role,
		(SELECT COUNT(1) FROM revisions v WHERE v.review_id = r.id),
		(SELECT COUNT(1) FROM comments c WHERE c.review_id = r.id AND c.parent_id IS NULL AND c.deleted = 0 AND c.status = 'open'),
		(SELECT COUNT(1) FROM comments c WHERE c.review_id = r.id AND c.parent_id IS NULL AND c.deleted = 0 AND c.kind = 'issue' AND c.status = 'open')
		FROM reviews r JOIN participants p ON p.review_id = r.id AND p.user_id = ?
		WHERE r.source_ref IN (`+strings.Join(ph, ",")+`) ORDER BY r.updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceSummary{}
	for rows.Next() {
		var x SourceSummary
		if err := rows.Scan(&x.ID, &x.Title, &x.Status, &x.SourceRef, &x.UpdatedAt, &x.MyRole, &x.Revisions, &x.OpenThreads, &x.OpenIssues); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetReviewStatus moves a review to a new status, stamping when it started
// and when it was decided.
func (s *Store) SetReviewStatus(ctx context.Context, id int64, status string, after ...core.CommitHook) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	q := `UPDATE reviews SET status = ?, updated_at = ?`
	args := []any{status, now}
	switch status {
	case StatusInReview:
		q += `, started_at = COALESCE(started_at, ?), decided_at = NULL`
		args = append(args, now)
	case StatusApproved, StatusRejected, StatusCancelled:
		q += `, decided_at = ?`
		args = append(args, now)
	default:
		q += `, decided_at = NULL`
	}
	_, err = tx.ExecContext(ctx, q+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return err
	}
	return core.Commit(tx, after...)
}

// TouchReview bumps updated_at.
func (s *Store) TouchReview(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE reviews SET updated_at = ? WHERE id = ?`, Now(), id)
}

// DeleteReview removes a review and everything on it (the files are the
// caller's to remove).
func (s *Store) DeleteReview(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM reviews WHERE id = ?`, id)
	return err
}

// GetReview loads a review as userID sees it, or ErrNotFound when they are not on it.
func (s *Store) GetReview(ctx context.Context, id, userID int64) (*Review, error) {
	var role string
	r, err := scanReview(s.DB.QueryRowContext(ctx, `SELECT `+reviewCols+`, p.role FROM reviews r
		JOIN participants p ON p.review_id = r.id AND p.user_id = ? WHERE r.id = ?`, userID, id), &role)
	if err != nil {
		return nil, err
	}
	r.MyRole = role
	if err := s.fillReview(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) fillReview(ctx context.Context, r *Review) error {
	var err error
	if r.Participants, err = s.ListParticipants(ctx, r.ID); err != nil {
		return err
	}
	if r.Revisions, err = s.ListRevisions(ctx, r.ID); err != nil {
		return err
	}
	for _, rv := range r.Revisions {
		if rv.ID == r.CurrentRevisionID {
			r.PageCount = rv.PageCount
		}
	}
	return s.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(1) FROM comments WHERE review_id = ? AND parent_id IS NULL AND deleted = 0 AND kind = 'issue' AND status = 'open'),
		(SELECT COUNT(1) FROM comments WHERE review_id = ? AND parent_id IS NULL AND deleted = 0 AND status = 'open'),
		(SELECT COUNT(1) FROM comments WHERE review_id = ? AND parent_id IS NULL AND deleted = 0),
		(SELECT COUNT(1) FROM tasks WHERE review_id = ? AND status = 'open'),
		COALESCE((SELECT MAX(created_at) FROM events WHERE review_id = ?), '')`, r.ID, r.ID, r.ID, r.ID, r.ID).
		Scan(&r.OpenIssues, &r.OpenThreads, &r.Threads, &r.OpenTasks, &r.LastActivity)
}

// ReviewFilter narrows ListReviews.
type ReviewFilter struct {
	UserID int64
	Status string // a status, "open" (not finished) or "" for all
	Role   string
	Query  string
	Limit  int // at most this many, 0 = all
}

// ListReviews returns the reviews a user is on, newest activity first.
func (s *Store) ListReviews(ctx context.Context, f ReviewFilter) ([]*Review, error) {
	q := `SELECT ` + reviewCols + `, p.role FROM reviews r JOIN participants p ON p.review_id = r.id AND p.user_id = ? WHERE 1 = 1`
	args := []any{f.UserID}
	switch f.Status {
	case "":
	case "open":
		q += ` AND r.status IN ('draft', 'in_review', 'changes_requested')`
	case "closed":
		q += ` AND r.status IN ('approved', 'rejected', 'cancelled')`
	default:
		q += ` AND r.status = ?`
		args = append(args, f.Status)
	}
	if f.Role != "" {
		q += ` AND p.role = ?`
		args = append(args, f.Role)
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		q += ` AND (r.title LIKE ? OR r.description LIKE ?)`
		args = append(args, "%"+t+"%", "%"+t+"%")
	}
	q += ` ORDER BY r.updated_at DESC, r.id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []*Review
	for rows.Next() {
		var role string
		r, err := scanReview(rows, &role)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.MyRole = role
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if err := s.fillReview(ctx, r); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []*Review{}
	}
	return out, nil
}

// ---- participants ----

// ListParticipants returns the people on a review, owner first, with their review time.
func (s *Store) ListParticipants(ctx context.Context, reviewID int64) ([]Participant, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT p.review_id, p.user_id, u.name, u.email, p.role, p.state, p.verdict, p.verdict_note, p.verdict_at,
		COALESCE(p.added_by, 0), p.added_at, p.first_opened_at, p.last_seen_at,
		COALESCE((SELECT SUM(active_seconds) FROM reading_sessions rs WHERE rs.review_id = p.review_id AND rs.user_id = p.user_id), 0)
		FROM participants p JOIN users u ON u.id = p.user_id WHERE p.review_id = ?
		ORDER BY CASE p.role WHEN 'owner' THEN 0 WHEN 'approver' THEN 1 WHEN 'reviewer' THEN 2 ELSE 3 END, p.added_at`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Participant{}
	for rows.Next() {
		var p Participant
		if err := rows.Scan(&p.ReviewID, &p.UserID, &p.Name, &p.Email, &p.Role, &p.State, &p.Verdict, &p.VerdictNote, &p.VerdictAt,
			&p.AddedBy, &p.AddedAt, &p.FirstOpenedAt, &p.LastSeenAt, &p.ActiveSeconds); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetParticipant returns one person's place on a review.
func (s *Store) GetParticipant(ctx context.Context, reviewID, userID int64) (*Participant, error) {
	list, err := s.ListParticipants(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].UserID == userID {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

// AddParticipant puts a person on a review (or changes their role when already on it).
func (s *Store) AddParticipant(ctx context.Context, reviewID, userID int64, role string, addedBy int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO participants (review_id, user_id, role, added_by, added_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(review_id, user_id) DO UPDATE SET role = excluded.role`, reviewID, userID, role, addedBy, Now())
	return err
}

// SetParticipantRole changes a person's role.
func (s *Store) SetParticipantRole(ctx context.Context, reviewID, userID int64, role string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE participants SET role = ? WHERE review_id = ? AND user_id = ?`, role, reviewID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveParticipant takes a person off a review.
func (s *Store) RemoveParticipant(ctx context.Context, reviewID, userID int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM participants WHERE review_id = ? AND user_id = ?`, reviewID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVerdict records a person's verdict (empty verdict = still reviewing).
func (s *Store) SetVerdict(ctx context.Context, reviewID, userID int64, verdict, note string) error {
	state, at := "done", sql.NullString{String: Now(), Valid: true}
	if verdict == "" {
		state, at = "reviewing", sql.NullString{}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE participants SET verdict = ?, verdict_note = ?, verdict_at = ?, state = ? WHERE review_id = ? AND user_id = ?`,
		verdict, note, at, state, reviewID, userID)
	return err
}

// ResetVerdicts clears every verdict on a review (a new revision needs a fresh look).
func (s *Store) ResetVerdicts(ctx context.Context, reviewID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE participants SET verdict = '', verdict_note = '', verdict_at = NULL,
		state = CASE WHEN first_opened_at IS NULL THEN 'invited' ELSE 'reviewing' END WHERE review_id = ? AND role <> 'owner'`, reviewID)
	return err
}

// MarkOpened stamps first_opened_at / last_seen_at for a participant and says
// whether this was their first time.
func (s *Store) MarkOpened(ctx context.Context, reviewID, userID int64) (first bool) {
	now := Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE participants SET first_opened_at = ?, state = CASE WHEN state = 'invited' THEN 'reviewing' ELSE state END
		WHERE review_id = ? AND user_id = ? AND first_opened_at IS NULL`, now, reviewID, userID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			first = true
		}
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE participants SET last_seen_at = ? WHERE review_id = ? AND user_id = ?`, now, reviewID, userID)
	return first
}

// ---- revisions ----

const revisionCols = `id, review_id, number, kind, filename, mime, size, sha256, storage_path, source_url, page_count, note, COALESCE(uploaded_by, 0), created_at`

func scanRevision(row interface{ Scan(...any) error }) (*Revision, error) {
	var r Revision
	if err := row.Scan(&r.ID, &r.ReviewID, &r.Number, &r.Kind, &r.Filename, &r.Mime, &r.Size, &r.SHA256, &r.StoragePath, &r.SourceURL,
		&r.PageCount, &r.Note, &r.UploadedBy, &r.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// AddRevision stores a new revision, numbered after the last, and makes it current.
func (s *Store) AddRevision(ctx context.Context, rv *Revision) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM revisions WHERE review_id = ?`, rv.ReviewID).Scan(&rv.Number); err != nil {
		return err
	}
	rv.CreatedAt = Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO revisions (review_id, number, kind, filename, mime, size, sha256, storage_path, source_url, page_count, note, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rv.ReviewID, rv.Number, rv.Kind, rv.Filename, rv.Mime, rv.Size, rv.SHA256, rv.StoragePath, rv.SourceURL,
		rv.PageCount, rv.Note, rv.UploadedBy, rv.CreatedAt)
	if err != nil {
		return err
	}
	rv.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE reviews SET current_revision_id = ?, updated_at = ? WHERE id = ?`, rv.ID, rv.CreatedAt, rv.ReviewID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListRevisions returns a review's revisions, oldest first.
func (s *Store) ListRevisions(ctx context.Context, reviewID int64) ([]Revision, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+revisionCols+` FROM revisions WHERE review_id = ? ORDER BY number`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRevision returns one revision of a review.
func (s *Store) GetRevision(ctx context.Context, reviewID, id int64) (*Revision, error) {
	return scanRevision(s.DB.QueryRowContext(ctx, `SELECT `+revisionCols+` FROM revisions WHERE review_id = ? AND id = ?`, reviewID, id))
}

// SetPageCount records the page count once a viewer has measured it.
func (s *Store) SetPageCount(ctx context.Context, reviewID, id int64, n int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE revisions SET page_count = ? WHERE review_id = ? AND id = ? AND page_count = 0`, n, reviewID, id)
	return err
}

// StoragePathInUse reports whether any revision still points at a stored file.
func (s *Store) StoragePathInUse(ctx context.Context, path string) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM revisions WHERE storage_path = ?`, path).Scan(&n)
	return n > 0
}
