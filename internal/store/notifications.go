package store

import (
	"context"
	"database/sql"
	"strings"
)

// Notification is one line in someone's inbox.
type Notification struct {
	ID          int64   `json:"id"`
	UserID      int64   `json:"-"`
	ReviewID    int64   `json:"review_id"`
	ReviewTitle string  `json:"review_title"`
	Kind        string  `json:"kind"`
	ActorID     int64   `json:"actor_id"`
	ActorName   string  `json:"actor_name"`
	CommentID   int64   `json:"comment_id"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	CreatedAt   string  `json:"created_at"`
	ReadAt      *string `json:"read_at"`
}

// NotifyKinds are the things a person can be told about; each can be switched off.
var NotifyKinds = []string{"mention", "reply", "thread", "revision", "review", "verdict", "task"}

// AddNotification puts a line in someone's inbox.
func (s *Store) AddNotification(ctx context.Context, n *Notification) error {
	n.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO notifications (user_id, review_id, kind, actor_id, comment_id, title, body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.UserID, nullID(n.ReviewID), n.Kind, nullID(n.ActorID), n.CommentID, n.Title, n.Body, n.CreatedAt)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

// ListNotifications returns someone's newest notifications and how many are unread.
func (s *Store) ListNotifications(ctx context.Context, uid int64, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, n.user_id, COALESCE(n.review_id, 0), COALESCE(r.title, ''), n.kind, COALESCE(n.actor_id, 0),
		COALESCE(NULLIF(u.name, ''), u.email, ''), n.comment_id, n.title, n.body, n.created_at, n.read_at
		FROM notifications n LEFT JOIN reviews r ON r.id = n.review_id LEFT JOIN users u ON u.id = n.actor_id
		WHERE n.user_id = ? ORDER BY n.id DESC LIMIT ?`, uid, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.ReviewID, &n.ReviewTitle, &n.Kind, &n.ActorID, &n.ActorName, &n.CommentID, &n.Title, &n.Body, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	unread, err := s.UnreadNotifications(ctx, uid)
	return out, unread, err
}

// UnreadNotifications counts someone's unread notifications.
func (s *Store) UnreadNotifications(ctx context.Context, uid int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM notifications WHERE user_id = ? AND read_at IS NULL`, uid).Scan(&n)
	return n, err
}

// ReadNotifications marks some (or, with no ids, all) of someone's notifications read.
func (s *Store) ReadNotifications(ctx context.Context, uid int64, ids []int64) error {
	if len(ids) == 0 {
		_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, Now(), uid)
		return err
	}
	args := []any{Now(), uid}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL AND id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// ReadReviewNotifications marks what someone was told about a thread (or, with
// commentID 0, the whole review) read once they have looked at it.
func (s *Store) ReadReviewNotifications(ctx context.Context, uid, reviewID, commentID int64) error {
	q := `UPDATE notifications SET read_at = ? WHERE user_id = ? AND review_id = ? AND read_at IS NULL`
	args := []any{Now(), uid, reviewID}
	if commentID != 0 {
		q += ` AND comment_id = ?`
		args = append(args, commentID)
	}
	_, err := s.DB.ExecContext(ctx, q, args...)
	return err
}

// PruneNotifications forgets read notifications older than the cutoff.
func (s *Store) PruneNotifications(ctx context.Context, before string) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM notifications WHERE read_at IS NOT NULL AND created_at < ?`, before)
}

// NotifyOff returns the kinds someone switched off.
func (s *Store) NotifyOff(ctx context.Context, uid int64) map[string]bool {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE user_id = ? AND key = 'notify_off'`, uid).Scan(&v)
	off := map[string]bool{}
	for _, k := range strings.Split(v, ",") {
		if k != "" {
			off[k] = true
		}
	}
	return off
}

// SaveNotifyOff stores the kinds someone switched off.
func (s *Store) SaveNotifyOff(ctx context.Context, uid int64, kinds []string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES (?, 'notify_off', ?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
		uid, strings.Join(kinds, ","))
	return err
}

// ThreadPeople returns everyone who wrote in a thread (its opener and repliers).
func (s *Store) ThreadPeople(ctx context.Context, rootID int64) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT author_id FROM comments WHERE (id = ? OR parent_id = ?) AND author_id IS NOT NULL AND deleted = 0`, rootID, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- Web Push subscriptions ----

// PushSub is one browser or phone that takes notifications.
type PushSub struct {
	ID       int64   `json:"id"`
	UserID   int64   `json:"-"`
	Endpoint string  `json:"endpoint"`
	P256dh   string  `json:"-"`
	Auth     string  `json:"-"`
	Agent    string  `json:"agent"`
	Fails    int     `json:"fails"`
	LastOK   *string `json:"last_ok"`
	Created  string  `json:"created_at"`
}

// PushSubs lists someone's browsers and phones.
func (s *Store) PushSubs(ctx context.Context, uid int64) ([]PushSub, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, endpoint, p256dh, auth, agent, fails, last_ok, created_at FROM push_subs WHERE user_id = ? ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSub{}
	for rows.Next() {
		var p PushSub
		if err := rows.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Agent, &p.Fails, &p.LastOK, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePushSub adds a browser (or moves an endpoint to this person).
func (s *Store) SavePushSub(ctx context.Context, p *PushSub) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO push_subs (user_id, endpoint, p256dh, auth, agent) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth, agent = excluded.agent, fails = 0`,
		p.UserID, p.Endpoint, p.P256dh, p.Auth, p.Agent)
	return err
}

// DeletePushSub forgets a browser.
func (s *Store) DeletePushSub(ctx context.Context, uid int64, endpoint string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE user_id = ? AND endpoint = ?`, uid, endpoint)
	return err
}

// PushResult notes how sending to a browser went; a gone one is forgotten.
func (s *Store) PushResult(ctx context.Context, id int64, ok, gone bool) {
	switch {
	case gone:
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ?`, id)
	case ok:
		_, _ = s.DB.ExecContext(ctx, `UPDATE push_subs SET fails = 0, last_ok = ? WHERE id = ?`, Now(), id)
	default:
		_, _ = s.DB.ExecContext(ctx, `UPDATE push_subs SET fails = fails + 1 WHERE id = ?`, id)
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ? AND fails >= 10`, id)
	}
}

// Setting reads one box-wide setting ("" when unset).
func (s *Store) Setting(ctx context.Context, key string) string {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil && err != sql.ErrNoRows {
		return ""
	}
	return v
}

// SetSetting stores one box-wide setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
