package archive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestPasswordAndUsernamePolicy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, password := range []string{
		"fourteen char!",
		strings.Repeat("x", 129),
	} {
		if _, err := s.Register(ctx, "valid-name", password); err == nil {
			t.Fatalf("accepted invalid password length %d", utf8.RuneCountInString(password))
		}
	}

	password := "  密碼 with spaces  "
	if utf8.RuneCountInString(password) < 15 {
		t.Fatal("test password is unexpectedly short")
	}
	user, err := s.Register(ctx, "Case_Name-7", password)
	if err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	if user.Username != "Case_Name-7" {
		t.Fatalf("display casing changed: %q", user.Username)
	}
	if _, err = s.Authenticate(ctx, "case_name-7", password); err != nil {
		t.Fatalf("case-insensitive login failed: %v", err)
	}
	if _, err = s.Authenticate(ctx, " Case_Name-7 ", password); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("username containing spaces was accepted at login: %v", err)
	}
	if _, err = s.Authenticate(ctx, "Case_Name-7", strings.TrimSpace(password)); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("password was trimmed or normalized: %v", err)
	}
	if _, err = s.Register(ctx, "case_name-7", "another password"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("case-insensitive uniqueness failed: %v", err)
	}

	for i, username := range []string{"-starts", "ends-", "has space", "naïve", " abcd", "abcd "} {
		if _, err = s.Register(ctx, username, "valid password here"); err == nil {
			t.Fatalf("accepted invalid username %d: %q", i, username)
		}
	}
}

func TestRecoveryCodeIsHashedOneUseAndRevokesSessions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.Register(ctx, "recover-me", "original password here")
	if err != nil {
		t.Fatal(err)
	}
	oldSession, _ := s.NewSession(ctx, &user.ID)
	code, err := s.ReplaceRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.DB.QueryRow(`SELECT code_hash FROM recovery_codes WHERE user_id=?`, user.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == code || strings.Contains(stored, code) {
		t.Fatal("recovery code was stored in plaintext")
	}
	recovered, replacement, err := s.RecoverPassword(ctx, "RECOVER-ME", code, "replacement password")
	if err != nil || recovered.ID != user.ID || replacement == "" || replacement == code {
		t.Fatalf("recovery failed: user=%+v replacement=%q err=%v", recovered, replacement, err)
	}
	if _, err = s.Session(ctx, oldSession.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("recovery did not revoke old session: %v", err)
	}
	if _, err = s.Authenticate(ctx, "recover-me", "replacement password"); err != nil {
		t.Fatalf("replacement password does not authenticate: %v", err)
	}
	if _, _, err = s.RecoverPassword(ctx, "recover-me", code, "another replacement"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatalf("used recovery code remained valid: %v", err)
	}
}

func TestRecoveryCanAddPasskeyRotateCodeAndRevokeSessions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, _ := s.Register(ctx, "recover-key", "original key password")
	oldSession, _ := s.NewSession(ctx, &user.ID)
	code, _ := s.ReplaceRecoveryCode(ctx, user.ID)
	authUser, grant, err := s.VerifyRecoveryCode(ctx, "RECOVER-KEY", code)
	if err != nil || authUser.ID != user.ID || grant == "" {
		t.Fatalf("recovery verification failed: user=%+v grant=%q err=%v", authUser, grant, err)
	}
	credential := webauthn.Credential{ID: []byte("recovered-key"), PublicKey: []byte("recovered-public")}
	replacement, err := s.CompletePasskeyRecovery(ctx, user.ID, grant, "Recovered passkey", credential, "")
	if err != nil || replacement == "" || replacement == code {
		t.Fatalf("passkey recovery failed: replacement=%q err=%v", replacement, err)
	}
	if _, err = s.Session(ctx, oldSession.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("passkey recovery did not revoke session: %v", err)
	}
	security, _ := s.SecurityStatus(ctx, user.ID)
	if len(security.Passkeys) != 1 || security.Passkeys[0].Name != "Recovered passkey" {
		t.Fatalf("recovered passkey missing: %+v", security)
	}
	if _, _, err = s.VerifyRecoveryCode(ctx, "recover-key", code); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatalf("old recovery code survived passkey recovery: %v", err)
	}
}

