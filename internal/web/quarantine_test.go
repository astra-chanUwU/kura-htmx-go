package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestOrdinaryAdminDeleteActionQuarantinesAnotherOwnersUpload(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-quarantine-root", "web quarantine root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "web-quarantine-admin", "web quarantine admin password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "web-quarantine-owner", "web quarantine owner password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	postID := insertWebPolicyPost(t, store, owner.ID, "web-quarantine-cross-owner")
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	response := sessionRequest(t, server.Handler(), http.MethodPost, "/posts/"+strconv.FormatInt(postID, 10)+"/delete", url.Values{"csrf": {session.CSRF}}, session)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("cross-owner action status=%d body=%s", response.Code, response.Body.String())
	}
	var deletedAt, quarantinedAt, reason sql.NullString
	if err = store.DB.QueryRow(`SELECT deleted_at,quarantined_at,quarantine_reason FROM posts WHERE id=?`, postID).Scan(&deletedAt, &quarantinedAt, &reason); err != nil {
		t.Fatal(err)
	}
	if deletedAt.Valid || !quarantinedAt.Valid || reason.String == "" {
		t.Fatalf("cross-owner action metadata deleted=%v quarantined=%v reason=%q", deletedAt, quarantinedAt, reason.String)
	}
	if public := mediaRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10), nil, nil); public.Code != http.StatusNotFound {
		t.Fatalf("quarantined direct view status=%d", public.Code)
	}
}

