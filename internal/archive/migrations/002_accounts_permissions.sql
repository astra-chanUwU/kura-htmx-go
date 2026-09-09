CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'viewer' CHECK(role IN ('viewer','moderator','admin')),
  is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK(is_super_admin IN (0,1)),
  suspended_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX users_one_super_admin ON users(is_super_admin) WHERE is_super_admin=1;

CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  csrf_token TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX sessions_expires_at ON sessions(expires_at);
CREATE INDEX sessions_user_id ON sessions(user_id);

ALTER TABLE posts ADD COLUMN uploader_id INTEGER REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE posts ADD COLUMN deleted_at TEXT;
CREATE INDEX posts_uploader_id ON posts(uploader_id);

ALTER TABLE pools ADD COLUMN owner_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE pools ADD COLUMN status TEXT NOT NULL DEFAULT 'published' CHECK(status IN ('draft','published'));
CREATE INDEX pools_owner_status ON pools(owner_id,status);

-- Version 001 stored installation-local favorites under owner_key. Accounts
-- provide the first meaningful owner, so replace that unmappable legacy table.
DROP TABLE IF EXISTS favorites;
CREATE TABLE favorites (
  post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(post_id,user_id)
);
CREATE INDEX favorites_user_created ON favorites(user_id,created_at DESC);
