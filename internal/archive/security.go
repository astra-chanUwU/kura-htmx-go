package archive

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

var (
	ErrInvalidRecovery   = errors.New("invalid username or recovery code")
	ErrInvalidChallenge  = errors.New("authentication request expired or already used")
	ErrLastAuthenticator = errors.New("the account must retain a password or passkey")
	ErrFreshPasskey      = errors.New("fresh passkey verification required")
	ErrConfirmation      = errors.New("confirmation required")
	ErrInvalidBootstrap  = errors.New("setup link expired or already used")
)

type Passkey struct {
	ID, UserID            int64
	Name                  string
	Credential            webauthn.Credential
	CreatedAt, LastUsedAt string
}

type AccountSecurity struct {
	PasswordEnabled    bool
	RecoveryCodeActive bool
	Passkeys           []Passkey
}

type AuthChallenge struct {
	Kind      string
	UserID    *int64
	Payload   []byte
	ExpiresAt time.Time
}

func NewPasskeyRegistrationUser(username string) (User, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	handle, err := randomBytes(32)
	if err != nil {
		return User{}, err
	}
	return User{Username: username, PasskeyHandle: handle}, nil
}

func (s *Store) CreateBootstrapToken(ctx context.Context, lifetime time.Duration) (string, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_super_admin=1`).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrAlreadyBootstrapped
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO bootstrap_tokens(id,token_hash,expires_at,created_at) VALUES(1,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,created_at=excluded.created_at`,
		hashToken(token), time.Now().UTC().Add(lifetime).Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) BootstrapTokenValid(ctx context.Context, token string) bool {
	var valid int
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM bootstrap_tokens b WHERE b.id=1 AND b.token_hash=? AND julianday(b.expires_at)>julianday(?)
		AND NOT EXISTS(SELECT 1 FROM users WHERE is_super_admin=1))`, hashToken(token), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&valid)
	return err == nil && valid == 1
}

func (s *Store) BootstrapSuperAdminWithPassword(ctx context.Context, token, username, password string) (User, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	handle, err := randomBytes(32)
	if err != nil {
		return User{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var valid int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM bootstrap_tokens b WHERE b.id=1 AND b.token_hash=? AND julianday(b.expires_at)>julianday(?)
		AND NOT EXISTS(SELECT 1 FROM users WHERE is_super_admin=1))`, hashToken(token), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&valid); err != nil || valid != 1 {
		return User{}, ErrInvalidBootstrap
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id,role,is_super_admin) VALUES(?,?,?,'admin',1)`, username, passwordHash, handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM bootstrap_tokens WHERE id=1 AND token_hash=?`, hashToken(token)); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	id, _ := result.LastInsertId()
	return s.User(ctx, id)
}

func (s *Store) ReplaceRecoveryCode(ctx context.Context, actor User) (string, error) {
	code, err := randomToken(24)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return "", ErrPermission
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id,code_hash,created_at) VALUES(?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(user_id) DO UPDATE SET code_hash=excluded.code_hash,created_at=excluded.created_at`, current.ID, hashToken(code)); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

func (s *Store) RecoverPassword(ctx context.Context, username, code, password string) (User, string, error) {
	passwordHash, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	var userID int64
	var codeHash, suspended string
	err = s.DB.QueryRowContext(ctx, `SELECT u.id,r.code_hash,COALESCE(u.suspended_at,'') FROM users u
		JOIN recovery_codes r ON r.user_id=u.id WHERE u.username=? COLLATE NOCASE`, username).Scan(&userID, &codeHash, &suspended)
	want := hashToken(code)
	if err != nil || suspended != "" || subtle.ConstantTimeCompare([]byte(codeHash), []byte(want)) != 1 {
		return User{}, "", ErrInvalidRecovery
	}
	replacement, err := randomToken(24)
	if err != nil {
		return User{}, "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET code_hash=?,created_at=CURRENT_TIMESTAMP WHERE user_id=? AND code_hash=?`, hashToken(replacement), userID, want)
	if err != nil {
		return User{}, "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return User{}, "", ErrInvalidRecovery
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, passwordHash, userID); err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		return User{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return User{}, "", err
	}
	user, err := s.User(ctx, userID)
	return user, replacement, err
}

