package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestBulkTagControlsAreEditorOnlyAndBoundToVisibleBrowseResults(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-ui-admin", "bulk ui admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "bulk-ui-viewer", "bulk ui viewer password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, archive.NewPost{
		Status: "published", OriginalPath: "originals/ui.png", ThumbnailPath: "thumbs/ui.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	adminSession, _ := store.NewSession(ctx, &admin.ID)
	adminPage := sessionRequest(t, handler, http.MethodGet, "/posts", nil, adminSession)
	body := adminPage.Body.String()
	if adminPage.Code != http.StatusOK || !strings.Contains(body, "bulk-tag-toolbar") || !strings.Contains(body, `data-bulk-post="`+strconv.FormatInt(post.ID, 10)+`"`) || !strings.Contains(body, "Select this page") || !strings.Contains(body, "24 posts maximum") {
		t.Fatalf("editor browse page lacks bounded bulk controls: status=%d body=%s", adminPage.Code, body)
	}
	viewerSession, _ := store.NewSession(ctx, &viewer.ID)
	viewerPage := sessionRequest(t, handler, http.MethodGet, "/posts", nil, viewerSession)
	if viewerPage.Code != http.StatusOK || strings.Contains(viewerPage.Body.String(), "bulk-tag-toolbar") || strings.Contains(viewerPage.Body.String(), "data-bulk-post") {
		t.Fatalf("viewer browse page exposed bulk controls: status=%d body=%s", viewerPage.Code, viewerPage.Body.String())
	}
}

func TestBulkTagPreviewRendersChangesWithoutMutatingAndRejectsViewers(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-preview-admin", "bulk preview admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "bulk-preview-viewer", "bulk preview viewer password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, archive.NewPost{
		Status: "published", OriginalPath: "originals/preview-ui.png", ThumbnailPath: "thumbs/preview-ui.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-preview-ui",
		Tags: []string{"keep", "remove_me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	adminSession, _ := store.NewSession(ctx, &admin.ID)
	values := url.Values{"post_ids": {strconv.FormatInt(post.ID, 10)}, "add_tags": {"add_me keep"}, "remove_tags": {"remove_me absent"}, "source": {"/posts"}}
	preview := sessionRequest(t, handler, http.MethodPost, "/posts/bulk-tags/preview", values, adminSession)
	body := preview.Body.String()
	if preview.Code != http.StatusOK || !strings.Contains(body, "Preview bulk tag changes") || !strings.Contains(body, "add_me") || !strings.Contains(body, "remove_me") || !strings.Contains(body, "No-op") {
		t.Fatalf("bulk preview response status=%d body=%s", preview.Code, body)
	}
	after, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tags) != 2 {
		t.Fatalf("preview changed tags: %+v", after.Tags)
	}
	viewerSession, _ := store.NewSession(ctx, &viewer.ID)
	denied := sessionRequest(t, handler, http.MethodPost, "/posts/bulk-tags/preview", values, viewerSession)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer bulk preview status=%d, want 403", denied.Code)
	}
}

func TestBulkTagApplySupportsHTMXAndOrdinaryRedirect(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-apply-admin", "bulk apply admin password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, archive.NewPost{
		Status: "published", OriginalPath: "originals/apply-ui.png", ThumbnailPath: "thumbs/apply-ui.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-apply-ui",
		Tags: []string{"remove_me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	session, _ := store.NewSession(ctx, &admin.ID)
	values := url.Values{"post_ids": {strconv.FormatInt(post.ID, 10)}, "add_tags": {"applied"}, "remove_tags": {"remove_me"}, "source": {"/posts?q=remove_me"}}
	request := sessionRequest(t, handler, http.MethodPost, "/posts/bulk-tags/apply", values, session)
	if request.Code != http.StatusSeeOther || request.Header().Get("Location") != "/posts?q=remove_me" {
		t.Fatalf("ordinary bulk apply status=%d location=%q body=%s", request.Code, request.Header().Get("Location"), request.Body.String())
	}
	updated, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Tags) != 1 || updated.Tags[0].Name != "applied" {
		t.Fatalf("ordinary apply did not persist delta: %+v", updated.Tags)
	}

	values = url.Values{"post_ids": {strconv.FormatInt(post.ID, 10)}, "add_tags": {"htmx_applied"}, "remove_tags": {}, "csrf": {session.CSRF}}
	htmxRequest := httptest.NewRequest(http.MethodPost, "/posts/bulk-tags/apply", strings.NewReader(values.Encode()))
	htmxRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	htmxRequest.Header.Set("HX-Request", "true")
	htmxRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, htmxRequest)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "Bulk tags applied") {
		t.Fatalf("HTMX bulk apply status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestBulkSelectionResetsOnHTMXHistoryRestore(t *testing.T) {
	server, _ := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/bulk-selection.js", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "htmx:historyRestore") {
		t.Fatalf("bulk selection script does not reset restored history state: status=%d body=%s", response.Code, response.Body.String())
	}
}