func TestPasskeyStorageAndLastAuthenticatorProtection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, _ := s.Register(ctx, "key-user", "password authentication")
	keep, _ := s.NewSession(ctx, &user.ID)
	other, _ := s.NewSession(ctx, &user.ID)
	credential := webauthn.Credential{ID: []byte("credential-one"), PublicKey: []byte("public-key")}
	if _, err := s.AddPasskey(ctx, user.ID, "MacBook Touch ID", credential); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceRecoveryCode(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePassword(ctx, user.ID, keep.Token); err != nil {
		t.Fatal(err)
	}
	status, err := s.SecurityStatus(ctx, user.ID)
	if err != nil || status.PasswordEnabled || !status.RecoveryCodeActive || len(status.Passkeys) != 1 || status.Passkeys[0].Name != "MacBook Touch ID" {
		t.Fatalf("unexpected passwordless status: %+v err=%v", status, err)
	}
	if _, err = s.Session(ctx, keep.Token); err != nil {
		t.Fatalf("current session was revoked with other sessions: %v", err)
	}
	if _, err = s.Session(ctx, other.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other session survived password removal: %v", err)
	}
	if err = s.RemovePasskey(ctx, user.ID, status.Passkeys[0].ID); !errors.Is(err, ErrLastAuthenticator) {
		t.Fatalf("last passkey removal was allowed: %v", err)
	}
	second, err := s.AddPasskey(ctx, user.ID, "Security key", webauthn.Credential{ID: []byte("credential-two"), PublicKey: []byte("public-key-two")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RemovePasskey(ctx, user.ID, second.ID); err != nil {
		t.Fatalf("second passkey could not be removed: %v", err)
	}
}

func TestFreshPasskeyVerificationIsBoundToOneSessionAndExpires(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, _ := s.Register(ctx, "fresh-user", "fresh passkey password")
	session, _ := s.NewSession(ctx, &user.ID)
	other, _ := s.NewSession(ctx, &user.ID)
	if fresh, err := s.SessionHasFreshPasskey(ctx, session.Token, 5*time.Minute); err != nil || fresh {
		t.Fatalf("unverified session was fresh: fresh=%v err=%v", fresh, err)
	}
	if err := s.MarkSessionPasskeyVerified(ctx, session.Token, user.ID); err != nil {
		t.Fatal(err)
	}
	if fresh, err := s.SessionHasFreshPasskey(ctx, session.Token, 5*time.Minute); err != nil || !fresh {
		t.Fatalf("verified session was not fresh: fresh=%v err=%v", fresh, err)
	}
	if fresh, _ := s.SessionHasFreshPasskey(ctx, other.Token, 5*time.Minute); fresh {
		t.Fatal("fresh verification leaked to another session")
	}
	if _, err := s.DB.Exec(`UPDATE sessions SET fresh_passkey_at=? WHERE token_hash=?`, time.Now().UTC().Add(-6*time.Minute).Format(time.RFC3339Nano), hashToken(session.Token)); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.SessionHasFreshPasskey(ctx, session.Token, 5*time.Minute); fresh {
		t.Fatal("expired fresh verification was accepted")
	}
}

func TestBootstrapEnrollmentTokenIsHashedExpiringAndOneUse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	expired, err := s.CreateBootstrapToken(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.DB.QueryRow(`SELECT token_hash FROM bootstrap_tokens WHERE id=1`).Scan(&stored); err != nil || stored == expired {
		t.Fatalf("bootstrap token was not hashed: stored=%q err=%v", stored, err)
	}
	if _, err = s.DB.Exec(`UPDATE bootstrap_tokens SET expires_at=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BootstrapSuperAdminWithPassword(ctx, expired, "RootUser", "bootstrap password"); !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("expired bootstrap token was accepted: %v", err)
	}
	token, err := s.CreateBootstrapToken(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.BootstrapSuperAdminWithPassword(ctx, token, "RootUser", "bootstrap password")
	if err != nil || !root.IsSuperAdmin || root.Role != "admin" {
		t.Fatalf("bootstrap enrollment failed: %+v err=%v", root, err)
	}
	if _, err = s.BootstrapSuperAdminWithPassword(ctx, token, "AnotherRoot", "another password"); !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("bootstrap token replay was accepted: %v", err)
	}
	if _, err = s.CreateBootstrapToken(ctx, time.Minute); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("bootstrap reopened after super admin creation: %v", err)
	}
}

func TestBootstrapSupportsPasskeyOnlySuperAdmin(t *testing.T) {
	s := testStore(t)
	token, _ := s.CreateBootstrapToken(context.Background(), time.Minute)
	handle := bytes.Repeat([]byte{0x42}, 32)
	credential := webauthn.Credential{ID: []byte("root-key"), PublicKey: []byte("root-public-key")}
	user, recovery, err := s.BootstrapSuperAdminWithPasskey(context.Background(), token, "PasskeyRoot", "Root key", handle, credential, "")
	if err != nil || !user.IsSuperAdmin || user.Role != "admin" || recovery == "" {
		t.Fatalf("passkey-only bootstrap failed: user=%+v recovery=%q err=%v", user, recovery, err)
	}
	security, _ := s.SecurityStatus(context.Background(), user.ID)
	if security.PasswordEnabled || len(security.Passkeys) != 1 || !security.RecoveryCodeActive {
		t.Fatalf("unexpected bootstrap security: %+v", security)
	}
}

func TestPasskeyOnlyAccountLoadsByOpaqueHandleAndCredential(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	credential := webauthn.Credential{ID: []byte("discoverable-key"), PublicKey: []byte("public-key")}
	user, recovery, err := s.CreatePasskeyOnlyAccount(ctx, "PasskeyOnly", "Phone passkey", credential)
	if err != nil || recovery == "" {
		t.Fatalf("passkey-only registration failed: user=%+v recovery=%q err=%v", user, recovery, err)
	}
	loaded, err := s.WebAuthnUser(ctx, user.ID)
	if err != nil || len(loaded.WebAuthnID()) != 32 || len(loaded.WebAuthnCredentials()) != 1 {
		t.Fatalf("WebAuthn user did not round-trip: %+v err=%v", loaded, err)
	}
	discovered, err := s.PasskeyUser(ctx, credential.ID, loaded.WebAuthnID())
	if err != nil || discovered.ID != user.ID {
		t.Fatalf("discoverable lookup failed: %+v err=%v", discovered, err)
	}
	if _, err = s.PasskeyUser(ctx, []byte("wrong-key"), loaded.WebAuthnID()); err == nil {
		t.Fatal("user handle was accepted with a credential owned by nobody")
	}
	if _, err = s.DB.Exec(`UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id=?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PasskeyUser(ctx, credential.ID, loaded.WebAuthnID()); err == nil {
		t.Fatal("suspended account authenticated with a passkey")
	}
	if _, err = s.Authenticate(ctx, "passkeyonly", "anything long enough"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("passwordless account leaked or accepted a password: %v", err)
	}
}

func TestPasskeyOnlyAccountPreservesCeremonyUserHandle(t *testing.T) {
	s := testStore(t)
	handle := bytes.Repeat([]byte{0x7a}, 32)
	credential := webauthn.Credential{ID: []byte("handle-key"), PublicKey: []byte("public-key")}
	user, _, err := s.CreatePasskeyOnlyAccountWithHandle(context.Background(), "HandleUser", "Phone", handle, credential)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.WebAuthnUser(context.Background(), user.ID)
	if err != nil || !bytes.Equal(loaded.WebAuthnID(), handle) {
		t.Fatalf("ceremony user handle changed: got=%x want=%x err=%v", loaded.WebAuthnID(), handle, err)
	}
}

func TestAuthChallengeExpiresAndCannotBeReplayedOrChanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, _ := s.Register(ctx, "challenge-user", "challenge password")
	token, err := s.CreateAuthChallenge(ctx, "add", &user.ID, []byte(`{"challenge":"value"}`), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeAuthChallenge(ctx, token, "login"); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("challenge kind could be changed: %v", err)
	}
	challenge, err := s.ConsumeAuthChallenge(ctx, token, "add")
	if err != nil || challenge.UserID == nil || *challenge.UserID != user.ID || string(challenge.Payload) != `{"challenge":"value"}` {
		t.Fatalf("valid challenge failed: %+v err=%v", challenge, err)
	}
	if _, err = s.ConsumeAuthChallenge(ctx, token, "add"); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("challenge replay was allowed: %v", err)
	}
	expired, err := s.CreateAuthChallenge(ctx, "login", nil, []byte(`{}`), -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeAuthChallenge(ctx, expired, "login"); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("expired challenge was accepted: %v", err)
	}
}

func TestAuthSecurityMigrationPreservesLegacyPasswordUser(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_initial.sql", "002_accounts_permissions.sql"} {
		body, readErr := os.ReadFile(filepath.Join("migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err = db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, name); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := hashPassword("legacy password remains")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO users(username,password_hash) VALUES('LegacyUser',?)`, hash); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Authenticate(context.Background(), "legacyuser", "legacy password remains"); err != nil {
		t.Fatalf("legacy password stopped working: %v", err)
	}
	if _, err = s.DB.Exec(`INSERT INTO users(username,password_hash,webauthn_id) VALUES('Passwordless',NULL,randomblob(32))`); err != nil {
		t.Fatalf("password_hash is not nullable after migration: %v", err)
	}
}

func TestAccountRoleAndSuperAdminBoundaries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "root", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := s.Register(ctx, "manager", "manager password long")
	viewer, _ := s.Register(ctx, "viewer", "viewer password long")
	if err = s.SetUserRole(ctx, root, manager.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	manager, _ = s.User(ctx, manager.ID)
	if err = s.SetUserRole(ctx, manager, viewer.ID, "admin"); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin promoted an admin: %v", err)
	}
	if err = s.SetUserRole(ctx, root, root.ID, "viewer"); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("super admin demoted itself without transfer: %v", err)
	}
	if err = s.TransferSuperAdmin(ctx, root, viewer.ID); err != nil {
		t.Fatal(err)
	}
	oldRoot, _ := s.User(ctx, root.ID)
	newRoot, _ := s.User(ctx, viewer.ID)
	if oldRoot.IsSuperAdmin || !newRoot.IsSuperAdmin || newRoot.Role != "admin" {
		t.Fatalf("unexpected transfer result: old=%+v new=%+v", oldRoot, newRoot)
	}
}