func (s *Store) VerifyRecoveryCode(ctx context.Context, username, code string) (User, string, error) {
	var userID int64
	var codeHash, suspended string
	err := s.DB.QueryRowContext(ctx, `SELECT u.id,r.code_hash,COALESCE(u.suspended_at,'') FROM users u
		JOIN recovery_codes r ON r.user_id=u.id WHERE u.username=? COLLATE NOCASE`, username).Scan(&userID, &codeHash, &suspended)
	want := hashToken(code)
	if err != nil || suspended != "" || subtle.ConstantTimeCompare([]byte(codeHash), []byte(want)) != 1 {
		return User{}, "", ErrInvalidRecovery
	}
	user, err := s.WebAuthnUser(ctx, userID)
	if err != nil {
		return User{}, "", ErrInvalidRecovery
	}
	return user, codeHash, nil
}

func (s *Store) CompletePasskeyRecovery(ctx context.Context, userID int64, expectedCodeHash, name string, credential webauthn.Credential, preparedPassword string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return "", errors.New("invalid passkey credential")
	}
	if preparedPassword != "" && !strings.HasPrefix(preparedPassword, "$argon2id$") {
		return "", errors.New("invalid prepared password")
	}
	body, err := json.Marshal(credential)
	if err != nil {
		return "", err
	}
	replacement, err := randomToken(24)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET code_hash=?,created_at=CURRENT_TIMESTAMP WHERE user_id=? AND code_hash=?`, hashToken(replacement), userID, expectedCodeHash)
	if err != nil {
		return "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", ErrInvalidRecovery
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_credentials(user_id,credential_id,name,credential_json) VALUES(?,?,?,?)`, userID, credential.ID, name, body); err != nil {
		return "", err
	}
	if preparedPassword != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, preparedPassword, userID); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return replacement, nil
}

func (s *Store) AddPasskey(ctx context.Context, actor User, name string, credential webauthn.Credential) (Passkey, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return Passkey{}, errors.New("passkey name must be between 1 and 64 characters")
	}
	if len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return Passkey{}, errors.New("invalid passkey credential")
	}
	body, err := json.Marshal(credential)
	if err != nil {
		return Passkey{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Passkey{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return Passkey{}, ErrPermission
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO passkey_credentials(user_id,credential_id,name,credential_json) VALUES(?,?,?,?)`, current.ID, credential.ID, name, body)
	if err != nil {
		return Passkey{}, err
	}
	id, _ := result.LastInsertId()
	if err = tx.Commit(); err != nil {
		return Passkey{}, err
	}
	return s.passkey(ctx, current.ID, id)
}

func (s *Store) CreatePasskeyOnlyAccount(ctx context.Context, username, name string, credential webauthn.Credential) (User, string, error) {
	handle, err := randomBytes(32)
	if err != nil {
		return User{}, "", err
	}
	return s.CreatePasskeyOnlyAccountWithHandle(ctx, username, name, handle, credential)
}

func (s *Store) CreatePasskeyOnlyAccountWithHandle(ctx context.Context, username, name string, handle []byte, credential webauthn.Credential) (User, string, error) {
	return s.CreatePasskeyAccountWithHandleAndPassword(ctx, username, name, handle, credential, "")
}

func (s *Store) CreatePasskeyAccountWithHandleAndPassword(ctx context.Context, username, name string, handle []byte, credential webauthn.Credential, passwordHash string) (User, string, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || len(handle) != 32 || len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return User{}, "", errors.New("invalid passkey account")
	}
	if passwordHash != "" && !strings.HasPrefix(passwordHash, "$argon2id$") {
		return User{}, "", errors.New("invalid prepared password")
	}
	recovery, err := randomToken(24)
	if err != nil {
		return User{}, "", err
	}
	body, err := json.Marshal(credential)
	if err != nil {
		return User{}, "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	var storedPassword any
	if passwordHash != "" {
		storedPassword = passwordHash
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id) VALUES(?,?,?)`, username, storedPassword, handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", ErrUsernameTaken
		}
		return User{}, "", err
	}
	userID, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_credentials(user_id,credential_id,name,credential_json) VALUES(?,?,?,?)`, userID, credential.ID, name, body); err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id,code_hash) VALUES(?,?)`, userID, hashToken(recovery)); err != nil {
		return User{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return User{}, "", err
	}
	user, err := s.User(ctx, userID)
	return user, recovery, err
}

func (s *Store) BootstrapSuperAdminWithPasskey(ctx context.Context, token, username, name string, handle []byte, credential webauthn.Credential, password string) (User, string, error) {
	var passwordHash *string
	if password != "" {
		hash, err := hashPassword(password)
		if err != nil {
			return User{}, "", err
		}
		passwordHash = &hash
	}
	return s.bootstrapSuperAdminWithPasskey(ctx, token, username, name, handle, credential, passwordHash)
}

func PreparePassword(password string) (string, error) { return hashPassword(password) }

