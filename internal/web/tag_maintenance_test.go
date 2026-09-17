package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestTagMaintenanceIsSuperAdminOnlyAndSupportsPreviewThenApply(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "tag-web-root", "tag web root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "tag-web-admin", "tag web admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, root, archive.NewPost{Status: "published", OriginalPath: "originals/tag-web.png", ThumbnailPath: "thumbs/tag-web.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tag-web", Tags: []string{"artist:web_old"}})
	if err != nil {
		t.Fatal(err)
	}
	rootSession, _ := store.NewSession(ctx, &root.ID)
	adminSession, _ := store.NewSession(ctx, &admin.ID)
	handler := server.Handler()
	if response := sessionRequest(t, handler, http.MethodGet, "/admin/tags", nil, adminSession); response.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin tag inventory status=%d body=%s", response.Code, response.Body.String())
	}
	page := sessionRequest(t, handler, http.MethodGet, "/admin/tags?q=web&category=artist", nil, rootSession)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Tag maintenance") || !strings.Contains(page.Body.String(), "web_old") || !strings.Contains(page.Body.String(), "Post usage") || !strings.Contains(page.Body.String(), ">1</td>") {
		t.Fatalf("tag inventory status=%d body=%s", page.Code, page.Body.String())
	}
	previewValues := url.Values{"operation": {"rename"}, "source": {"web_old"}, "target": {"web_new"}, "reason": {"web cleanup"}}
	preview := sessionRequest(t, handler, http.MethodPost, "/admin/tags/preview", previewValues, rootSession)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "Preview") || !strings.Contains(preview.Body.String(), "affected post") {
		t.Fatalf("tag preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	missingCSRF := sessionRequest(t, handler, http.MethodPost, "/admin/tags/apply", url.Values{"operation": {"rename"}, "source": {"web_old"}, "target": {"web_new"}}, archive.Session{Token: rootSession.Token})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", missingCSRF.Code)
	}
	archivePreview, err := store.PreviewTagMaintenance(ctx, root, archive.TagMaintenanceRequest{Operation: "rename", Source: "web_old", Target: "web_new", Reason: "web cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	applyValues := url.Values{"operation": {"rename"}, "source": {"web_old"}, "target": {"web_new"}, "reason": {"web cleanup"}, "expected_fingerprint": {archivePreview.Fingerprint}}
	apply := sessionRequest(t, handler, http.MethodPost, "/admin/tags/apply", applyValues, rootSession)
	if apply.Code != http.StatusSeeOther || apply.Header().Get("Location") != "/admin/tags" {
		t.Fatalf("ordinary apply status=%d location=%q body=%s", apply.Code, apply.Header().Get("Location"), apply.Body.String())
	}
	updated, err := store.Post(ctx, post.ID)
	if err != nil || len(updated.Tags) != 1 || updated.Tags[0].Name != "web_new" {
		t.Fatalf("web apply did not rename post=%+v err=%v", updated, err)
	}
}

func TestTagMaintenanceRejectsStaleRoleBeforePreview(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "tag-role-root", "tag role root password")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := store.Register(ctx, "tag-role-recipient", "tag role recipient password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, recipient.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	rootSession, _ := store.NewSession(ctx, &root.ID)
	if err = store.TransferSuperAdmin(ctx, root, recipient.ID); err != nil {
		t.Fatal(err)
	}
	response := sessionRequest(t, server.Handler(), http.MethodGet, "/admin/tags", nil, rootSession)
	if response.Code != http.StatusForbidden {
		t.Fatalf("stale super-admin session status=%d body=%s", response.Code, response.Body.String())
	}
}
