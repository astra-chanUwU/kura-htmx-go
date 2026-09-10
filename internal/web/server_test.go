package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"kura/internal/archive"
)

type fakePasskeys struct {
	user       archive.User
	credential webauthn.Credential
}

func (f *fakePasskeys) BeginRegistration(archive.User) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	return &protocol.CredentialCreation{}, &webauthn.SessionData{Challenge: "register", Expires: time.Now().Add(time.Minute)}, nil
}
func (f *fakePasskeys) FinishRegistration(archive.User, webauthn.SessionData, *http.Request) (*webauthn.Credential, error) {
	return &f.credential, nil
}
func (f *fakePasskeys) BeginDiscoverableLogin() (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return &protocol.CredentialAssertion{}, &webauthn.SessionData{Challenge: "login", Expires: time.Now().Add(time.Minute)}, nil
}
func (f *fakePasskeys) FinishPasskeyLogin(webauthn.DiscoverableUserHandler, webauthn.SessionData, *http.Request) (webauthn.User, *webauthn.Credential, error) {
	return f.user, &f.credential, nil
}
func (f *fakePasskeys) BeginLogin(archive.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return &protocol.CredentialAssertion{}, &webauthn.SessionData{Challenge: "fresh", Expires: time.Now().Add(time.Minute)}, nil
}
func (f *fakePasskeys) FinishLogin(archive.User, webauthn.SessionData, *http.Request) (*webauthn.Credential, error) {
	return &f.credential, nil
}

