package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestContentCommandsEnforceCurrentActorAndOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "command-root", "command root password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, _ := s.Register(ctx, "command-viewer", "command viewer password")
	moderator, _ := s.Register(ctx, "command-moderator", "command moderator password")
	otherModerator, _ := s.Register(ctx, "command-other-mod", "command other moderator password")
	if err = s.SetUserRole(ctx, root, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, otherModerator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}

	input := NewPost{UploaderID: viewer.ID, Status: "published", OriginalPath: "originals/command.png", ThumbnailPath: "thumbs/command.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "command-post"}
	if _, err = s.CreatePost(ctx, viewer, input); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer created a post: %v", err)
	}
	post, err := s.CreatePost(ctx, moderator, input)
	if err != nil {
		t.Fatal(err)
	}
	var uploaderID int64
	if err = s.DB.QueryRow(`SELECT uploader_id FROM posts WHERE id=?`, post.ID).Scan(&uploaderID); err != nil || uploaderID != moderator.ID {
		t.Fatalf("post ownership trusted input instead of actor: owner=%d err=%v", uploaderID, err)
	}

	before, _ := s.Post(ctx, post.ID)
	if err = s.UpdatePost(ctx, viewer, post.ID, "forbidden", "viewer", "published"); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer edited a post: %v", err)
	}
	after, _ := s.Post(ctx, post.ID)
	if after.Source != before.Source || len(after.Tags) != len(before.Tags) {
		t.Fatalf("rejected edit changed post: before=%+v after=%+v", before, after)
	}
	if err = s.UpdatePost(ctx, otherModerator, post.ID, "moderated", "blue", "published"); err != nil {
		t.Fatalf("moderator could not moderate another upload: %v", err)
	}
	if err = s.SoftDeletePost(ctx, otherModerator, post.ID); !errors.Is(err, ErrPermission) {
		t.Fatalf("moderator deleted another user's upload: %v", err)
	}
	var deleted sql.NullString
	if err = s.DB.QueryRow(`SELECT deleted_at FROM posts WHERE id=?`, post.ID).Scan(&deleted); err != nil || deleted.Valid {
		t.Fatalf("rejected deletion changed post: deleted=%v err=%v", deleted, err)
	}
	if err = s.SoftDeletePost(ctx, moderator, post.ID); err != nil {
		t.Fatalf("owner moderator could not delete upload: %v", err)
	}
	adminPost, err := s.CreatePost(ctx, moderator, NewPost{Status: "published", OriginalPath: "originals/admin.png", ThumbnailPath: "thumbs/admin.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "admin-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SoftDeletePost(ctx, root, adminPost.ID); err != nil {
		t.Fatalf("admin could not delete another upload: %v", err)
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256) VALUES('published','originals/legacy.png','thumbs/legacy.jpg','image/png',1,1,1,'legacy-unowned')`)
	if err != nil {
		t.Fatal(err)
	}
	unownedID, _ := result.LastInsertId()
	if err = s.SoftDeletePost(ctx, root, unownedID); err != nil {
		t.Fatalf("admin could not delete unowned legacy post: %v", err)
	}

	stale := moderator
	if err = s.SetUserRole(ctx, root, moderator.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePost(ctx, stale, NewPost{Status: "draft", OriginalPath: "originals/stale.png", ThumbnailPath: "thumbs/stale.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "stale-post"}); !errors.Is(err, ErrPermission) {
		t.Fatalf("stale moderator snapshot bypassed role change: %v", err)
	}
}

func TestFavoriteAndPoolCommandsBindToActiveActorAndOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, _ := s.Register(ctx, "command-alice", "command alice password")
	b, _ := s.Register(ctx, "command-bobby", "command bobby password")
	addPost(t, s, "command-public", "published")
	var postID int64
	if err := s.DB.QueryRow(`SELECT id FROM posts WHERE sha256=?`, "command-public").Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, a, postID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, b, postID, false); err != nil {
		t.Fatal(err)
	}
	var favoriteCount int
	if err := s.DB.QueryRow(`SELECT count(*) FROM favorites WHERE user_id=? AND post_id=?`, a.ID, postID).Scan(&favoriteCount); err != nil || favoriteCount != 1 {
		t.Fatalf("another actor changed the owner's favorite: count=%d err=%v", favoriteCount, err)
	}

	pool, err := s.CreatePool(ctx, a, "Command Pool", "", "draft", fmt.Sprint(postID))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddPostToPool(ctx, b, pool.Slug, postID); !errors.Is(err, ErrPermission) {
		t.Fatalf("another viewer changed pool membership: %v", err)
	}
	if err = s.UpdatePool(ctx, b, pool.Slug, "Hijacked", "", "published", ""); !errors.Is(err, ErrPermission) {
		t.Fatalf("another viewer edited pool: %v", err)
	}
	unchanged, err := s.Pool(ctx, pool.Slug, a.ID)
	if err != nil || unchanged.Name != "Command Pool" || unchanged.Status != "draft" || len(unchanged.Posts) != 1 {
		t.Fatalf("rejected pool mutation changed data: pool=%+v err=%v", unchanged, err)
	}

	stale := a
	if _, err = s.DB.Exec(`UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetFavorite(ctx, stale, postID, false); !errors.Is(err, ErrPermission) {
		t.Fatalf("suspended actor changed favorite: %v", err)
	}
	if err = s.UpdatePool(ctx, stale, pool.Slug, "Changed", "", "draft", fmt.Sprint(postID)); !errors.Is(err, ErrPermission) {
		t.Fatalf("suspended actor edited pool: %v", err)
	}
	if err = s.AddPostToPool(ctx, stale, pool.Slug, postID); !errors.Is(err, ErrPermission) {
		t.Fatalf("suspended actor changed pool membership: %v", err)
	}
	if _, err = s.Pool(ctx, pool.Slug, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("suspended actor retained private pool visibility: %v", err)
	}
	if _, err = s.PoolCandidates(ctx, PoolCandidateFilter{Source: "pool", PoolSlug: pool.Slug, ViewerID: a.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("suspended actor retained private pool candidates: %v", err)
	}
}

func TestRoleCommandsRejectStaleAndSuspendedAuthority(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := s.BootstrapSuperAdmin(ctx, "command-authority", "command authority password")
	admin, _ := s.Register(ctx, "command-admin", "command admin password")
	target, _ := s.Register(ctx, "command-target", "command target password")
	if err := s.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	staleRoot := root
	if err := s.TransferSuperAdmin(ctx, root, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, staleRoot, target.ID, "admin"); !errors.Is(err, ErrPermission) {
		t.Fatalf("former super admin retained authority: %v", err)
	}
	currentTarget, _ := s.User(ctx, target.ID)
	if currentTarget.Role != "viewer" {
		t.Fatalf("rejected role change modified target: %+v", currentTarget)
	}
	if err := s.SetUserSuspended(ctx, admin, target.ID, true); err != nil {
		t.Fatalf("new super admin could not suspend target: %v", err)
	}
	currentTarget, _ = s.User(ctx, target.ID)
	if currentTarget.SuspendedAt == "" {
		t.Fatal("target was not suspended by current super admin")
	}
}

func TestVisibilityQueriesDoNotTrustCallerRoleFlags(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner, _ := s.Register(ctx, "visibility-owner", "visibility owner password")
	other, _ := s.Register(ctx, "visibility-other", "visibility other password")
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id) VALUES('draft','originals/private.png','thumbs/private.jpg','image/png',1,1,1,'private-visibility',?)`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := result.LastInsertId()
	if _, err = s.PostForUser(ctx, postID, other.ID, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("forged moderator flag exposed draft: %v", err)
	}
	visible, err := s.MediaVisibleToUser(ctx, "originals", "originals/private.png", other.ID, true)
	if err != nil || visible {
		t.Fatalf("forged moderator flag exposed media: visible=%v err=%v", visible, err)
	}
}