func TestSuperAdminCanReviewRestoreAndPermanentlyDeleteQuarantinedMedia(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-review-root", "web review root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "web-review-owner", "web review owner password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	postID := insertWebPolicyPost(t, store, owner.ID, "web-review-quarantined")
	original := filepath.Join(server.mediaRoot, "originals", "web-review-quarantined.png")
	thumbnail := filepath.Join(server.mediaRoot, "thumbs", "web-review-quarantined.jpg")
	if err = os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(thumbnail), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.QuarantinePost(ctx, root, postID, "review this upload"); err != nil {
		t.Fatal(err)
	}
	rootSession, err := store.NewSession(ctx, &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionRequest(t, server.Handler(), http.MethodGet, "/admin/images?status=quarantined", nil, rootSession)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "review this upload") || !strings.Contains(page.Body.String(), "Quarantined") {
		t.Fatalf("quarantine review page status=%d body=%s", page.Code, page.Body.String())
	}
	if response := mediaRequest(t, server.Handler(), http.MethodGet, "/media/originals/web-review-quarantined.png", nil, &rootSession); response.Code != http.StatusOK {
		t.Fatalf("super-admin quarantined media status=%d body=%s", response.Code, response.Body.String())
	}
	if response := mediaRequest(t, server.Handler(), http.MethodGet, "/media/originals/web-review-quarantined.png", nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("anonymous quarantined media status=%d", response.Code)
	}
	review := sessionRequest(t, server.Handler(), http.MethodGet, "/admin/images/"+strconv.FormatInt(postID, 10)+"/review", nil, rootSession)
	if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), "/media/originals/web-review-quarantined.png") {
		t.Fatalf("super-admin review detail status=%d body=%s", review.Code, review.Body.String())
	}

	restore := sessionRequest(t, server.Handler(), http.MethodPost, "/admin/images/"+strconv.FormatInt(postID, 10)+"/restore", url.Values{"csrf": {rootSession.CSRF}}, rootSession)
	if restore.Code != http.StatusSeeOther {
		t.Fatalf("restore status=%d body=%s", restore.Code, restore.Body.String())
	}
	if _, err = store.Post(ctx, postID); err != nil {
		t.Fatalf("restored post remained unavailable: %v", err)
	}
	if err = store.QuarantinePost(ctx, root, postID, "delete after review"); err != nil {
		t.Fatal(err)
	}
	deleteValues := url.Values{"csrf": {rootSession.CSRF}, "confirmation": {strconv.FormatInt(postID, 10)}}
	deleted := sessionRequest(t, server.Handler(), http.MethodPost, "/admin/images/"+strconv.FormatInt(postID, 10)+"/permanent-delete", deleteValues, rootSession)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("permanent delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err = store.DB.Exec(`SELECT id FROM posts WHERE id=?`, postID); err != nil {
		// The query itself is expected to remain valid after deletion.
		t.Fatal(err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM posts WHERE id=?`, postID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("quarantined post metadata remained: count=%d err=%v", count, err)
	}
	if _, err = os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("original remained after web permanent deletion: %v", err)
	}
	if _, err = os.Stat(thumbnail); !os.IsNotExist(err) {
		t.Fatalf("thumbnail remained after web permanent deletion: %v", err)
	}
}

func TestOwnerPermanentDeleteRequiresExplicitConfirmation(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-owner-delete-root", "web owner delete root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "web-owner-delete-owner", "web owner delete owner password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	postID := insertWebPolicyPost(t, store, owner.ID, "web-owner-delete")
	original := filepath.Join(server.mediaRoot, "originals", "web-owner-delete.png")
	thumbnail := filepath.Join(server.mediaRoot, "thumbs", "web-owner-delete.jpg")
	if err = os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(thumbnail), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	postPage := sessionRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10), nil, session)
	if postPage.Code != http.StatusOK || !strings.Contains(postPage.Body.String(), `action="/posts/`+strconv.FormatInt(postID, 10)+`/delete"`) || !strings.Contains(postPage.Body.String(), "Type DELETE") {
		t.Fatalf("owner permanent-delete action missing: status=%d body=%s", postPage.Code, postPage.Body.String())
	}
	path := "/posts/" + strconv.FormatInt(postID, 10) + "/delete"
	missing := sessionRequest(t, server.Handler(), http.MethodPost, path, url.Values{}, session)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing hard-delete confirmation status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, err = store.Post(ctx, postID); err != nil {
		t.Fatalf("missing confirmation removed post: %v", err)
	}
	confirmed := sessionRequest(t, server.Handler(), http.MethodPost, path, url.Values{"confirmation": {"DELETE"}}, session)
	if confirmed.Code != http.StatusSeeOther {
		t.Fatalf("confirmed hard-delete status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	if _, err = store.Post(ctx, postID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("confirmed hard-delete kept metadata: %v", err)
	}
}

func TestSuperAdminCanPermanentlyDeleteLegacyUnownedUpload(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "web-unowned-delete-root", "web unowned delete root password")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/web-unowned.png','thumbs/web-unowned.jpg','image/png',10,10,8,'web-unowned-delete','2026-09-17T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`UPDATE posts SET deleted_at='2026-09-17T00:00:00Z' WHERE id=?`, postID); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(server.mediaRoot, "originals", "web-unowned.png")
	thumbnail := filepath.Join(server.mediaRoot, "thumbs", "web-unowned.jpg")
	if err = os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(thumbnail), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionRequest(t, server.Handler(), http.MethodGet, "/admin/images?status=deleted", nil, session)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `action="/admin/images/`+strconv.FormatInt(postID, 10)+`/permanent-delete"`) {
		t.Fatalf("deleted-image cleanup action missing: status=%d body=%s", page.Code, page.Body.String())
	}
	path := "/admin/images/" + strconv.FormatInt(postID, 10) + "/permanent-delete"
	response := sessionRequest(t, server.Handler(), http.MethodPost, path, url.Values{"confirmation": {strconv.FormatInt(postID, 10)}}, session)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("unowned permanent delete status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM posts WHERE id=?`, postID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unowned post remained after permanent deletion: count=%d err=%v", count, err)
	}
}

func insertWebPolicyPost(t *testing.T, store *archive.Store, uploaderID int64, hash string) int64 {
	t.Helper()
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id) VALUES('published',?,?,?,?,?,?,?,?,?)`, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", 10, 10, 8, hash, "2026-09-17T00:00:00Z", uploaderID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
