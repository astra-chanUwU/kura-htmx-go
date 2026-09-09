package archive

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestAccountRoleAndSuperAdminBoundaries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "root", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := s.Register(ctx, "manager", "manager password long")
	viewer, _ := s.Register(ctx, "viewer", "viewer password long")
	if err = s.SetUserRole(ctx, root, manager.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	manager, _ = s.User(ctx, manager.ID)
	if err = s.SetUserRole(ctx, manager, viewer.ID, "admin"); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin promoted an admin: %v", err)
	}
	if err = s.SetUserRole(ctx, root, root.ID, "viewer"); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("super admin demoted itself without transfer: %v", err)
	}
	if err = s.TransferSuperAdmin(ctx, root, viewer.ID); err != nil {
		t.Fatal(err)
	}
	oldRoot, _ := s.User(ctx, root.ID)
	newRoot, _ := s.User(ctx, viewer.ID)
	if oldRoot.IsSuperAdmin || !newRoot.IsSuperAdmin || newRoot.Role != "admin" {
		t.Fatalf("unexpected transfer result: old=%+v new=%+v", oldRoot, newRoot)
	}
}

func TestPasswordsSessionsAndSuspensionPreserveUploadOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := s.BootstrapSuperAdmin(ctx, "root", "correct horse battery staple")
	uploader, err := s.Register(ctx, "uploader", "uploader password long")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.DB.QueryRow(`SELECT password_hash FROM users WHERE id=?`, uploader.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "uploader password long" || !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("password was not stored as Argon2id: %q", stored)
	}
	if _, err = s.Authenticate(ctx, "UPLOADER", "uploader password long"); err != nil {
		t.Fatal(err)
	}
	session, err := s.NewSession(ctx, &uploader.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id,published_at) VALUES('published','originals/a.png','thumbs/a.jpg','image/png',1,1,1,'owned',?,CURRENT_TIMESTAMP)`, uploader.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := result.LastInsertId()
	if err = s.SetUserSuspended(ctx, root, uploader.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, session.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("suspended account session remained usable: %v", err)
	}
	if _, err = s.DB.Exec(`DELETE FROM users WHERE id=?`, uploader.ID); err == nil {
		t.Fatal("database allowed an uploader account to be deleted and orphan its attribution")
	}
	var owner int64
	if err = s.DB.QueryRow(`SELECT uploader_id FROM posts WHERE id=?`, postID).Scan(&owner); err != nil || owner != uploader.ID {
		t.Fatalf("upload attribution changed after suspension: owner=%d err=%v", owner, err)
	}
}

func TestFavoritesAndDraftPoolsArePrivate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, _ := s.Register(ctx, "alice", "alice password long")
	b, _ := s.Register(ctx, "bobby", "bobby password long")
	addPost(t, s, "public", "published")
	var postID int64
	_ = s.DB.QueryRow(`SELECT id FROM posts WHERE sha256='public'`).Scan(&postID)
	if err := s.SetFavorite(ctx, a.ID, postID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, b.ID, postID, true); err != nil {
		t.Fatalf("second viewer could not favorite the same post: %v", err)
	}
	aFavorites, _ := s.Favorites(ctx, a.ID)
	bFavorites, _ := s.Favorites(ctx, b.ID)
	if len(aFavorites) != 1 || len(bFavorites) != 1 {
		t.Fatalf("favorites leaked: alice=%d bobby=%d", len(aFavorites), len(bFavorites))
	}
	pool, err := s.CreatePool(ctx, a.ID, "Secret Set", "draft", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool(ctx, pool.Slug, b.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("draft pool visible to another viewer: %v", err)
	}
	if _, err = s.Pool(ctx, pool.Slug, a.ID); err != nil {
		t.Fatalf("owner could not view draft pool: %v", err)
	}
}
