ALTER TABLE audit_events RENAME TO audit_events_legacy;
DROP INDEX audit_events_target;
DROP INDEX audit_events_post;

CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY,
  event_type TEXT NOT NULL CHECK(length(event_type)<=64),
  actor_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  actor_username TEXT NOT NULL DEFAULT '' CHECK(length(actor_username)<=160),
  target_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  uploader_username TEXT NOT NULL DEFAULT '' CHECK(length(uploader_username)<=160),
  post_id INTEGER,
  from_role TEXT,
  to_role TEXT,
  reason TEXT NOT NULL DEFAULT '' CHECK(length(reason)<=500),
  before_snapshot TEXT NOT NULL DEFAULT '' CHECK(length(before_snapshot)<=32768),
  after_snapshot TEXT NOT NULL DEFAULT '' CHECK(length(after_snapshot)<=32768),
  reverted_event_id INTEGER,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO audit_events(id,event_type,actor_id,actor_username,target_user_id,uploader_username,post_id,from_role,to_role,reason,created_at)
SELECT legacy.id,legacy.event_type,legacy.actor_id,COALESCE(actor.username,''),legacy.target_user_id,COALESCE(target.username,''),legacy.post_id,legacy.from_role,legacy.to_role,legacy.reason,legacy.created_at
FROM audit_events_legacy legacy
LEFT JOIN users actor ON actor.id=legacy.actor_id
LEFT JOIN users target ON target.id=legacy.target_user_id;

DROP TABLE audit_events_legacy;
CREATE INDEX audit_events_target ON audit_events(target_user_id,created_at);
CREATE INDEX audit_events_post ON audit_events(post_id,created_at);
CREATE INDEX audit_events_action ON audit_events(event_type,created_at);
