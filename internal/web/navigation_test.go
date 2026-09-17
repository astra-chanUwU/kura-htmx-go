package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

func insertWebNavigationPost(t *testing.T, serverStore interface {
	Exec(string, ...any) (sql.Result, error)
}, hash, status, publishedAt string, uploaderID any, tags ...string) int64 {
	t.Helper()
	result, err := serverStore.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, status, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", 10, 10, 100, hash, publishedAt, uploaderID)
	if err != nil {
		t.Fatal(err)
	}
	lastID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if _, err = serverStore.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, tag, tag, "general"); err != nil {
			t.Fatal(err)
		}
		if _, err = serverStore.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, lastID, tag); err != nil {
			t.Fatal(err)
		}
	}
	return lastID
}

func TestPostNavigationPropagatesBrowsePoolUploadsAndAdminContexts(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "navigation-web-root", "navigation web root password")
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := store.Register(ctx, "navigation-web-moderator", "navigation web moderator password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	publicID := insertWebNavigationPost(t, store.DB, "navigation-web-public", "published", "2026-09-17T03:00:00Z", nil, "needle")
	draftID := insertWebNavigationPost(t, store.DB, "navigation-web-draft", "draft", "2026-09-17T02:00:00Z", moderator.ID)
	pool, err := store.CreatePool(ctx, moderator, "Web navigation pool", "", "published", fmt.Sprint(publicID))
	if err != nil {
		t.Fatal(err)
	}

	handler := server.Handler()
	browse := sessionRequest(t, handler, http.MethodGet, "/posts?q=needle&page=2", nil, archive.Session{})
	if browse.Code != http.StatusOK || !strings.Contains(browse.Body.String(), `context=browse`) || !strings.Contains(browse.Body.String(), `/posts/`+strconv.FormatInt(publicID, 10)) {
		t.Fatalf("browse links did not carry context: status=%d body=%s", browse.Code, browse.Body.String())
	}

	uploadsSession, err := store.NewSession(ctx, &moderator.ID)
	if err != nil {
		t.Fatal(err)
	}
	uploads := sessionRequest(t, handler, http.MethodGet, "/uploads?status=draft", nil, uploadsSession)
	if uploads.Code != http.StatusOK || !strings.Contains(uploads.Body.String(), `context=uploads`) || !strings.Contains(uploads.Body.String(), `status=draft`) {
		t.Fatalf("uploads links did not carry active filter context: status=%d body=%s", uploads.Code, uploads.Body.String())
	}

	poolPage := sessionRequest(t, handler, http.MethodGet, "/pools/"+pool.Slug, nil, uploadsSession)
	if poolPage.Code != http.StatusOK || !strings.Contains(poolPage.Body.String(), `context=pool`) || !strings.Contains(poolPage.Body.String(), `pool=`+pool.Slug) {
		t.Fatalf("pool links did not carry context: status=%d body=%s", poolPage.Code, poolPage.Body.String())
	}

	adminSession, err := store.NewSession(ctx, &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin := sessionRequest(t, handler, http.MethodGet, "/admin/images?status=draft&uploader="+strconv.FormatInt(moderator.ID, 10)+"&page=2", nil, adminSession)
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), `context=admin`) || !strings.Contains(admin.Body.String(), `status=draft`) || !strings.Contains(admin.Body.String(), `uploader=`+strconv.FormatInt(moderator.ID, 10)) {
		t.Fatalf("admin links did not carry active filter context: status=%d body=%s", admin.Code, admin.Body.String())
	}
	if draftID == 0 {
		t.Fatal("draft post was not inserted")
	}
}

func TestPostDetailRendersValidatedNeighborsBackAndBoundaries(t *testing.T) {
	server, store := testServer(t)
	first := insertWebNavigationPost(t, store.DB, "navigation-detail-first", "published", "2026-09-17T02:00:00Z", nil, "needle")
	current := insertWebNavigationPost(t, store.DB, "navigation-detail-current", "published", "2026-09-17T01:00:00Z", nil, "needle")
	handler := server.Handler()

	detail := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d?context=browse&page=4&q=needle", current), nil, archive.Session{})
	body := detail.Body.String()
	if detail.Code != http.StatusOK || !strings.Contains(body, `>Previous</a>`) || !strings.Contains(body, `>Next`) || !strings.Contains(body, `href="/posts?page=4&amp;q=needle"`) || !strings.Contains(body, `href="/posts/`+strconv.FormatInt(first, 10)+`?context=browse`) {
		t.Fatalf("detail navigation was wrong: status=%d body=%s", detail.Code, body)
	}

	firstDetail := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d?context=browse", first), nil, archive.Session{})
	firstBody := firstDetail.Body.String()
	if firstDetail.Code != http.StatusOK || !strings.Contains(firstBody, `aria-disabled="true">Previous</span>`) || !strings.Contains(firstBody, `>Next</a>`) {
		t.Fatalf("first boundary navigation was wrong: status=%d body=%s", firstDetail.Code, firstBody)
	}
	lastDetail := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d?context=browse", current), nil, archive.Session{})
	if !strings.Contains(lastDetail.Body.String(), `aria-disabled="true">Next</span>`) {
		t.Fatalf("last boundary did not disable Next: %s", lastDetail.Body.String())
	}
}

func TestPostDetailInvalidOrUnauthorizedContextFallsBackToBrowse(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "navigation-fallback-root", "navigation fallback root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "navigation-fallback-owner", "navigation fallback owner password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	draft := insertWebNavigationPost(t, store.DB, "navigation-fallback-draft", "draft", "2026-09-17T01:00:00Z", owner.ID)
	insertWebNavigationPost(t, store.DB, "navigation-fallback-other", "draft", "2026-09-17T02:00:00Z", owner.ID)
	session, err := store.NewSession(ctx, &owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	invalid := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d?context=not-real&return=https://evil.example", draft), nil, session)
	if invalid.Code != http.StatusOK || !strings.Contains(invalid.Body.String(), `href="/posts"`) || strings.Contains(invalid.Body.String(), "evil.example") {
		t.Fatalf("invalid context did not fall back safely: status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	if err = store.SetUserRole(ctx, root, owner.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	changed := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d?context=uploads&status=draft", draft), nil, session)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `href="/posts"`) || strings.Contains(changed.Body.String(), `context=uploads`) {
		t.Fatalf("authorization change did not invalidate uploads context: status=%d body=%s", changed.Code, changed.Body.String())
	}
}
