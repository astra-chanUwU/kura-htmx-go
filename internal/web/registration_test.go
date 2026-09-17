package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"kura/internal/archive"
)

func modeServer(t *testing.T, mode string) (*Server, *archive.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := archive.Open(root + "/kura.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server, err := NewWithAuth(store, root+"/media", AuthConfig{RPID: "localhost", Origins: []string{"http://localhost:8080"}, RegistrationMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func TestRegistrationModeDefaultsOpenAndRejectsUnknownValues(t *testing.T) {
	server, store := modeServer(t, "")
	if server.registrationMode != RegistrationModeOpen {
		t.Fatalf("empty registration mode=%q, want open", server.registrationMode)
	}
	if _, err := NewWithAuth(store, t.TempDir()+"/media-invalid-mode", AuthConfig{RPID: "localhost", Origins: []string{"http://localhost:8080"}, RegistrationMode: "sometimes"}); err == nil {
		t.Fatal("unknown registration mode was accepted")
	}
}

func TestRegistrationModeExplainsClosedAndInviteOnlyWithoutChangingLoginOrRecovery(t *testing.T) {
	for _, mode := range []string{"closed", "invite-only"} {
		server, store := modeServer(t, mode)
		root, err := store.BootstrapSuperAdmin(context.Background(), "mode-root-"+strings.ReplaceAll(mode, "-", ""), "mode root password")
		if err != nil {
			t.Fatal(err)
		}
		_ = root
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/register", nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("mode=%s registration status=%d, want 403", mode, response.Code)
		}
		body := response.Body.String()
		if mode == "closed" && !strings.Contains(body, "Registration is closed") {
			t.Fatalf("closed registration copy missing: %s", body)
		}
		if mode == "invite-only" && !strings.Contains(body, "invitation") {
			t.Fatalf("invite-only registration copy missing: %s", body)
		}
		if strings.Contains(body, "<a href=\"/register\">Register</a>") {
			t.Fatalf("mode=%s still exposed public registration link", mode)
		}
	}
}

func TestInvitePasswordRegistrationConsumesInviteAndUsesGenericInvalidTokenError(t *testing.T) {
	server, store := modeServer(t, "invite-only")
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-invite-root", "web invite root password")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest("GET", "/register?invite="+url.QueryEscape(invite.Token), nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "invitation") {
		t.Fatalf("invite registration form status=%d body=%s", get.Code, get.Body.String())
	}
	cookies := get.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("invite registration did not receive anonymous session")
	}
	session, err := store.Session(ctx, cookies[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	registered := sessionRequest(t, server.Handler(), "POST", "/register", url.Values{"invite": {invite.Token}, "username": {"web-invited"}, "password": {"web invited password"}}, session)
	if registered.Code != http.StatusSeeOther {
		t.Fatalf("invite registration status=%d body=%s", registered.Code, registered.Body.String())
	}
	if _, err = store.User(ctx, 2); err != nil {
		t.Fatal(err)
	}
	invalidGet := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidGet, httptest.NewRequest("GET", "/register?invite=arbitrary-invalid-token", nil))
	invalidCookie := invalidGet.Result().Cookies()[0]
	invalidSession, err := store.Session(ctx, invalidCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	invalid := sessionRequest(t, server.Handler(), "POST", "/register", url.Values{"invite": {"arbitrary-invalid-token"}, "username": {"web-invalid"}, "password": {"web invalid password"}}, invalidSession)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "The invitation is invalid or expired") || strings.Contains(invalid.Body.String(), "arbitrary-invalid-token") {
		t.Fatalf("invalid invite response status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestInvitePasskeyRegistrationSupportsPasswordlessAndBothChoices(t *testing.T) {
	server, store := modeServer(t, "invite-only")
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-passkey-root", "web passkey root password")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server.passkeys = &fakePasskeys{credential: webauthn.Credential{ID: []byte("invite-web-credential"), PublicKey: []byte("invite-web-public-key")}}
	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest("GET", "/register?invite="+url.QueryEscape(invite.Token), nil))
	cookie := get.Result().Cookies()[0]
	session, err := store.Session(ctx, cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	beginBody := `{"username":"web-passkey","name":"Browser","password":"web passkey password","invite":"` + invite.Token + `"}`
	begin := httptest.NewRequest("POST", "/auth/passkeys/register/begin", strings.NewReader(beginBody))
	begin.Header.Set("Content-Type", "application/json")
	begin.Header.Set("X-CSRF-Token", session.CSRF)
	begin.AddCookie(cookie)
	beginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(beginResponse, begin)
	if beginResponse.Code != http.StatusOK {
		t.Fatalf("invite passkey begin status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}
	var payload struct {
		ChallengeToken string `json:"challengeToken"`
	}
	if err = json.Unmarshal(beginResponse.Body.Bytes(), &payload); err != nil || payload.ChallengeToken == "" {
		t.Fatalf("invite passkey begin payload=%s err=%v", beginResponse.Body.String(), err)
	}
	finish := httptest.NewRequest("POST", "/auth/passkeys/register/finish", strings.NewReader(`{}`))
	finish.Header.Set("Content-Type", "application/json")
	finish.Header.Set("X-CSRF-Token", session.CSRF)
	finish.Header.Set("X-Kura-Challenge", payload.ChallengeToken)
	finish.AddCookie(cookie)
	finishResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusOK {
		t.Fatalf("invite passkey finish status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	status, err := store.SecurityStatus(ctx, 2)
	if err != nil || !status.PasswordEnabled {
		t.Fatalf("invited both-method account status=%+v err=%v", status, err)
	}
}

func TestSuperAdminCanCreateAndRevokeInvitationsButOtherRolesCannot(t *testing.T) {
	server, store := modeServer(t, "invite-only")
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-admin-invite-root", "web admin invite root password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "web-admin-invite-viewer", "web admin invite viewer password")
	if err != nil {
		t.Fatal(err)
	}
	viewerSession, err := store.NewSession(ctx, &viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	denied := sessionRequest(t, server.Handler(), "POST", "/admin/invites", url.Values{"expires_minutes": {"30"}}, viewerSession)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer invite creation status=%d body=%s", denied.Code, denied.Body.String())
	}
	rootSession, err := store.NewSession(ctx, &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	created := sessionRequest(t, server.Handler(), "POST", "/admin/invites", url.Values{"expires_minutes": {"30"}}, rootSession)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "/register?invite=") {
		t.Fatalf("super-admin invite creation status=%d body=%s", created.Code, created.Body.String())
	}
	var tokenHash string
	if err = store.DB.QueryRowContext(ctx, `SELECT token_hash FROM registration_invites ORDER BY id DESC LIMIT 1`).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created.Body.String(), tokenHash) {
		t.Fatalf("invite page exposed stored hash")
	}
	var inviteID int64
	if err = store.DB.QueryRowContext(ctx, `SELECT id FROM registration_invites ORDER BY id DESC LIMIT 1`).Scan(&inviteID); err != nil {
		t.Fatal(err)
	}
	revoked := sessionRequest(t, server.Handler(), "POST", "/admin/invites/"+strconv.FormatInt(inviteID, 10)+"/revoke", nil, rootSession)
	if revoked.Code != http.StatusSeeOther {
		t.Fatalf("super-admin invite revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}
