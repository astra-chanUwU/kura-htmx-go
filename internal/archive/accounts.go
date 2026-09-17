package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/argon2"
)

const sessionLifetime = 30 * 24 * time.Hour

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

var (
	ErrUsernameTaken       = errors.New("username is already registered")
	ErrInvalidLogin        = errors.New("invalid username or password")
	ErrSuspended           = errors.New("account is suspended")
	ErrLastSuperAdmin      = errors.New("the installation must retain an active super admin")
	ErrPermission          = errors.New("permission denied")
	ErrAlreadyBootstrapped = errors.New("a super admin already exists")
)

type User struct {
	ID            int64
	Username      string
	Role          string
	IsSuperAdmin  bool
	SuspendedAt   string
	CreatedAt     string
	PasskeyHandle []byte
	Credentials   []webauthn.Credential
}

func (u User) WebAuthnID() []byte                         { return u.PasskeyHandle }
func (u User) WebAuthnName() string                       { return u.Username }
func (u User) WebAuthnDisplayName() string                { return u.Username }
func (u User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

func (u *User) Active() bool { return u != nil && u.SuspendedAt == "" }
func (u *User) CanUpload() bool {
	return u.Active() && (u.Role == "moderator" || u.Role == "admin")
}
func (u *User) CanAdminister() bool { return u.Active() && u.Role == "admin" }
func (u *User) CanDelete(post Post) bool {
	return u.CanUpload() && (u.Role == "admin" || post.UploaderID == u.ID)
}

type Session struct {
	Token, CSRF string
	ExpiresAt   time.Time
	User        *User
}

func randomToken(bytes int) (string, error) {
	b, err := randomBytes(bytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomBytes(size int) ([]byte, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

func hashPassword(password string) (string, error) {
	length := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || length < 15 || length > 128 {
		return "", errors.New("password must be between 15 and 128 characters")
	}
	for _, r := range password {
		if !unicode.IsPrint(r) {
			return "", errors.New("password may contain only printable characters")
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func normalizeUsername(username string) (string, error) {
	if len(username) < 3 || len(username) > 32 {
		return "", errors.New("username must be 3 to 32 characters")
	}
	for i, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return "", errors.New("username may contain only letters, numbers, underscores, and hyphens")
		}
		if (i == 0 || i == len(username)-1) && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return "", errors.New("username must begin and end with a letter or number")
		}
	}
	return username, nil
}

func (s *Store) Register(ctx context.Context, username, password string) (User, error) {
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
	result, err := s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,webauthn_id) VALUES(?,?,?)`, username, passwordHash, handle)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	id, _ := result.LastInsertId()
	return s.User(ctx, id)
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var user User
	var hash sql.NullString
	var super int
	err := s.DB.QueryRowContext(ctx, `SELECT id,username,password_hash,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE username=? COLLATE NOCASE`, username).Scan(
		&user.ID, &user.Username, &hash, &user.Role, &super, &user.SuspendedAt, &user.CreatedAt)
	encoded := dummyPasswordHash
	if hash.Valid {
		encoded = hash.String
	}
	passwordValid := verifyPassword(encoded, password)
	if err != nil || !hash.Valid || !passwordValid {
		return User{}, ErrInvalidLogin
	}
	user.IsSuperAdmin = super != 0
	if !user.Active() {
		return User{}, ErrInvalidLogin
	}
	return user, nil
}

func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var user User
	var super int
	err := s.DB.QueryRowContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, id).Scan(
		&user.ID, &user.Username, &user.Role, &super, &user.SuspendedAt, &user.CreatedAt)
	user.IsSuperAdmin = super != 0
	return user, err
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	var super int
	err := row.Scan(&user.ID, &user.Username, &user.Role, &super, &user.SuspendedAt, &user.CreatedAt)
	user.IsSuperAdmin = super != 0
	return user, err
}

func (s *Store) actorTx(ctx context.Context, tx *sql.Tx, actor User) (User, error) {
	current, err := scanUser(tx.QueryRowContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, actor.ID))
	if err != nil || !current.Active() {
		return User{}, ErrPermission
	}
	return current, nil
}

func (s *Store) NewSession(ctx context.Context, userID *int64) (Session, error) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, time.Now().UTC().Format(time.RFC3339))
	token, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return Session{}, err
	}
	expires := time.Now().UTC().Add(sessionLifetime)
	if _, err = s.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES(?,?,?,?)`, hashToken(token), userID, csrf, expires.Format(time.RFC3339)); err != nil {
		return Session{}, err
	}
	return Session{Token: token, CSRF: csrf, ExpiresAt: expires}, nil
}

func (s *Store) Session(ctx context.Context, token string) (Session, error) {
	var session Session
	var userID sql.NullInt64
	var expires string
	err := s.DB.QueryRowContext(ctx, `SELECT user_id,csrf_token,expires_at FROM sessions WHERE token_hash=? AND expires_at>?`, hashToken(token), time.Now().UTC().Format(time.RFC3339)).Scan(&userID, &session.CSRF, &expires)
	if err != nil {
		return Session{}, err
	}
	session.Token = token
	session.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return Session{}, err
	}
	if userID.Valid {
		user, userErr := s.User(ctx, userID.Int64)
		if userErr == nil && user.Active() {
			session.User = &user
		}
	}
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
	return err
}

func (s *Store) BootstrapSuperAdmin(ctx context.Context, username, password string) (User, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_super_admin=1`).Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, ErrAlreadyBootstrapped
	}
	user, err := s.Register(ctx, username, password)
	if err != nil {
		return User{}, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE users SET role='admin',is_super_admin=1 WHERE id=?`, user.ID)
	if err != nil {
		return User{}, err
	}
	return s.User(ctx, user.ID)
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users ORDER BY is_super_admin DESC,username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		var super int
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &super, &user.SuspendedAt, &user.CreatedAt); err != nil {
			return nil, err
		}
		user.IsSuperAdmin = super != 0
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) SetUserRole(ctx context.Context, actor User, targetID int64, role string) error {
	if role != "viewer" && role != "moderator" && role != "admin" {
		return ErrPermission
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanAdminister() {
		return ErrPermission
	}
	target, err := scanUser(tx.QueryRowContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, targetID))
	if err != nil {
		return err
	}
	if target.IsSuperAdmin {
		return ErrLastSuperAdmin
	}
	if (target.Role == "admin" || role == "admin") && !current.IsSuperAdmin {
		return ErrPermission
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET role=? WHERE id=?`, role, targetID); err != nil {
		return err
	}
	if target.Role != "viewer" && role == "viewer" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, targetID); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,target_user_id,from_role,to_role,reason,created_at) VALUES('role_change',?,?,?,?,?,?)`, current.ID, targetID, target.Role, role, "role changed", now); err != nil {
			return err
		}
		rows, queryErr := tx.QueryContext(ctx, `SELECT id FROM posts WHERE uploader_id=? AND status='draft' AND deleted_at IS NULL AND quarantined_at IS NULL`, targetID)
		if queryErr != nil {
			return queryErr
		}
		var postIDs []int64
		for rows.Next() {
			var postID int64
			if err = rows.Scan(&postID); err != nil {
				rows.Close()
				return err
			}
			postIDs = append(postIDs, postID)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, postID := range postIDs {
			if _, err = tx.ExecContext(ctx, `UPDATE posts SET quarantined_at=?,quarantined_by=?,quarantine_reason=?,quarantine_previous_status=status WHERE id=? AND deleted_at IS NULL AND quarantined_at IS NULL`, now, current.ID, "uploader role demoted to viewer", postID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,target_user_id,post_id,from_role,to_role,reason,created_at) VALUES('role_change_quarantine',?,?,?,?,?,?,?)`, current.ID, targetID, postID, target.Role, role, "draft quarantined during role change", now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SetUserSuspended(ctx context.Context, actor User, targetID int64, suspended bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanAdminister() {
		return ErrPermission
	}
	target, err := scanUser(tx.QueryRowContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, targetID))
	if err != nil {
		return err
	}
	if target.IsSuperAdmin || (target.Role == "admin" && !current.IsSuperAdmin) {
		return ErrPermission
	}
	value := any(nil)
	if suspended {
		value = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET suspended_at=? WHERE id=?`, value, targetID); err != nil {
		return err
	}
	if suspended {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, targetID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) TransferSuperAdmin(ctx context.Context, actor User, targetID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin || current.ID == targetID {
		return ErrPermission
	}
	target, err := scanUser(tx.QueryRowContext(ctx, `SELECT id,username,role,is_super_admin,COALESCE(suspended_at,''),created_at FROM users WHERE id=?`, targetID))
	if err != nil {
		return err
	}
	if !target.Active() {
		return ErrLastSuperAdmin
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET is_super_admin=0 WHERE id=?`, current.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET role='admin',is_super_admin=1 WHERE id=?`, target.ID); err != nil {
		return err
	}
	return tx.Commit()
}
