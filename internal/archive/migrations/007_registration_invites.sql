CREATE TABLE registration_invites (
  id INTEGER PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_by_username TEXT NOT NULL DEFAULT '' CHECK(length(created_by_username)<=160),
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  consumed_at TEXT,
  consumed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  consumed_by_username TEXT NOT NULL DEFAULT '' CHECK(length(consumed_by_username)<=160),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX registration_invites_active ON registration_invites(expires_at,revoked_at,consumed_at);