func TestPasskeyRegistrationBeginUsesStrictConfiguredRPAndServerSideChallenge(t *testing.T) {
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewWithAuth(store, filepath.Join(root, "media"), AuthConfig{
		RPID: "localhost", Origins: []string{"http://localhost:8080"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/register", nil))
	cookie := get.Result().Cookies()[0]
	session, err := store.Session(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"username":"PasskeyUser","name":"MacBook Touch ID"}`)
	request := httptest.NewRequest("POST", "/auth/passkeys/register/begin", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.CSRF)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		ChallengeToken string         `json:"challengeToken"`
		Options        map[string]any `json:"options"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	publicKey, _ := payload.Options["publicKey"].(map[string]any)
	rp, _ := publicKey["rp"].(map[string]any)
	if payload.ChallengeToken == "" || rp["id"] != "localhost" {
		t.Fatalf("unexpected begin payload: %+v", payload)
	}
	challenge, err := store.ConsumeAuthChallenge(context.Background(), payload.ChallengeToken, "register")
	if err != nil || bytes.Contains(challenge.Payload, []byte(payload.ChallengeToken)) {
		t.Fatalf("challenge was not stored opaquely: %+v err=%v", challenge, err)
	}
}

func TestPasskeyConfigurationRejectsOriginOutsideRPID(t *testing.T) {
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = NewWithAuth(store, filepath.Join(root, "media"), AuthConfig{RPID: "kura.example", Origins: []string{"https://evil.example"}}); err == nil {
		t.Fatal("WebAuthn accepted an origin outside the configured RP ID")
	}
}

func TestJSONPasskeyEndpointsRequireCSRFHeader(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest("POST", "/auth/passkeys/login/begin", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("JSON request without CSRF status=%d, want 403", response.Code)
	}
}

func TestPasskeyLoginCreatesOrdinaryKuraSession(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "passkey-login", "password login backup")
	credential := webauthn.Credential{ID: []byte("login-credential"), PublicKey: []byte("public-key")}
	if _, err := store.AddPasskey(ctx, user.ID, "Test passkey", credential); err != nil {
		t.Fatal(err)
	}
	authUser, _ := store.WebAuthnUser(ctx, user.ID)
	server.passkeys = &fakePasskeys{user: authUser, credential: credential}
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/login", nil))
	anonCookie := get.Result().Cookies()[0]
	anonSession, _ := store.Session(ctx, anonCookie.Value)

	begin := httptest.NewRequest("POST", "/auth/passkeys/login/begin", strings.NewReader(`{}`))
	begin.Header.Set("Content-Type", "application/json")
	begin.Header.Set("X-CSRF-Token", anonSession.CSRF)
	begin.AddCookie(anonCookie)
	beginResponse := httptest.NewRecorder()
	handler.ServeHTTP(beginResponse, begin)
	var begun struct {
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(beginResponse.Body.Bytes(), &begun)
	if beginResponse.Code != http.StatusOK || begun.ChallengeToken == "" {
		t.Fatalf("login begin failed: status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}

	finish := httptest.NewRequest("POST", "/auth/passkeys/login/finish", strings.NewReader(`{}`))
	finish.Header.Set("Content-Type", "application/json")
	finish.Header.Set("X-CSRF-Token", anonSession.CSRF)
	finish.Header.Set("X-Kura-Challenge", begun.ChallengeToken)
	finish.AddCookie(anonCookie)
	finishResponse := httptest.NewRecorder()
	handler.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusOK {
		t.Fatalf("login finish failed: status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	cookies := finishResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("passkey login did not set a session cookie")
	}
	authenticated, err := store.Session(ctx, cookies[0].Value)
	if err != nil || authenticated.User == nil || authenticated.User.ID != user.ID {
		t.Fatalf("passkey login session missing: %+v err=%v", authenticated, err)
	}
}

func TestPasswordRemovalRequiresFreshPasskeyAndRecovery(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "remove-password", "password to remove")
	credential := webauthn.Credential{ID: []byte("remove-credential"), PublicKey: []byte("public-key")}
	key, _ := store.AddPasskey(ctx, user.ID, "MacBook", credential)
	if key.ID == 0 {
		t.Fatal("test passkey was not stored")
	}
	_, _ = store.ReplaceRecoveryCode(ctx, user.ID)
	authUser, _ := store.WebAuthnUser(ctx, user.ID)
	server.passkeys = &fakePasskeys{user: authUser, credential: credential}
	session, _ := store.NewSession(ctx, &user.ID)

	denied := sessionRequest(t, server.Handler(), "POST", "/account/password/remove", url.Values{"confirmation": {"remove"}}, session)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("password removed without fresh passkey: status=%d body=%s", denied.Code, denied.Body.String())
	}

	beginRequest := httptest.NewRequest("POST", "/auth/passkeys/fresh/begin", strings.NewReader(`{}`))
	beginRequest.Header.Set("Content-Type", "application/json")
	beginRequest.Header.Set("X-CSRF-Token", session.CSRF)
	beginRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	beginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(beginResponse, beginRequest)
	var begun struct {
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(beginResponse.Body.Bytes(), &begun)
	if beginResponse.Code != http.StatusOK || begun.ChallengeToken == "" {
		t.Fatalf("fresh verification begin failed: status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}
	finishRequest := httptest.NewRequest("POST", "/auth/passkeys/fresh/finish", strings.NewReader(`{}`))
	finishRequest.Header.Set("Content-Type", "application/json")
	finishRequest.Header.Set("X-CSRF-Token", session.CSRF)
	finishRequest.Header.Set("X-Kura-Challenge", begun.ChallengeToken)
	finishRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	finishResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(finishResponse, finishRequest)
	if finishResponse.Code != http.StatusOK {
		t.Fatalf("fresh verification failed: status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}

	removed := sessionRequest(t, server.Handler(), "POST", "/account/password/remove", url.Values{"confirmation": {"remove"}}, session)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("fresh password removal failed: status=%d body=%s", removed.Code, removed.Body.String())
	}
	status, err := store.SecurityStatus(ctx, user.ID)
	if err != nil || status.PasswordEnabled {
		t.Fatalf("password remained enabled: %+v err=%v", status, err)
	}
}

func TestAuthenticatedUserAddsFirstPasskeyAndReceivesRecoveryCode(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "add-passkey", "password stays enabled")
	credential := webauthn.Credential{ID: []byte("new-credential"), PublicKey: []byte("public-key")}
	authUser, _ := store.WebAuthnUser(ctx, user.ID)
	server.passkeys = &fakePasskeys{user: authUser, credential: credential}
	session, _ := store.NewSession(ctx, &user.ID)
	cookie := &http.Cookie{Name: sessionCookie, Value: session.Token}

	begin := httptest.NewRequest("POST", "/account/passkeys/add/begin", strings.NewReader(`{"name":"Bitwarden"}`))
	begin.Header.Set("Content-Type", "application/json")
	begin.Header.Set("X-CSRF-Token", session.CSRF)
	begin.AddCookie(cookie)
	beginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(beginResponse, begin)
	var begun struct {
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(beginResponse.Body.Bytes(), &begun)
	if beginResponse.Code != http.StatusOK || begun.ChallengeToken == "" {
		t.Fatalf("add begin failed: status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}

	finish := httptest.NewRequest("POST", "/account/passkeys/add/finish", strings.NewReader(`{}`))
	finish.Header.Set("Content-Type", "application/json")
	finish.Header.Set("X-CSRF-Token", session.CSRF)
	finish.Header.Set("X-Kura-Challenge", begun.ChallengeToken)
	finish.AddCookie(cookie)
	finishResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(finishResponse, finish)
	var result struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	_ = json.Unmarshal(finishResponse.Body.Bytes(), &result)
	security, _ := store.SecurityStatus(ctx, user.ID)
	if finishResponse.Code != http.StatusOK || result.RecoveryCode == "" || len(security.Passkeys) != 1 || security.Passkeys[0].Name != "Bitwarden" || !security.RecoveryCodeActive {
		t.Fatalf("add finish failed: status=%d result=%+v security=%+v body=%s", finishResponse.Code, result, security, finishResponse.Body.String())
	}
}

func TestRecoveryReplacesCodeRevokesSessionsAndSignsUserIn(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "web-recovery", "original web password")
	oldSession, _ := store.NewSession(ctx, &user.ID)
	code, _ := store.ReplaceRecoveryCode(ctx, user.ID)
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/recover", nil))
	anonCookie := get.Result().Cookies()[0]
	anonSession, _ := store.Session(ctx, anonCookie.Value)
	response := sessionRequest(t, handler, "POST", "/recover", url.Values{
		"username": {"WEB-RECOVERY"}, "recovery_code": {code}, "password": {"replacement web password"},
	}, anonSession)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Save your replacement recovery code") || !strings.Contains(response.Body.String(), "data-download-recovery") {
		t.Fatalf("recovery response status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := store.Session(ctx, oldSession.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old session survived recovery: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("recovery did not sign the user in")
	}
	recovered, err := store.Session(ctx, cookies[0].Value)
	if err != nil || recovered.User == nil || recovered.User.ID != user.ID {
		t.Fatalf("recovery session missing: %+v err=%v", recovered, err)
	}
}

func TestPasswordLoginRateLimitUsesGenericFailure(t *testing.T) {
	server, store := testServer(t)
	_, _ = store.Register(context.Background(), "rate-user", "correct rate password")
	server.loginLimiter = newAttemptLimiter(2, time.Minute)
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/login", nil))
	cookie := get.Result().Cookies()[0]
	session, _ := store.Session(context.Background(), cookie.Value)
	for i := 0; i < 2; i++ {
		response := sessionRequest(t, handler, "POST", "/login", url.Values{"username": {"rate-user"}, "password": {"wrong password here"}}, session)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), archive.ErrInvalidLogin.Error()) {
			t.Fatalf("attempt %d leaked a distinct failure: status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	blocked := sessionRequest(t, handler, "POST", "/login", url.Values{"username": {"rate-user"}, "password": {"correct rate password"}}, session)
	if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Body.String(), archive.ErrInvalidLogin.Error()) {
		t.Fatalf("rate limit response was not generic: status=%d body=%s", blocked.Code, blocked.Body.String())
	}
}

func TestAuthenticationAndAccountSecurityUIExposePasskeyFirstFlows(t *testing.T) {
	server, store := testServer(t)
	handler := server.Handler()
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest("GET", "/login", nil))
	loginHTML := login.Body.String()
	passkeyAt := strings.Index(loginHTML, `data-passkey-action="login"`)
	passwordAt := strings.Index(loginHTML, `autocomplete="current-password"`)
	if passkeyAt < 0 || passwordAt < 0 || passkeyAt > passwordAt || !strings.Contains(loginHTML, `href="/recover"`) {
		t.Fatalf("sign-in is not passkey-first with recovery: %s", loginHTML)
	}
	register := httptest.NewRecorder()
	handler.ServeHTTP(register, httptest.NewRequest("GET", "/register", nil))
	if body := register.Body.String(); !strings.Contains(body, `data-passkey-action="register"`) || !strings.Contains(body, `minlength="15"`) || !strings.Contains(body, `maxlength="128"`) {
		t.Fatalf("registration does not offer both approved methods: %s", body)
	}

	user, _ := store.Register(context.Background(), "security-ui", "security ui password")
	_, _ = store.AddPasskey(context.Background(), user.ID, "Bitwarden", webauthn.Credential{ID: []byte("ui-key"), PublicKey: []byte("public-key")})
	_, _ = store.ReplaceRecoveryCode(context.Background(), user.ID)
	session, _ := store.NewSession(context.Background(), &user.ID)
	account := sessionRequest(t, handler, "GET", "/account", nil, session)
	body := account.Body.String()
	for _, want := range []string{"Account security", "Password enabled", "Bitwarden", "Recovery code active", `data-passkey-action="add"`, `data-passkey-action="fresh"`, "data-download-recovery"} {
		if !strings.Contains(body, want) {
			t.Fatalf("account security UI missing %q: %s", want, body)
		}
	}
	if !strings.Contains(body, `class="account-status password-enabled"`) {
		t.Fatalf("password-enabled state is not semantically styled: %s", body)
	}
	if strings.Contains(body, "hx-push-url") {
		t.Fatal("account inline security actions change browser history")
	}
	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest("GET", "/static/passkeys.js", nil))
	if script.Code != http.StatusOK || !strings.Contains(script.Body.String(), "data-copy-recovery") || !strings.Contains(script.Body.String(), "navigator.clipboard") {
		t.Fatalf("recovery code copy control is unavailable: status=%d", script.Code)
	}
}

func TestAuthFormsUseArchiveSheetStructure(t *testing.T) {
	server, _ := testServer(t)
	for _, path := range []string{"/login", "/register"} {
		page := httptest.NewRecorder()
		server.Handler().ServeHTTP(page, httptest.NewRequest("GET", path, nil))
		body := page.Body.String()
		if page.Code != http.StatusOK || !strings.Contains(body, `<main id="main" class="form-page auth-page">`) || !strings.Contains(body, `auth-fields`) {
			t.Fatalf("%s does not use the archive form structure: status=%d body=%s", path, page.Code, body)
		}
	}
	css := httptest.NewRecorder()
	server.Handler().ServeHTTP(css, httptest.NewRequest("GET", "/static/kura.css", nil))
	if !strings.Contains(css.Body.String(), ".auth-fields label{display:grid;gap:5px;align-items:initial}") || !strings.Contains(css.Body.String(), ".auth-fields button{grid-column:auto;justify-self:start}") {
		t.Fatalf("auth form still depends on nested grid placement: %s", css.Body.String())
	}
	styles := css.Body.String()
	for _, want := range []string{
		".auth-page .badge{border:0;padding:0;color:var(--mint)",
		".auth-page .auth-method button{color:var(--mint);font-weight:700",
		".auth-page .auth-divider{display:flex;align-items:center;gap:12px",
	} {
		if !strings.Contains(styles, want) {
			t.Fatalf("auth refinement missing %q: %s", want, styles)
		}
	}
}

func TestBrowseSearchControlIsCompact(t *testing.T) {
	server, _ := testServer(t)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest("GET", "/posts", nil))
	body := page.Body.String()
	for _, want := range []string{`class="visually-hidden" for="q">Search tags</label>`, `placeholder="search tags…"`, `<button class="search-submit">Go</button>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("browse search control missing %q: %s", want, body)
		}
	}
}

func TestAccountUsesFramedEditorialSections(t *testing.T) {
	server, store := testServer(t)
	user, err := store.Register(context.Background(), "account-layout", "account layout password")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(context.Background(), &user.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionRequest(t, server.Handler(), "GET", "/account", nil, session)
	body := page.Body.String()
	for _, want := range []string{`class="narrow account-page"`, `class="security-panel account-section"`, `class="account-grid account-sections"`, `class="account-section"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("account page missing %q: %s", want, body)
		}
	}
	css := httptest.NewRecorder()
	server.Handler().ServeHTTP(css, httptest.NewRequest("GET", "/static/kura.css", nil))
	if !strings.Contains(css.Body.String(), ".account-page{max-width:680px;margin:22px auto;background:none;border:0;padding:0") {
		t.Fatalf("account page still uses a framed dashboard surface: %s", css.Body.String())
	}
}

func TestRecoveryUsesAuthPageStructure(t *testing.T) {
	server, _ := testServer(t)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest("GET", "/recover", nil))
	body := page.Body.String()
	for _, want := range []string{`class="form-page auth-page"`, `class="auth-method passkey-first"`, `class="stack auth-fields"`, `class="auth-divider"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("recovery page missing %q: %s", want, body)
		}
	}
}

func TestPasskeyRemovalHasHTMXFragmentAndRedirectFallback(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "remove-keys", "password remains here")
	first, _ := store.AddPasskey(ctx, user.ID, "First", webauthn.Credential{ID: []byte("first-key"), PublicKey: []byte("public-one")})
	second, _ := store.AddPasskey(ctx, user.ID, "Second", webauthn.Credential{ID: []byte("second-key"), PublicKey: []byte("public-two")})
	session, _ := store.NewSession(ctx, &user.ID)

	values := url.Values{"csrf": {session.CSRF}}
	htmxRequest := httptest.NewRequest("POST", "/account/passkeys/"+strconv.FormatInt(first.ID, 10)+"/remove", strings.NewReader(values.Encode()))
	htmxRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	htmxRequest.Header.Set("HX-Request", "true")
	htmxRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	htmxResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(htmxResponse, htmxRequest)
	if htmxResponse.Code != http.StatusOK || htmxResponse.Header().Get("Location") != "" || htmxResponse.Body.Len() != 0 {
		t.Fatalf("HTMX removal navigated or returned stale content: status=%d location=%q body=%s", htmxResponse.Code, htmxResponse.Header().Get("Location"), htmxResponse.Body.String())
	}
	fallback := sessionRequest(t, server.Handler(), "POST", "/account/passkeys/"+strconv.FormatInt(second.ID, 10)+"/remove", nil, session)
	if fallback.Code != http.StatusSeeOther || fallback.Header().Get("Location") != "/account?notice=passkey-removed" {
		t.Fatalf("fallback removal status=%d location=%q", fallback.Code, fallback.Header().Get("Location"))
	}
}

func TestBootstrapSetupURLCreatesFirstSuperAdminOnce(t *testing.T) {
	server, store := testServer(t)
	token, err := store.CreateBootstrapToken(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/setup?token="+url.QueryEscape(token), nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "Passwordless passkey") || !strings.Contains(get.Body.String(), "Passkey plus password") {
		t.Fatalf("setup choices missing: status=%d body=%s", get.Code, get.Body.String())
	}
	cookie := get.Result().Cookies()[0]
	session, _ := store.Session(context.Background(), cookie.Value)
	created := sessionRequest(t, handler, "POST", "/setup/password", url.Values{
		"token": {token}, "username": {"FirstRoot"}, "password": {"initial root password"},
	}, session)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/account" {
		t.Fatalf("password bootstrap failed: status=%d body=%s", created.Code, created.Body.String())
	}
	cookies := created.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("bootstrap did not create session")
	}
	authenticated, err := store.Session(context.Background(), cookies[0].Value)
	if err != nil || authenticated.User == nil || !authenticated.User.IsSuperAdmin {
		t.Fatalf("bootstrap session is not super admin: %+v err=%v", authenticated, err)
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest("GET", "/setup?token="+url.QueryEscape(token), nil))
	if replay.Code != http.StatusGone {
		t.Fatalf("used setup URL status=%d, want 410", replay.Code)
	}
}

func TestBootstrapPasskeyOnlyCreatesSuperAdminSession(t *testing.T) {
	server, store := testServer(t)
	token, _ := store.CreateBootstrapToken(context.Background(), time.Minute)
	credential := webauthn.Credential{ID: []byte("bootstrap-key"), PublicKey: []byte("bootstrap-public")}
	server.passkeys = &fakePasskeys{credential: credential}
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/setup?token="+url.QueryEscape(token), nil))
	cookie := get.Result().Cookies()[0]
	session, _ := store.Session(context.Background(), cookie.Value)

	begin := httptest.NewRequest("POST", "/setup/passkey/begin", strings.NewReader(`{"username":"KeyRoot","name":"Root key"}`))
	begin.Header.Set("Content-Type", "application/json")
	begin.Header.Set("X-CSRF-Token", session.CSRF)
	begin.Header.Set("X-Kura-Bootstrap", token)
	begin.AddCookie(cookie)
	beginResponse := httptest.NewRecorder()
	handler.ServeHTTP(beginResponse, begin)
	var begun struct {
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(beginResponse.Body.Bytes(), &begun)
	if beginResponse.Code != http.StatusOK || begun.ChallengeToken == "" {
		t.Fatalf("bootstrap begin failed: status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}
	finish := httptest.NewRequest("POST", "/setup/passkey/finish", strings.NewReader(`{}`))
	finish.Header.Set("Content-Type", "application/json")
	finish.Header.Set("X-CSRF-Token", session.CSRF)
	finish.Header.Set("X-Kura-Bootstrap", token)
	finish.Header.Set("X-Kura-Challenge", begun.ChallengeToken)
	finish.AddCookie(cookie)
	finishResponse := httptest.NewRecorder()
	handler.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusOK || !strings.Contains(finishResponse.Body.String(), "recoveryCode") {
		t.Fatalf("bootstrap finish failed: status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	cookies := finishResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("bootstrap passkey did not set a session")
	}
	authenticated, err := store.Session(context.Background(), cookies[0].Value)
	if err != nil || authenticated.User == nil || !authenticated.User.IsSuperAdmin {
		t.Fatalf("passkey bootstrap session is not super admin: %+v err=%v", authenticated, err)
	}
}

func TestRecoveryCanEstablishPasskeyAndNewSession(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, _ := store.Register(ctx, "recover-passkey", "original recovery password")
	oldSession, _ := store.NewSession(ctx, &user.ID)
	code, _ := store.ReplaceRecoveryCode(ctx, user.ID)
	credential := webauthn.Credential{ID: []byte("recovery-passkey"), PublicKey: []byte("recovery-public")}
	server.passkeys = &fakePasskeys{credential: credential}
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/recover", nil))
	cookie := get.Result().Cookies()[0]
	session, _ := store.Session(ctx, cookie.Value)
	beginBody, _ := json.Marshal(map[string]string{"username": "recover-passkey", "recoveryCode": code, "name": "Recovered key"})
	begin := httptest.NewRequest("POST", "/recover/passkey/begin", bytes.NewReader(beginBody))
	begin.Header.Set("Content-Type", "application/json")
	begin.Header.Set("X-CSRF-Token", session.CSRF)
	begin.AddCookie(cookie)
	beginResponse := httptest.NewRecorder()
	handler.ServeHTTP(beginResponse, begin)
	var begun struct {
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(beginResponse.Body.Bytes(), &begun)
	if beginResponse.Code != http.StatusOK || begun.ChallengeToken == "" {
		t.Fatalf("recovery passkey begin failed: status=%d body=%s", beginResponse.Code, beginResponse.Body.String())
	}
	finish := httptest.NewRequest("POST", "/recover/passkey/finish", strings.NewReader(`{}`))
	finish.Header.Set("Content-Type", "application/json")
	finish.Header.Set("X-CSRF-Token", session.CSRF)
	finish.Header.Set("X-Kura-Challenge", begun.ChallengeToken)
	finish.AddCookie(cookie)
	finishResponse := httptest.NewRecorder()
	handler.ServeHTTP(finishResponse, finish)
	if finishResponse.Code != http.StatusOK || !strings.Contains(finishResponse.Body.String(), "recoveryCode") {
		t.Fatalf("recovery passkey finish failed: status=%d body=%s", finishResponse.Code, finishResponse.Body.String())
	}
	if _, err := store.Session(ctx, oldSession.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old session survived passkey recovery: %v", err)
	}
	cookies := finishResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("passkey recovery did not sign the user in")
	}
	authenticated, err := store.Session(ctx, cookies[0].Value)
	if err != nil || authenticated.User == nil || authenticated.User.ID != user.ID {
		t.Fatalf("passkey recovery session missing: %+v err=%v", authenticated, err)
	}
}

func TestTemplatesUseVendoredHTMX(t *testing.T) {
	body, err := assets.ReadFile("templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, `src="/static/htmx.min.js"`) {
		t.Error("base template does not reference the vendored HTMX asset")
	}
	if strings.Contains(html, "unpkg.com") {
		t.Error("base template still references the unpkg CDN")
	}
	if _, err := assets.ReadFile("static/htmx.min.js"); err != nil {
		t.Errorf("vendored HTMX asset is missing: %v", err)
	}
}

func TestUploadPreviewBlobURLsAreAllowedByContentSecurityPolicy(t *testing.T) {
	server, _ := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/", nil))

	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "img-src 'self' data: blob:") {
		t.Fatalf("image Content-Security-Policy %q does not allow local blob previews", policy)
	}
}

func testServer(t *testing.T) (*Server, *archive.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server, err := New(store, filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func sessionRequest(t *testing.T, handler http.Handler, method, path string, values url.Values, session archive.Session) *httptest.ResponseRecorder {
	t.Helper()
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf", session.CSRF)
	request := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestRegistrationRequiresCSRFAndCreatesViewerSession(t *testing.T) {
	server, store := testServer(t)
	handler := server.Handler()
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/register", nil))
	cookies := get.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Secure {
		t.Fatalf("local session cookie flags are wrong: %+v", cookies)
	}
	session, err := store.Session(context.Background(), cookies[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	without := httptest.NewRequest("POST", "/register", strings.NewReader("username=alice&password=alice+password+long"))
	without.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	without.AddCookie(cookies[0])
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, without)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("registration without CSRF status=%d, want 403", rejected.Code)
	}
	registered := sessionRequest(t, handler, "POST", "/register", url.Values{"username": {"alice"}, "password": {"alice password long"}}, session)
	if registered.Code != http.StatusSeeOther {
		t.Fatalf("registration status=%d body=%s", registered.Code, registered.Body.String())
	}
	var role string
	if err = store.DB.QueryRow(`SELECT role FROM users WHERE username='alice'`).Scan(&role); err != nil || role != "viewer" {
		t.Fatalf("self-registration role=%q err=%v", role, err)
	}
	viewerCookie := registered.Result().Cookies()[0]
	viewerSession, err := store.Session(context.Background(), viewerCookie.Value)
	if err != nil || viewerSession.User == nil {
		t.Fatalf("rotated authenticated session missing: %+v %v", viewerSession, err)
	}
	denied := sessionRequest(t, handler, "GET", "/uploads/new", nil, viewerSession)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer upload status=%d, want 403", denied.Code)
	}
	if regexp.MustCompile(`name="csrf" value="[^"]+"`).FindString(get.Body.String()) == "" {
		t.Fatal("registration form did not render a CSRF token")
	}
}

func TestUploadFormDefaultsToPublishedAndUsesWorkSurface(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	moderator, err := store.BootstrapSuperAdmin(ctx, "upload-admin", "upload admin password")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &moderator.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionRequest(t, server.Handler(), "GET", "/uploads/new", nil, session)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `<main id="main" class="form-page upload-page">`) || !strings.Contains(body, `<section class="upload-section">`) || !strings.Contains(body, `<label class="dropzone"`) {
		t.Fatalf("upload form is missing its work-surface structure: status=%d body=%s", page.Code, body)
	}
	if strings.Contains(body, `class="form-page wide-form upload-page"`) || strings.Contains(body, `class="upload-page framed-surface"`) {
		t.Fatalf("upload form still uses the old framed layout: %s", body)
	}
	if !strings.Contains(body, `<div class="upload-fields" aria-label="Image details">`) || !strings.Contains(body, `<button class="primary-action" type="submit">Store image</button>`) {
		t.Fatalf("upload form is missing its clean details and action structure: %s", body)
	}
	if !strings.Contains(body, `<option value="published" selected>Published — visible immediately</option>`) {
		t.Fatalf("upload form does not default to published: %s", body)
	}
}

func TestNewPoolUsesCleanFormHierarchy(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	user, err := store.Register(ctx, "pool-layout", "pool layout password")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &user.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionRequest(t, server.Handler(), "GET", "/pools/new", nil, session)
	body := page.Body.String()
	for _, want := range []string{`<main id="main" class="pool-editor form-page pool-form-page">`, `<header class="pool-header">`, `<form id="pool-form"`, `<section class="pool-metadata">`, `<section class="selected-section pool-section">`, `<section class="picker-section pool-section">`, `<button form="pool-form" class="primary-action" type="submit">Save pool</button>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("new pool page missing %q: %s", want, body)
		}
	}
	css := httptest.NewRecorder()
	server.Handler().ServeHTTP(css, httptest.NewRequest("GET", "/static/kura.css", nil))
	if !strings.Contains(css.Body.String(), ".pool-form-page{max-width:680px;margin:22px auto") {
		t.Fatalf("new pool page is missing the clean form layout: %s", css.Body.String())
	}
}

func TestModeratorCannotDeleteAnotherUsersUploadButAdminCan(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, _ := store.BootstrapSuperAdmin(ctx, "root", "correct horse battery staple")
	owner, _ := store.Register(ctx, "owner", "owner password long")
	other, _ := store.Register(ctx, "other", "other password long")
	if err := store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id,published_at) VALUES('published','originals/owned.png','thumbs/owned.jpg','image/png',1,1,1,'owned',?,CURRENT_TIMESTAMP)`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	handler := server.Handler()
	otherSession, _ := store.NewSession(ctx, &other.ID)
	denied := sessionRequest(t, handler, "POST", "/posts/"+strconv.FormatInt(id, 10)+"/delete", nil, otherSession)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-owner moderator delete status=%d, want 403", denied.Code)
	}
	var deleted any
	if err = store.DB.QueryRow(`SELECT deleted_at FROM posts WHERE id=?`, id).Scan(&deleted); err != nil || deleted != nil {
		t.Fatalf("post changed after denied deletion: %v %v", deleted, err)
	}
	rootSession, _ := store.NewSession(ctx, &root.ID)
	allowed := sessionRequest(t, handler, "POST", "/posts/"+strconv.FormatInt(id, 10)+"/delete", nil, rootSession)
	if allowed.Code != http.StatusSeeOther {
		t.Fatalf("admin deletion status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestViewerAddsPostToOwnedPoolFromPostPage(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	viewer, err := store.Register(ctx, "pool-viewer", "pool viewer password")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/pool.png','thumbs/pool.jpg','image/png',1,1,1,'pool-post',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := result.LastInsertId()
	pool, err := store.CreatePool(ctx, viewer.ID, "My Visual Pool", "", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	session, _ := store.NewSession(ctx, &viewer.ID)
	handler := server.Handler()
	page := sessionRequest(t, handler, "GET", "/posts/"+strconv.FormatInt(postID, 10), nil, session)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Add to pool") || !strings.Contains(page.Body.String(), pool.Name) || !strings.Contains(page.Body.String(), `hx-post="/posts/`+strconv.FormatInt(postID, 10)+`/pools"`) {
		t.Fatalf("post page lacks the visual pool action: status=%d body=%s", page.Code, page.Body.String())
	}
	values := url.Values{"pool": {pool.Slug}, "csrf": {session.CSRF}}
	request := httptest.NewRequest("POST", "/posts/"+strconv.FormatInt(postID, 10)+"/pools", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	added := httptest.NewRecorder()
	handler.ServeHTTP(added, request)
	if added.Code != http.StatusOK || added.Header().Get("Location") != "" || !strings.Contains(added.Body.String(), "· added") {
		t.Fatalf("HTMX add-to-pool response status=%d location=%q body=%s", added.Code, added.Header().Get("Location"), added.Body.String())
	}
	updated, err := store.Pool(ctx, pool.Slug, viewer.ID)
	if err != nil || len(updated.Posts) != 1 || updated.Posts[0].ID != postID {
		t.Fatalf("post was not added to pool: %+v err=%v", updated, err)
	}
}

func TestActionStylesAreTextFirst(t *testing.T) {
	server, _ := testServer(t)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest("GET", "/static/kura.css", nil))
	css := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("stylesheet status=%d", page.Code)
	}
	if !strings.Contains(css, "button{cursor:pointer;padding:0;border:0;background:none") {
		t.Fatalf("buttons do not use the text-first base treatment: %s", css)
	}
	if !strings.Contains(css, "button:hover{color:var(--link);text-decoration:none;background:var(--soft)") {
		t.Fatalf("buttons do not have a visible hover treatment: %s", css)
	}
	if !strings.Contains(css, ".button{display:inline-block;padding:0;border:0;background:none") {
		t.Fatalf("button links do not use the text-first treatment: %s", css)
	}
	if !strings.Contains(css, "a:hover{color:var(--accent);text-decoration:none}") {
		t.Fatalf("links still underline on hover: %s", css)
	}
	if !strings.Contains(css, ".pool-action button{color:var(--mint);font-weight:700") {
		t.Fatalf("pool action does not have a distinct text treatment: %s", css)
	}
}

func TestFavoriteHTMXUpdatesControlWithoutNavigation(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	viewer, _ := store.Register(ctx, "favorite-viewer", "favorite viewer password")
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/favorite.png','thumbs/favorite.jpg','image/png',1,1,1,'favorite-post',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	session, _ := store.NewSession(ctx, &viewer.ID)
	handler := server.Handler()
	page := sessionRequest(t, handler, "GET", "/posts/"+strconv.FormatInt(id, 10), nil, session)
	if !strings.Contains(page.Body.String(), `hx-post="/posts/`+strconv.FormatInt(id, 10)+`/favorite"`) || strings.Contains(page.Body.String(), "hx-push-url") {
		t.Fatalf("favorite control is not history-neutral HTMX: %s", page.Body.String())
	}
	values := url.Values{"favorite": {"1"}, "csrf": {session.CSRF}}
	request := httptest.NewRequest("POST", "/posts/"+strconv.FormatInt(id, 10)+"/favorite", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "Remove favorite") {
		t.Fatalf("HTMX favorite response status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestAdminHTMXUpdatesAccountListWithoutNavigation(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, _ := store.BootstrapSuperAdmin(ctx, "admin-root", "admin root password")
	target, _ := store.Register(ctx, "role-target", "role target password")
	session, _ := store.NewSession(ctx, &root.ID)
	handler := server.Handler()
	page := sessionRequest(t, handler, "GET", "/admin/accounts", nil, session)
	rolePath := "/admin/accounts/" + strconv.FormatInt(target.ID, 10) + "/role"
	if !strings.Contains(page.Body.String(), `hx-post="`+rolePath+`"`) || strings.Contains(page.Body.String(), "hx-push-url") {
		t.Fatalf("admin controls are not history-neutral HTMX: %s", page.Body.String())
	}
	values := url.Values{"role": {"moderator"}, "csrf": {session.CSRF}}
	request := httptest.NewRequest("POST", rolePath, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "role-target") || !strings.Contains(response.Body.String(), "moderator") {
		t.Fatalf("HTMX admin response status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	transferPath := "/admin/accounts/" + strconv.FormatInt(target.ID, 10) + "/transfer"
	transferValues := url.Values{"csrf": {session.CSRF}}
	transferRequest := httptest.NewRequest("POST", transferPath, strings.NewReader(transferValues.Encode()))
	transferRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	transferRequest.Header.Set("HX-Request", "true")
	transferRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	transfer := httptest.NewRecorder()
	handler.ServeHTTP(transfer, transferRequest)
	formerSuperRolePath := `/admin/accounts/` + strconv.FormatInt(root.ID, 10) + `/role`
	if transfer.Code != http.StatusOK || strings.Contains(transfer.Body.String(), formerSuperRolePath) {
		t.Fatalf("super-admin transfer rendered stale privileges: status=%d body=%s", transfer.Code, transfer.Body.String())
	}
}

func TestPoolPickerReturnsFilteredPublishedThumbnails(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	viewer, _ := store.Register(ctx, "picker-viewer", "picker viewer password")
	blue, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/blue.png','thumbs/blue.jpg','image/png',10,10,1,'blue',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	blueID, _ := blue.LastInsertId()
	red, _ := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/red.png','thumbs/red.jpg','image/png',10,10,1,'red',CURRENT_TIMESTAMP)`)
	redID, _ := red.LastInsertId()
	_, _ = store.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES('blue','Blue','general')`)
	_, _ = store.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name='blue'`, blueID)
	session, _ := store.NewSession(ctx, &viewer.ID)

	response := sessionRequest(t, server.Handler(), "GET", "/pools/picker?q=blue", nil, session)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `data-post-id="`+strconv.FormatInt(blueID, 10)+`"`) || strings.Contains(body, `data-post-id="`+strconv.FormatInt(redID, 10)+`"`) {
		t.Fatalf("picker did not filter visual candidates: status=%d body=%s", response.Code, body)
	}
}

func TestPoolPickerPreservesSelectedIDsForSourcePicker(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	viewer, _ := store.Register(ctx, "picker-selected", "picker selected password")
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/selected.png','thumbs/selected.jpg','image/png',10,10,1,'selected',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	selectedID, _ := result.LastInsertId()
	session, _ := store.NewSession(ctx, &viewer.ID)
	response := sessionRequest(t, server.Handler(), "GET", "/pools/picker?source=all&post_ids="+strconv.FormatInt(selectedID, 10), nil, session)
	body := response.Body.String()
	want := `data-post-id="` + strconv.FormatInt(selectedID, 10) + `"`
	if response.Code != http.StatusOK || !strings.Contains(body, want+` aria-label="Add image to pool" aria-pressed="true" disabled`) {
		t.Fatalf("picker did not preserve selected ID: status=%d body=%s", response.Code, body)
	}
}

func TestHomeUsesKuraBanner(t *testing.T) {
	body, err := assets.ReadFile("templates/home.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, `src="/static/images/kura-banner.png"`) {
		t.Error("home template does not reference the Kura banner")
	}
	if strings.Contains(html, "safebooru-reference") {
		t.Error("home template still references the temporary Safebooru banner")
	}
	if _, err := assets.ReadFile("static/images/kura-banner.png"); err != nil {
		t.Errorf("Kura banner asset is missing: %v", err)
	}
}

func TestNavigationMarksOnlyTheCurrentSection(t *testing.T) {
	s, err := New(nil, "")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		template  string
		activeNav string
		current   string
	}{
		{name: "home", template: "home", activeNav: "home", current: `href="/" aria-current="page"`},
		{name: "posts", template: "posts", activeNav: "posts", current: `href="/posts" aria-current="page"`},
		{name: "post detail", template: "post", activeNav: "posts", current: `href="/posts" aria-current="page"`},
		{name: "pools", template: "pools", activeNav: "pools", current: `href="/pools" aria-current="page"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("GET", "/", nil)
			s.render(recorder, request, tt.template, viewData{Title: "Kura", ActiveNav: tt.activeNav})
			html := recorder.Body.String()
			if !strings.Contains(html, tt.current) {
				t.Errorf("current navigation item missing %q", tt.current)
			}
			if got := strings.Count(html, `aria-current="page"`); got != 1 {
				t.Errorf("current navigation item count = %d, want 1", got)
			}
		})
	}
}
