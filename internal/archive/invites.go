package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

const maxRegistrationInviteLifetime = 30 * 24 * time.Hour

var (
	ErrInvalidRegistrationInvite  = errors.New("invalid registration invite")
	ErrRegistrationInviteLifetime = errors.New("registration invite lifetime is out of bounds")
)

type RegistrationInvite struct {
	ID           int64
	Token        string
	CreatedByID  int64
	CreatedBy    string
	ExpiresAt    time.Time
	RevokedAt    string
	ConsumedAt   string
	ConsumedByID int64
	ConsumedBy   string
	CreatedAt    string
}

type registrationInviteRow struct {
	RegistrationInvite
	TokenHash string
}

func (s *Store) CreateRegistrationInvite(ctx context.Context, actor User, lifetime time.Duration) (RegistrationInvite, error) {
	if lifetime <= 0 || lifetime > maxRegistrationInviteLifetime {
		return RegistrationInvite{}, ErrRegistrationInviteLifetime
	}
	token, err := randomToken(32)
	if err != nil {
		return RegistrationInvite{}, err
	}
	expires := time.Now().UTC().Add(lifetime)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RegistrationInvite{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return RegistrationInvite{}, ErrPermission
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO registration_invites(token_hash,created_by,created_by_username,expires_at) VALUES(?,?,?,?)`, hashToken(token), current.ID, current.Username, expires.Format(time.RFC3339Nano))
	if err != nil {
		return RegistrationInvite{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return RegistrationInvite{}, err
	}
	if err = insertAuditRecordTx(ctx, tx, current, "invite_created", 0, "", 0, "registration invite #"+strconv.FormatInt(id, 10)+" created", "", "", 0); err != nil {
		return RegistrationInvite{}, err
	}
	if err = tx.Commit(); err != nil {
		return RegistrationInvite{}, err
	}
	return RegistrationInvite{ID: id, Token: token, CreatedByID: current.ID, CreatedBy: current.Username, ExpiresAt: expires}, nil
}

func (s *Store) RevokeRegistrationInvite(ctx context.Context, actor User, inviteID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return ErrPermission
	}
	result, err := tx.ExecContext(ctx, `UPDATE registration_invites SET revoked_at=? WHERE id=? AND revoked_at IS NULL AND consumed_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), inviteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrInvalidRegistrationInvite
	}
	if err = insertAuditRecordTx(ctx, tx, current, "invite_revoked", 0, "", 0, "registration invite #"+strconv.FormatInt(inviteID, 10)+" revoked", "", "", 0); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RegistrationInvites(ctx context.Context, actor User) ([]RegistrationInvite, error) {
	current, err := s.User(ctx, actor.ID)
	if err != nil || !current.Active() || !current.IsSuperAdmin {
		return nil, ErrPermission
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,COALESCE(created_by,0),created_by_username,expires_at,COALESCE(revoked_at,''),COALESCE(consumed_at,''),COALESCE(consumed_by,0),consumed_by_username,created_at FROM registration_invites ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invites []RegistrationInvite
	for rows.Next() {
		var invite RegistrationInvite
		var expires string
		if err = rows.Scan(&invite.ID, &invite.CreatedByID, &invite.CreatedBy, &expires, &invite.RevokedAt, &invite.ConsumedAt, &invite.ConsumedByID, &invite.ConsumedBy, &invite.CreatedAt); err != nil {
			return nil, err
		}
		invite.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (s *Store) RegisterWithInvite(ctx context.Context, token, username, password string) (User, error) {
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
	invite, err := loadActiveInviteTx(ctx, tx, token)
	if err != nil {
		return User{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id) VALUES(?,?,?)`, username, passwordHash, handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if err = consumeInviteTx(ctx, tx, invite, userID, username); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return s.User(ctx, userID)
}

func RegistrationInviteIdentifier(token string) string { return hashToken(token) }

func (s *Store) RegistrationInviteValid(ctx context.Context, token string) bool {
	return s.registrationInviteHashValid(ctx, hashToken(token))
}

func (s *Store) registrationInviteHashValid(ctx context.Context, tokenHash string) bool {
	var valid int
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM registration_invites WHERE token_hash=? AND revoked_at IS NULL AND consumed_at IS NULL AND julianday(expires_at)>julianday(?))`, tokenHash, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&valid)
	return err == nil && valid == 1
}

func (s *Store) CreatePasskeyAccountWithInvite(ctx context.Context, token, username, name string, handle []byte, credential webauthn.Credential, passwordHash string) (User, string, error) {
	return s.createPasskeyAccountWithInviteHash(ctx, hashToken(token), username, name, handle, credential, passwordHash)
}

func (s *Store) CreatePasskeyAccountWithInviteHash(ctx context.Context, tokenHash, username, name string, handle []byte, credential webauthn.Credential, passwordHash string) (User, string, error) {
	return s.createPasskeyAccountWithInviteHash(ctx, tokenHash, username, name, handle, credential, passwordHash)
}

func (s *Store) createPasskeyAccountWithInviteHash(ctx context.Context, tokenHash, username, name string, handle []byte, credential webauthn.Credential, passwordHash string) (User, string, error) {
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
	invite, err := loadActiveInviteHashTx(ctx, tx, tokenHash)
	if err != nil {
		return User{}, "", err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id) VALUES(?,?,?)`, username, nullableString(passwordHash), handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", ErrUsernameTaken
		}
		return User{}, "", err
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO passkey_credentials(user_id,credential_id,name,credential_json) VALUES(?,?,?,?)`, userID, credential.ID, name, body); err != nil {
		return User{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id,code_hash) VALUES(?,?)`, userID, hashToken(recovery)); err != nil {
		return User{}, "", err
	}
	if err = consumeInviteTx(ctx, tx, invite, userID, username); err != nil {
		return User{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return User{}, "", err
	}
	user, err := s.User(ctx, userID)
	return user, recovery, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func loadActiveInviteTx(ctx context.Context, tx *sql.Tx, token string) (registrationInviteRow, error) {
	return loadActiveInviteHashTx(ctx, tx, hashToken(token))
}

func loadActiveInviteHashTx(ctx context.Context, tx *sql.Tx, tokenHash string) (registrationInviteRow, error) {
	var invite registrationInviteRow
	var expires string
	var revoked, consumed sql.NullString
	var createdBy, consumedBy sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,token_hash,COALESCE(created_by,0),created_by_username,expires_at,revoked_at,consumed_at,COALESCE(consumed_by,0),consumed_by_username,created_at FROM registration_invites WHERE token_hash=? AND revoked_at IS NULL AND consumed_at IS NULL AND julianday(expires_at)>julianday(?)`, tokenHash, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&invite.ID, &invite.TokenHash, &createdBy, &invite.CreatedBy, &expires, &revoked, &consumed, &consumedBy, &invite.ConsumedBy, &invite.CreatedAt)
	if err != nil {
		return registrationInviteRow{}, ErrInvalidRegistrationInvite
	}
	invite.CreatedByID = createdBy.Int64
	invite.ConsumedByID = consumedBy.Int64
	invite.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if revoked.Valid {
		invite.RevokedAt = revoked.String
	}
	if consumed.Valid {
		invite.ConsumedAt = consumed.String
	}
	return invite, nil
}

func consumeInviteTx(ctx context.Context, tx *sql.Tx, invite registrationInviteRow, userID int64, username string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE registration_invites SET consumed_at=?,consumed_by=?,consumed_by_username=? WHERE id=? AND revoked_at IS NULL AND consumed_at IS NULL AND julianday(expires_at)>julianday(?)`, now, userID, username, invite.ID, now)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrInvalidRegistrationInvite
	}
	actor := User{ID: invite.CreatedByID, Username: invite.CreatedBy}
	return insertAuditRecordTx(ctx, tx, actor, "invite_consumed", userID, username, 0, "registration invite #"+strconv.FormatInt(invite.ID, 10)+" consumed", "", "", 0)
}
