-- Notifications: someone mentioned you, replied, a new version is up, the
-- review started or was decided, a task is yours. Each person's inbox; the
-- bell in the top bar reads it. Browsers and phones that allowed it also get
-- a Web Push (push_subs), signed with Review's own key (settings "vapid").
CREATE TABLE notifications (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  review_id  INTEGER REFERENCES reviews(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  comment_id INTEGER NOT NULL DEFAULT 0,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  read_at    TEXT
);
CREATE INDEX notifications_user ON notifications(user_id, id);

CREATE TABLE push_subs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  agent      TEXT NOT NULL DEFAULT '',
  fails      INTEGER NOT NULL DEFAULT 0,
  last_ok    TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX push_subs_user ON push_subs(user_id);