func (s *Store) BootstrapSuperAdminWithPasskeyHash(ctx context.Context, token, username, name string, handle []byte, credential webauthn.Credential, passwordHash string) (User, string, error) {
	var hash *string
	if passwordHash != "" {
		if !strings.HasPrefix(passwordHash, "$argon2id$") {
			return User{}, "", errors.New("invalid prepared password")
		}
		hash = &passwordHash
	}
	return s.bootstrapSuperAdminWithPasskey(ctx, token, username, name, handle, credential, hash)
}

func (s *Store) bootstrapSuperAdminWithPasskey(ctx context.Context, token, username, name string, handle []byte, credential webauthn.Credential, preparedPassword *string) (User, string, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || len(handle) != 32 || len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return User{}, "", errors.New("invalid passkey account")
	}
	var passwordHash any
	if preparedPassword != nil {
		passwordHash = *preparedPassword
	}
	recovery, err := randomToken(24)
	if err != nil {
		return User{}, "", err
	}
	body, err := json.Marshal(credential)
	if err != nil {
		return User{}, "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	var valid int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM bootstrap_tokens
		WHERE id=1 AND token_hash=? AND julianday(expires_at)>julianday(?) AND NOT EXISTS(SELECT 1 FROM users WHERE is_super_admin=1)`,
		hashToken(token), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&valid); err != nil || valid != 1 {
		return User{}, "", ErrInvalidBootstrap
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id,role,is_super_admin) VALUES(?,?,?,'admin',1)`, username, passwordHash, handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", ErrUsernameTaken
		}
		return User{}, "", err
	}
	userID, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_credentials(user_id,credential_id,name,credential_json) VALUES(?,?,?,?)`, userID, credential.ID, name, body); err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id,code_hash) VALUES(?,?)`, userID, hashToken(recovery)); err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM bootstrap_tokens WHERE id=1`); err != nil {
		return User{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return User{}, "", err
	}
	user, err := s.User(ctx, userID)
	return user, recovery, err
}

func (s *Store) WebAuthnUser(ctx context.Context, userID int64) (User, error) {
	var user User
	var super int
	err := s.DB.QueryRowContext(ctx, `SELECT id,username,webauthn_id,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, userID).Scan(
		&user.ID, &user.Username, &user.PasskeyHandle, &user.Role, &super, &user.SuspendedAt, &user.CreatedAt)
	if err != nil {
		return User{}, err
	}
	user.IsSuperAdmin = super != 0
	if !user.Active() {
		return User{}, ErrSuspended
	}
	keys, err := s.Passkeys(ctx, userID)
	if err != nil {
		return User{}, err
	}
	for _, key := range keys {
		user.Credentials = append(user.Credentials, key.Credential)
	}
	return user, nil
}

func (s *Store) PasskeyUser(ctx context.Context, credentialID, userHandle []byte) (User, error) {
	var userID int64
	err := s.DB.QueryRowContext(ctx, `SELECT u.id FROM users u JOIN passkey_credentials p ON p.user_id=u.id
		WHERE u.webauthn_id=? AND p.credential_id=? AND u.suspended_at IS NULL`, userHandle, credentialID).Scan(&userID)
	if err != nil {
		return User{}, err
	}
	return s.WebAuthnUser(ctx, userID)
}

func (s *Store) passkey(ctx context.Context, userID, id int64) (Passkey, error) {
	var key Passkey
	var body []byte
	err := s.DB.QueryRowContext(ctx, `SELECT id,user_id,name,credential_json,created_at,COALESCE(last_used_at,'')
		FROM passkey_credentials WHERE id=? AND user_id=?`, id, userID).Scan(&key.ID, &key.UserID, &key.Name, &body, &key.CreatedAt, &key.LastUsedAt)
	if err != nil {
		return Passkey{}, err
	}
	if err = json.Unmarshal(body, &key.Credential); err != nil {
		return Passkey{}, err
	}
	return key, nil
}

