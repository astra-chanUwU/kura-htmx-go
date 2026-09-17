ALTER TABLE posts ADD COLUMN quarantined_at TEXT;
ALTER TABLE posts ADD COLUMN quarantined_by INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE posts ADD COLUMN quarantine_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN quarantine_previous_status TEXT CHECK(quarantine_previous_status IN ('draft','published'));
CREATE INDEX posts_quarantine ON posts(quarantined_at,deleted_at,status);

CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY,
  event_type TEXT NOT NULL,
  actor_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  target_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  post_id INTEGER,
  from_role TEXT,
  to_role TEXT,
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX audit_events_target ON audit_events(target_user_id,created_at);
CREATE INDEX audit_events_post ON audit_events(post_id,created_at);
