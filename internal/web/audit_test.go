package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestAuditLogIsSuperAdminOnlyAndSupportsFiltersAndPagination(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-web-root", "audit web root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-web-owner", "audit web owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-web-admin", "audit web admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "audit-web-viewer", "audit web viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, owner, archive.NewPost{Status: "published", OriginalPath: "originals/audit-web.png", ThumbnailPath: "thumbs/audit-web.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "audit-web-post", Tags: []string{"web_before"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, "https://web.example", "web_after", "published"); err != nil {
		t.Fatal(err)
	}

	handler := server.Handler()
	adminSession, _ := store.NewSession(ctx, &admin.ID)
	viewerSession, _ := store.NewSession(ctx, &viewer.ID)
	if response := sessionRequest(t, handler, http.MethodGet, "/admin/audit", nil, adminSession); response.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin audit status=%d body=%s", response.Code, response.Body.String())
	}
	if response := sessionRequest(t, handler, http.MethodGet, "/admin/audit", nil, viewerSession); response.Code != http.StatusForbidden {
		t.Fatalf("viewer audit status=%d body=%s", response.Code, response.Body.String())
	}

	rootSession, _ := store.NewSession(ctx, &root.ID)
	path := "/admin/audit?action=metadata_change&post=" + strconv.FormatInt(post.ID, 10)
	response := sessionRequest(t, handler, http.MethodGet, path, nil, rootSession)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Audit log") || !strings.Contains(body, "metadata_change") || !strings.Contains(body, owner.Username) || !strings.Contains(body, "web_before") || !strings.Contains(body, "web_after") {
		t.Fatalf("filtered audit page status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "unfiltered-private-owner") {
		t.Fatalf("audit filter exposed unrelated private event: %s", body)
	}
}

func TestSuperAdminAuditRevertRouteUsesCSRFAndRedirects(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-route-root", "audit route root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-route-owner", "audit route owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-route-admin", "audit route admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, owner, archive.NewPost{Status: "published", OriginalPath: "originals/audit-route.png", ThumbnailPath: "thumbs/audit-route.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "audit-route-post", Source: "https://route-before.example", Tags: []string{"route_before"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, "https://route-after.example", "route_after", "draft"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAuditEvents(ctx, root, archive.AuditFilter{PostID: post.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("route audit page=%+v err=%v", page, err)
	}

	rootSession, _ := store.NewSession(ctx, &root.ID)
	missingCSRF := sessionRequest(t, server.Handler(), http.MethodPost, "/admin/audit/"+strconv.FormatInt(page.Events[0].ID, 10)+"/revert", url.Values{}, archive.Session{Token: rootSession.Token})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}
	request := sessionRequest(t, server.Handler(), http.MethodPost, "/admin/audit/"+strconv.FormatInt(page.Events[0].ID, 10)+"/revert", nil, rootSession)
	if request.Code != http.StatusSeeOther || request.Header().Get("Location") != "/admin/audit" {
		t.Fatalf("revert route status=%d location=%q body=%s", request.Code, request.Header().Get("Location"), request.Body.String())
	}
	restored, err := store.Post(ctx, post.ID)
	if err != nil || restored.Source != "https://route-before.example" || restored.Status != "published" {
		t.Fatalf("route revert did not restore post=%+v err=%v", restored, err)
	}
}