func (s *Store) Passkeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,user_id,name,credential_json,created_at,COALESCE(last_used_at,'')
		FROM passkey_credentials WHERE user_id=? ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Passkey
	for rows.Next() {
		var key Passkey
		var body []byte
		if err = rows.Scan(&key.ID, &key.UserID, &key.Name, &body, &key.CreatedAt, &key.LastUsedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &key.Credential); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) UpdatePasskey(ctx context.Context, userID int64, credential webauthn.Credential) error {
	body, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE passkey_credentials SET credential_json=?,last_used_at=CURRENT_TIMESTAMP WHERE user_id=? AND credential_id=?`, body, userID, credential.ID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RemovePasskey(ctx context.Context, actor User, passkeyID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return ErrPermission
	}
	var password sql.NullString
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, current.ID).Scan(&password); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM passkey_credentials WHERE user_id=?`, current.ID).Scan(&count); err != nil {
		return err
	}
	if !password.Valid && count <= 1 {
		return ErrLastAuthenticator
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM passkey_credentials WHERE id=? AND user_id=?`, passkeyID, current.ID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) RemovePassword(ctx context.Context, actor User, keepSessionToken, confirmation string) error {
	if confirmation != "remove" {
		return ErrConfirmation
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return ErrPermission
	}
	var fresh sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT fresh_passkey_at FROM sessions WHERE token_hash=? AND user_id=? AND expires_at>?`, hashToken(keepSessionToken), current.ID, time.Now().UTC().Format(time.RFC3339)).Scan(&fresh); err != nil || !fresh.Valid {
		return ErrFreshPasskey
	}
	verified, err := time.Parse(time.RFC3339Nano, fresh.String)
	if err != nil || !verified.After(time.Now().UTC().Add(-5*time.Minute)) {
		return ErrFreshPasskey
	}
	var keys, recovery int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM passkey_credentials WHERE user_id=?`, current.ID).Scan(&keys); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id=?`, current.ID).Scan(&recovery); err != nil {
		return err
	}
	if keys == 0 || recovery == 0 {
		return ErrLastAuthenticator
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash=NULL WHERE id=?`, current.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, current.ID, hashToken(keepSessionToken)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPassword(ctx context.Context, actor User, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return ErrPermission
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, hash, current.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SecurityStatus(ctx context.Context, userID int64) (AccountSecurity, error) {
	var status AccountSecurity
	if err := s.DB.QueryRowContext(ctx, `SELECT password_hash IS NOT NULL,EXISTS(SELECT 1 FROM recovery_codes WHERE user_id=users.id) FROM users WHERE id=?`, userID).Scan(&status.PasswordEnabled, &status.RecoveryCodeActive); err != nil {
		return status, err
	}
	keys, err := s.Passkeys(ctx, userID)
	status.Passkeys = keys
	return status, err
}

func (s *Store) CreateAuthChallenge(ctx context.Context, kind string, userID *int64, payload []byte, lifetime time.Duration) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(lifetime).Format(time.RFC3339Nano)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM auth_challenges WHERE julianday(expires_at)<=julianday(?)`, time.Now().UTC().Format(time.RFC3339Nano))
	_, err = s.DB.ExecContext(ctx, `INSERT INTO auth_challenges(token_hash,kind,user_id,payload,expires_at) VALUES(?,?,?,?,?)`, hashToken(token), kind, userID, payload, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) ConsumeAuthChallenge(ctx context.Context, token, kind string) (AuthChallenge, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AuthChallenge{}, err
	}
	defer tx.Rollback()
	var challenge AuthChallenge
	var userID sql.NullInt64
	var expires string
	err = tx.QueryRowContext(ctx, `SELECT kind,user_id,payload,expires_at FROM auth_challenges
		WHERE token_hash=? AND kind=? AND julianday(expires_at)>julianday(?)`, hashToken(token), kind, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&challenge.Kind, &userID, &challenge.Payload, &expires)
	if err != nil {
		return AuthChallenge{}, ErrInvalidChallenge
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM auth_challenges WHERE token_hash=?`, hashToken(token)); err != nil {
		return AuthChallenge{}, err
	}
	if err = tx.Commit(); err != nil {
		return AuthChallenge{}, err
	}
	if userID.Valid {
		challenge.UserID = &userID.Int64
	}
	challenge.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	return challenge, nil
}

func (s *Store) MarkSessionPasskeyVerified(ctx context.Context, token string, userID int64) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE sessions SET fresh_passkey_at=? WHERE token_hash=? AND user_id=? AND expires_at>?`,
		time.Now().UTC().Format(time.RFC3339Nano), hashToken(token), userID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SessionHasFreshPasskey(ctx context.Context, token string, maxAge time.Duration) (bool, error) {
	var raw sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT fresh_passkey_at FROM sessions WHERE token_hash=? AND expires_at>?`,
		hashToken(token), time.Now().UTC().Format(time.RFC3339)).Scan(&raw)
	if err != nil || !raw.Valid {
		return false, err
	}
	verified, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return false, err
	}
	return verified.After(time.Now().UTC().Add(-maxAge)), nil
}

func (s *Store) RevokeOtherSessions(ctx context.Context, actor User, keepSessionToken string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil {
		return ErrPermission
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, current.ID, hashToken(keepSessionToken)); err != nil {
		return err
	}
	return tx.Commit()
}
