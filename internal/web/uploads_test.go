package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestUploadsPageIsOwnerScopedWithStatusFiltersAndPagination(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "uploads-web-root", "uploads web root password")
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := store.Register(ctx, "uploads-web-moderator", "uploads web moderator password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "uploads-web-other", "uploads web other password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}

	insert := func(hash, status string, uploaderID int64) int64 {
		t.Helper()
		result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,original_filename,width,height,byte_size,sha256,published_at,uploader_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, status, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", hash+".png", 10, 20, 100, hash, "2026-09-17T00:00:00Z", uploaderID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	for i := 0; i < 25; i++ {
		status := "published"
		if i%2 == 0 {
			status = "draft"
		}
		insert("uploads-web-owned-"+strconv.Itoa(i), status, moderator.ID)
	}
	insert("uploads-web-other", "published", other.ID)
	insert("uploads-web-deleted", "published", moderator.ID)
	if _, err = store.DB.Exec(`UPDATE posts SET deleted_at=CURRENT_TIMESTAMP WHERE sha256='uploads-web-deleted'`); err != nil {
		t.Fatal(err)
	}

	session, err := store.NewSession(ctx, &moderator.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	all := sessionRequest(t, handler, http.MethodGet, "/uploads", nil, session)
	body := all.Body.String()
	if all.Code != http.StatusOK || !strings.Contains(body, "My uploads") || !strings.Contains(body, "uploads-web-owned-24") || strings.Contains(body, "uploads-web-other") || strings.Contains(body, "uploads-web-deleted") || !strings.Contains(body, `aria-label="Image pages"`) {
		t.Fatalf("owner listing was wrong: status=%d body=%s", all.Code, body)
	}
	if strings.Count(body, `class="upload-card"`) != 24 {
		t.Fatalf("all filter did not paginate at 24: cards=%d", strings.Count(body, `class="upload-card"`))
	}

	drafts := sessionRequest(t, handler, http.MethodGet, "/uploads?status=draft", nil, session)
	body = drafts.Body.String()
	if drafts.Code != http.StatusOK || !strings.Contains(body, `option value="draft" selected`) || strings.Contains(body, "uploads-web-owned-1.png") || strings.Contains(body, "uploads-web-other") {
		t.Fatalf("draft filter was wrong: status=%d body=%s", drafts.Code, body)
	}
	pageTwo := sessionRequest(t, handler, http.MethodGet, "/uploads?page=2", nil, session)
	if pageTwo.Code != http.StatusOK || !strings.Contains(pageTwo.Body.String(), "uploads-web-owned-0") || strings.Contains(pageTwo.Body.String(), "uploads-web-other") {
		t.Fatalf("second page was not owner-scoped: status=%d body=%s", pageTwo.Code, pageTwo.Body.String())
	}
}

