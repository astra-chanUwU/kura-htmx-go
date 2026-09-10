-- Rebuild users so password_hash can be NULL for passkey-only accounts.
-- legacy_alter_table keeps existing foreign keys pointed at the replacement
-- table name while the old table is renamed inside this migration.
PRAGMA legacy_alter_table=ON;
ALTER TABLE users RENAME TO users_legacy;
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT,
  webauthn_id BLOB NOT NULL UNIQUE,
  role TEXT NOT NULL DEFAULT 'viewer' CHECK(role IN ('viewer','moderator','admin')),
  is_super_admin INTEGER NOT NULL DEFAULT 0 CHECK(is_super_admin IN (0,1)),
  suspended_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO users(id,username,password_hash,webauthn_id,role,is_super_admin,suspended_at,created_at)
SELECT id,username,password_hash,randomblob(32),role,is_super_admin,suspended_at,created_at FROM users_legacy;
DROP TABLE users_legacy;
PRAGMA legacy_alter_table=OFF;
CREATE UNIQUE INDEX users_one_super_admin ON users(is_super_admin) WHERE is_super_admin=1;

ALTER TABLE sessions ADD COLUMN fresh_passkey_at TEXT;

CREATE TABLE passkey_credentials (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,
  name TEXT NOT NULL,
  credential_json BLOB NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
);
CREATE INDEX passkey_credentials_user ON passkey_credentials(user_id,created_at);

CREATE TABLE recovery_codes (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE auth_challenges (
  token_hash TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('register','add','login','fresh','recover-passkey','bootstrap')),
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  payload BLOB NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX auth_challenges_expires ON auth_challenges(expires_at);

CREATE TABLE bootstrap_tokens (
  id INTEGER PRIMARY KEY CHECK(id=1),
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
