package store

import (
	"context"
	"strings"
)

// LogEvent appends a line to a review's log. Errors are logged, not returned:
// the action already happened.
func (s *Store) LogEvent(ctx context.Context, e Event) {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events (review_id, user_id, kind, comment_id, task_id, revision_id, page, summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.ReviewID, nullID(e.UserID), e.Kind, nullID(e.CommentID), nullID(e.TaskID), nullID(e.RevisionID),
		e.Page, e.Summary, Now())
	if err != nil {
		s.log.Warn("log event", "kind", e.Kind, "err", err)
	}
}

// EventFilter narrows ListEvents.
type EventFilter struct {
	ReviewID int64 // one review, or 0 for every review UserID is on
	UserID   int64 // the viewer
	ActorID  int64 // only what this person did
	Kind     string
	Before   int64 // id, for paging
	Limit    int
}

// ListEvents returns log lines, newest first.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	q := `SELECT e.id, e.review_id, r.title, COALESCE(e.user_id, 0), COALESCE(NULLIF(u.name, ''), u.email, ''), e.kind, COALESCE(e.comment_id, 0),
		COALESCE(e.task_id, 0), COALESCE(e.revision_id, 0), e.page, e.summary, e.created_at
		FROM events e JOIN reviews r ON r.id = e.review_id
		JOIN participants p ON p.review_id = e.review_id AND p.user_id = ?
		LEFT JOIN users u ON u.id = e.user_id WHERE 1 = 1`
	args := []any{f.UserID}
	if f.ReviewID != 0 {
		q += ` AND e.review_id = ?`
		args = append(args, f.ReviewID)
	}
	if f.ActorID != 0 {
		q += ` AND e.user_id = ?`
		args = append(args, f.ActorID)
	}
	if f.Kind != "" {
		// "comment" matches comment.added, comment.resolved, …
		if strings.Contains(f.Kind, ".") {
			q += ` AND e.kind = ?`
			args = append(args, f.Kind)
		} else {
			q += ` AND e.kind LIKE ?`
			args = append(args, f.Kind+".%")
		}
	}
	if f.Before > 0 {
		q += ` AND e.id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 200
	}
	q += ` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ReviewID, &e.ReviewTitle, &e.UserID, &e.UserName, &e.Kind, &e.CommentID, &e.TaskID, &e.RevisionID,
			&e.Page, &e.Summary, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DayEvent is a log line for a day's journal, with the review's title and
// status now and the person's role on it.
type DayEvent struct {
	ReviewID    int64
	ReviewTitle string
	Status      string
	Role        string
	UserID      int64
	Kind        string
	CreatedAt   string
}

// DayEvents returns, oldest first, what a person did on the reviews they are
// on between from and to (stored stamps, from inclusive), and the decisions
// anyone made on the reviews they own.
func (s *Store) DayEvents(ctx context.Context, userID int64, from, to string) ([]DayEvent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT e.review_id, r.title, r.status, p.role, COALESCE(e.user_id, 0), e.kind, e.created_at
		FROM events e JOIN reviews r ON r.id = e.review_id
		JOIN participants p ON p.review_id = e.review_id AND p.user_id = ?
		WHERE e.created_at >= ? AND e.created_at < ?
		AND (e.user_id = ? OR (p.role = 'owner' AND e.kind IN ('review.approved', 'review.rejected', 'review.changes_requested')))
		ORDER BY e.created_at, e.id LIMIT 5000`, userID, from, to, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayEvent{}
	for rows.Next() {
		var e DayEvent
		if err := rows.Scan(&e.ReviewID, &e.ReviewTitle, &e.Status, &e.Role, &e.UserID, &e.Kind, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