func TestUploadsStatusAndDeleteSupportHTMXAndRedirectFallback(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "uploads-actions-root", "uploads actions root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "uploads-actions-owner", "uploads actions owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "uploads-actions-other", "uploads actions other password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	insert := func(hash string, uploaderID int64) int64 {
		t.Helper()
		result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,original_filename,width,height,byte_size,sha256,uploader_id) VALUES('draft',?,?,?,?,?,?,?,?,?)`, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", hash+".png", 10, 20, 100, hash, uploaderID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	ownedID := insert("uploads-actions-owned", owner.ID)
	otherID := insert("uploads-actions-other", other.ID)
	session, _ := store.NewSession(ctx, &owner.ID)
	handler := server.Handler()
	page := sessionRequest(t, handler, http.MethodGet, "/uploads?status=all&page=1", nil, session)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `/uploads/`+strconv.FormatInt(ownedID, 10)+`/status`) || !strings.Contains(page.Body.String(), `hx-target="#uploads-list"`) || strings.Contains(page.Body.String(), "hx-push-url") {
		t.Fatalf("uploads actions did not render history-neutral controls: status=%d body=%s", page.Code, page.Body.String())
	}

	statusPath := "/uploads/" + strconv.FormatInt(ownedID, 10) + "/status?status=all&page=1"
	statusValues := url.Values{"status": {"published"}, "csrf": {session.CSRF}}
	statusRequest := httptest.NewRequest(http.MethodPost, statusPath, strings.NewReader(statusValues.Encode()))
	statusRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	statusRequest.Header.Set("HX-Request", "true")
	statusRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || statusResponse.Header().Get("Location") != "" || !strings.Contains(statusResponse.Body.String(), "published") {
		t.Fatalf("HTMX publish failed: status=%d location=%q body=%s", statusResponse.Code, statusResponse.Header().Get("Location"), statusResponse.Body.String())
	}
	var status string
	if err = store.DB.QueryRow(`SELECT status FROM posts WHERE id=?`, ownedID).Scan(&status); err != nil || status != "published" {
		t.Fatalf("HTMX publish did not persist: status=%q err=%v", status, err)
	}

	normalStatusValues := url.Values{"status": {"draft"}, "csrf": {session.CSRF}}
	normalStatusRequest := httptest.NewRequest(http.MethodPost, statusPath, strings.NewReader(normalStatusValues.Encode()))
	normalStatusRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	normalStatusRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	normalStatusResponse := httptest.NewRecorder()
	handler.ServeHTTP(normalStatusResponse, normalStatusRequest)
	if normalStatusResponse.Code != http.StatusSeeOther || normalStatusResponse.Header().Get("Location") != "/uploads?status=all&page=1" {
		t.Fatalf("ordinary status change did not redirect to uploads: status=%d location=%q body=%s", normalStatusResponse.Code, normalStatusResponse.Header().Get("Location"), normalStatusResponse.Body.String())
	}
	if err = store.DB.QueryRow(`SELECT status FROM posts WHERE id=?`, ownedID).Scan(&status); err != nil || status != "draft" {
		t.Fatalf("ordinary status change did not persist: status=%q err=%v", status, err)
	}

	if strings.Contains(page.Body.String(), `/uploads/`+strconv.FormatInt(ownedID, 10)+`/delete?`) || !strings.Contains(page.Body.String(), `/uploads/`+strconv.FormatInt(ownedID, 10)+`/permanent-delete`) {
		t.Fatalf("uploads page exposed legacy soft deletion or omitted permanent deletion: body=%s", page.Body.String())
	}

	otherDelete := sessionRequest(t, handler, http.MethodPost, "/uploads/"+strconv.FormatInt(otherID, 10)+"/permanent-delete", url.Values{"confirmation": {"DELETE"}}, session)
	if otherDelete.Code != http.StatusForbidden {
		t.Fatalf("moderator deleted another uploader through uploads: status=%d body=%s", otherDelete.Code, otherDelete.Body.String())
	}
}

func TestAdminCanUseOwnUploadsButSuspendedSessionCannot(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "uploads-admin-root", "uploads admin root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "uploads-admin", "uploads admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id) VALUES('published','originals/admin.png','thumbs/admin.jpg','image/png',10,20,100,'uploads-admin-post',?)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	session, _ := store.NewSession(ctx, &admin.ID)
	page := sessionRequest(t, server.Handler(), http.MethodGet, "/uploads", nil, session)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/media/thumbs/admin.jpg") {
		t.Fatalf("admin could not use own uploads: status=%d body=%s", page.Code, page.Body.String())
	}
	if err = store.SetUserSuspended(ctx, root, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	suspended := sessionRequest(t, server.Handler(), http.MethodGet, "/uploads", nil, session)
	if suspended.Code != http.StatusSeeOther || !strings.HasPrefix(suspended.Header().Get("Location"), "/login") {
		t.Fatalf("suspended session retained uploads access: status=%d location=%q body=%s", suspended.Code, suspended.Header().Get("Location"), suspended.Body.String())
	}
}
