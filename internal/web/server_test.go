package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

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
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Add to pool") || !strings.Contains(page.Body.String(), pool.Name) {
		t.Fatalf("post page lacks the visual pool action: status=%d body=%s", page.Code, page.Body.String())
	}
	added := sessionRequest(t, handler, "POST", "/posts/"+strconv.FormatInt(postID, 10)+"/pools", url.Values{"pool": {pool.Slug}}, session)
	if added.Code != http.StatusSeeOther {
		t.Fatalf("add-to-pool status=%d body=%s", added.Code, added.Body.String())
	}
	updated, err := store.Pool(ctx, pool.Slug, viewer.ID)
	if err != nil || len(updated.Posts) != 1 || updated.Posts[0].ID != postID {
		t.Fatalf("post was not added to pool: %+v err=%v", updated, err)
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