func TestPasswordsSessionsAndSuspensionPreserveUploadOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := s.BootstrapSuperAdmin(ctx, "root", "correct horse battery staple")
	uploader, err := s.Register(ctx, "uploader", "uploader password long")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.DB.QueryRow(`SELECT password_hash FROM users WHERE id=?`, uploader.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "uploader password long" || !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("password was not stored as Argon2id: %q", stored)
	}
	if _, err = s.Authenticate(ctx, "UPLOADER", "uploader password long"); err != nil {
		t.Fatal(err)
	}
	session, err := s.NewSession(ctx, &uploader.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id,published_at) VALUES('published','originals/a.png','thumbs/a.jpg','image/png',1,1,1,'owned',?,CURRENT_TIMESTAMP)`, uploader.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := result.LastInsertId()
	if err = s.SetUserSuspended(ctx, root, uploader.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, session.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("suspended account session remained usable: %v", err)
	}
	if _, err = s.DB.Exec(`DELETE FROM users WHERE id=?`, uploader.ID); err == nil {
		t.Fatal("database allowed an uploader account to be deleted and orphan its attribution")
	}
	var owner int64
	if err = s.DB.QueryRow(`SELECT uploader_id FROM posts WHERE id=?`, postID).Scan(&owner); err != nil || owner != uploader.ID {
		t.Fatalf("upload attribution changed after suspension: owner=%d err=%v", owner, err)
	}
}

func TestFavoritesAndDraftPoolsArePrivate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, _ := s.Register(ctx, "alice", "alice password long")
	b, _ := s.Register(ctx, "bobby", "bobby password long")
	addPost(t, s, "public", "published")
	var postID int64
	_ = s.DB.QueryRow(`SELECT id FROM posts WHERE sha256='public'`).Scan(&postID)
	if err := s.SetFavorite(ctx, a.ID, postID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, b.ID, postID, true); err != nil {
		t.Fatalf("second viewer could not favorite the same post: %v", err)
	}
	aFavorites, _ := s.Favorites(ctx, a.ID)
	bFavorites, _ := s.Favorites(ctx, b.ID)
	if len(aFavorites) != 1 || len(bFavorites) != 1 {
		t.Fatalf("favorites leaked: alice=%d bobby=%d", len(aFavorites), len(bFavorites))
	}
	pool, err := s.CreatePool(ctx, a.ID, "Secret Set", "draft", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool(ctx, pool.Slug, b.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("draft pool visible to another viewer: %v", err)
	}
	if _, err = s.Pool(ctx, pool.Slug, a.ID); err != nil {
		t.Fatalf("owner could not view draft pool: %v", err)
	}
}
