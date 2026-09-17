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
)

func TestOrdinaryAdminImagesDenialUsesStyledForbiddenPage(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "errors-root", "errors root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "ordinary-admin", "ordinary admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	response := sessionRequest(t, server.Handler(), http.MethodGet, "/admin/images", nil, session)
	assertStyledError(t, response, http.StatusForbidden, "Access denied")
	if strings.Contains(response.Body.String(), "super admin access required") {
		t.Fatal("authorization detail leaked into the browser page")
	}
}

func TestInaccessiblePrivatePostUsesStyledNonRevealingNotFound(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	owner, err := store.Register(ctx, "private-owner", "private owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "private-other", "private other password")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id) VALUES('draft','originals/private.png','thumbs/private.jpg','image/png',1,1,1,'private-errors',?)`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &other.ID)
	if err != nil {
		t.Fatal(err)
	}

	response := sessionRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10), nil, session)
	assertStyledError(t, response, http.StatusNotFound, "Not found")
	body := response.Body.String()
	if strings.Contains(body, "private-owner") || strings.Contains(body, "draft") {
		t.Fatalf("private post details leaked into 404 page: %s", body)
	}
	privatePool, err := store.CreatePool(ctx, owner, "Private errors pool", "", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	poolResponse := sessionRequest(t, server.Handler(), http.MethodGet, "/pools/"+privatePool.Slug, nil, session)
	assertStyledError(t, poolResponse, http.StatusNotFound, "Not found")
	if strings.Contains(poolResponse.Body.String(), "Private errors pool") || strings.Contains(poolResponse.Body.String(), "private-owner") {
		t.Fatalf("private pool details leaked into 404 page: %s", poolResponse.Body.String())
	}
}

func TestExpiredSetupLinkUsesStyledGonePage(t *testing.T) {
	server, _ := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setup?token=expired-token", nil))
	assertStyledError(t, response, http.StatusGone, "Link expired")
	if strings.Contains(response.Body.String(), "expired-token") {
		t.Fatal("setup token leaked into expired-link page")
	}
}

func TestLoginRateLimitUsesStyled429Message(t *testing.T) {
	server, store := testServer(t)
	if _, err := store.Register(context.Background(), "limited-user", "limited user password"); err != nil {
		t.Fatal(err)
	}
	server.loginLimiter = newAttemptLimiter(1, time.Minute)
	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/login", nil))
	session, err := store.Session(context.Background(), get.Result().Cookies()[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	_ = sessionRequest(t, server.Handler(), http.MethodPost, "/login", mapValues("username", "limited-user", "password", "wrong password"), session)
	blocked := sessionRequest(t, server.Handler(), http.MethodPost, "/login", mapValues("username", "limited-user", "password", "wrong password"), session)
	if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Header().Get("Content-Type"), "text/html") || !strings.Contains(blocked.Body.String(), `class="alert"`) || !strings.Contains(blocked.Body.String(), "Too many attempts") {
		t.Fatalf("rate limit was not a styled 429 form response: status=%d type=%q body=%s", blocked.Code, blocked.Header().Get("Content-Type"), blocked.Body.String())
	}
}

func TestRecoveryRateLimitUsesStyled429Message(t *testing.T) {
	server, store := testServer(t)
	server.recoveryLimiter = newAttemptLimiter(1, time.Minute)
	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/recover", nil))
	session, err := store.Session(context.Background(), get.Result().Cookies()[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	values := mapValues("username", "missing-recovery-user", "recovery_code", "invalid", "password", "replacement password long")
	_ = sessionRequest(t, server.Handler(), http.MethodPost, "/recover", values, session)
	blocked := sessionRequest(t, server.Handler(), http.MethodPost, "/recover", values, session)
	if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Header().Get("Content-Type"), "text/html") || !strings.Contains(blocked.Body.String(), `class="alert"`) || !strings.Contains(blocked.Body.String(), "Too many attempts") {
		t.Fatalf("recovery rate limit was not a styled 429 form response: status=%d type=%q body=%s", blocked.Code, blocked.Header().Get("Content-Type"), blocked.Body.String())
	}
}

func TestClosedStoreBrowserFailureUsesGenericStyled500(t *testing.T) {
	server, store := testServer(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/posts", nil))
	assertStyledError(t, response, http.StatusInternalServerError, "Something went wrong")
	if strings.Contains(response.Body.String(), "database is closed") || strings.Contains(response.Body.String(), "session unavailable") {
		t.Fatal("internal browser failure leaked implementation details")
	}
}

func TestHTMXFailureUsesAccessibleFragment(t *testing.T) {
	server, store := testServer(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/posts/grid", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("HTMX failure status/type=%d/%q", response.Code, response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, `class="alert"`) || !strings.Contains(body, `role="alert"`) || strings.Contains(body, "<!doctype html>") {
		t.Fatalf("HTMX failure was not a small accessible fragment: %s", body)
	}
}

func TestPasskeyFailureUsesJSONSafeMessage(t *testing.T) {
	server, store := testServer(t)
	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/login", nil))
	cookie := get.Result().Cookies()[0]
	session, err := store.Session(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/passkeys/login/finish", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.CSRF)
	request.Header.Set("X-Kura-Challenge", "not-a-real-challenge")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("passkey failure status/type=%d/%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] == "" || strings.Contains(payload["error"], "challenge") || strings.Contains(response.Body.String(), "<!doctype html>") {
		t.Fatalf("passkey failure message is not safe: %s", response.Body.String())
	}
}

func TestProtectedMediaAndDownloadErrorsStayPlainAndNonRevealing(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	owner, err := store.Register(ctx, "media-private-owner", "media private owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "media-private-other", "media private other password")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id) VALUES('draft','originals/protected.png','thumbs/protected.jpg','image/png',1,1,1,'protected-errors',?)`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &other.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/media/originals/protected.png",
		"/posts/" + strconv.FormatInt(postID, 10) + "/download",
		"/posts/export?post_ids=" + strconv.FormatInt(postID, 10),
	} {
		response := mediaRequest(t, server.Handler(), http.MethodGet, path, nil, &session)
		if response.Code != http.StatusNotFound || strings.Contains(response.Header().Get("Content-Type"), "text/html") || strings.Contains(response.Body.String(), "media-private-owner") {
			t.Fatalf("protected response leaked or became HTML for %s: status=%d type=%q body=%s", path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
}

func assertStyledError(t *testing.T, response *httptest.ResponseRecorder, status int, heading string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("error status/type=%d/%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `class="error-page`) || !strings.Contains(body, heading) || !strings.Contains(body, `id="main"`) {
		t.Fatalf("response is not a styled Kura error page: %s", body)
	}
}

func mapValues(values ...string) url.Values {
	result := url.Values{}
	for i := 0; i+1 < len(values); i += 2 {
		result.Set(values[i], values[i+1])
	}
	return result
}
