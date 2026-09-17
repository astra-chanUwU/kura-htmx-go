package archive

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestDemotingUploaderQuarantinesDraftsRevokesSessionsAndKeepsPublishedPosts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "quarantine-root", "quarantine root password")
	if err != nil {
		t.Fatal(err)
	}
	uploader, err := s.Register(ctx, "quarantine-uploader", "quarantine uploader password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, uploader.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	draft := insertPolicyPost(t, s, uploader.ID, "draft", "demotion-draft")
	published := insertPolicyPost(t, s, uploader.ID, "published", "demotion-published")
	session, err := s.NewSession(ctx, &uploader.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SetUserRole(ctx, root, uploader.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	current, err := s.User(ctx, uploader.ID)
	if err != nil || current.Role != "viewer" {
		t.Fatalf("role=%q err=%v", current.Role, err)
	}
	if _, err = s.Session(ctx, session.Token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("demotion did not revoke session: %v", err)
	}

	var draftQuarantine, draftPrevious, publishedQuarantine sql.NullString
	if err = s.DB.QueryRow(`SELECT quarantined_at,quarantine_previous_status FROM posts WHERE id=?`, draft).Scan(&draftQuarantine, &draftPrevious); err != nil {
		t.Fatal(err)
	}
	if !draftQuarantine.Valid || draftPrevious.String != "draft" {
		t.Fatalf("draft quarantine metadata=%+v previous=%q", draftQuarantine, draftPrevious.String)
	}
	if err = s.DB.QueryRow(`SELECT quarantined_at FROM posts WHERE id=?`, published).Scan(&publishedQuarantine); err != nil {
		t.Fatal(err)
	}
	if publishedQuarantine.Valid {
		t.Fatal("published upload was quarantined during demotion")
	}
	var eventCount int
	if err = s.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE event_type='role_change' AND target_user_id=?`, uploader.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("role-change audit count=%d", eventCount)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE event_type='role_change_quarantine' AND post_id=?`, draft).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("draft quarantine audit count=%d", eventCount)
	}

	if err = s.SetUserRole(ctx, root, uploader.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	var stillQuarantined sql.NullString
	if err = s.DB.QueryRow(`SELECT quarantined_at FROM posts WHERE id=?`, draft).Scan(&stillQuarantined); err != nil {
		t.Fatal(err)
	}
	if !stillQuarantined.Valid {
		t.Fatal("re-promotion unexpectedly restored quarantined draft")
	}
}

func insertPolicyPost(t *testing.T, s *Store, uploaderID int64, status, hash string) int64 {
	t.Helper()
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, status, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", 10, 10, 100, hash, "2026-09-17T00:00:00Z", uploaderID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestQuarantineRestoreAndPermanentDeleteEnforceOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "policy-root", "policy root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.Register(ctx, "policy-admin", "policy admin password")
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := s.Register(ctx, "policy-moderator", "policy moderator password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}

	owned := insertPolicyPost(t, s, moderator.ID, "published", "policy-owned")
	other := insertPolicyPost(t, s, admin.ID, "published", "policy-other")
	if err = s.QuarantinePost(ctx, moderator, other, "moderators cannot quarantine another owner"); !errors.Is(err, ErrPermission) {
		t.Fatalf("cross-owner moderator quarantine err=%v", err)
	}
	if err = s.QuarantinePost(ctx, admin, owned, "review requested"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PostForDeletion(ctx, moderator, owned); !errors.Is(err, ErrPermission) {
		t.Fatalf("owner obtained quarantined post for permanent deletion: %v", err)
	}
	if _, err = s.PermanentDeletePost(ctx, moderator, owned); !errors.Is(err, ErrPermission) {
		t.Fatalf("owner permanently deleted quarantined post: %v", err)
	}
	if _, err = s.Post(ctx, owned); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("quarantined post remained public: %v", err)
	}
	reviewed, err := s.PostForReview(ctx, root, owned)
	if err != nil || reviewed.ID != owned || reviewed.QuarantineReason != "review requested" || reviewed.QuarantinePreviousStatus != "published" {
		t.Fatalf("super-admin review post=%+v err=%v", reviewed, err)
	}
	if err = s.RestorePost(ctx, root, owned); err != nil {
		t.Fatal(err)
	}
	if restored, err := s.Post(ctx, owned); err != nil || restored.Status != "published" {
		t.Fatalf("restore result=%+v err=%v", restored, err)
	}

	if _, err = s.PermanentDeletePost(ctx, admin, owned); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin permanently deleted another owner's post: %v", err)
	}
	if deleted, err := s.PermanentDeletePost(ctx, moderator, owned); err != nil || deleted.ID != owned {
		t.Fatalf("owner permanent deletion failed: post=%+v err=%v", deleted, err)
	}
	if _, err = s.Post(ctx, owned); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("permanently deleted post remains in database: %v", err)
	}
	if _, err = s.CreatePost(ctx, moderator, NewPost{Status: "published", OriginalPath: "originals/reuploaded.png", ThumbnailPath: "thumbs/reuploaded.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "policy-owned"}); err != nil {
		t.Fatalf("same SHA could not be uploaded after permanent deletion: %v", err)
	}
}

func TestQuarantineIsAbsentFromNormalDiscoveryMediaNavigationPoolsFavoritesAndExport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "visibility-root", "visibility root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Register(ctx, "visibility-owner", "visibility owner password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.Register(ctx, "visibility-viewer", "visibility viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	post, err := s.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/visibility.png", ThumbnailPath: "thumbs/visibility.jpg", MIMEType: "image/png", Width: 10, Height: 10, ByteSize: 10, SHA256: "visibility-quarantine", Tags: []string{"visible_tag"}})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := s.CreatePool(ctx, owner, "Visibility pool", "", "published", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddPostToPool(ctx, owner, pool.Slug, post.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetFavorite(ctx, viewer, post.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.QuarantinePost(ctx, root, post.ID, "visibility boundary"); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListPosts(ctx, "visible_tag", 1, 24); err != nil || page.Total != 0 {
		t.Fatalf("browse exposed quarantined post: page=%+v err=%v", page, err)
	}
	if _, err = s.Post(ctx, post.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("direct post view exposed quarantine: %v", err)
	}
	if _, err = s.PostForUser(ctx, post.ID, owner.ID, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("owner post view exposed quarantine: %v", err)
	}
	if visible, err := s.MediaVisibleToUser(ctx, "originals", post.OriginalPath, owner.ID, true); err != nil || visible {
		t.Fatalf("owner media visibility=%v err=%v", visible, err)
	}
	if visible, err := s.MediaVisibleToUser(ctx, "originals", post.OriginalPath, root.ID, false); err != nil || !visible {
		t.Fatalf("super-admin review media visibility=%v err=%v", visible, err)
	}
	if navigation, err := s.PostNeighbors(ctx, post.ID, owner.ID, PostNavigationContext{Source: "browse"}); !errors.Is(err, ErrNavigationUnavailable) || navigation.PreviousID != 0 || navigation.NextID != 0 {
		t.Fatalf("quarantined navigation=%+v err=%v", navigation, err)
	}
	if page, err := s.ListPostsForUploader(ctx, owner, UploaderPostFilter{}); err != nil || page.Total != 0 {
		t.Fatalf("uploader list exposed quarantine: page=%+v err=%v", page, err)
	}
	if visiblePool, err := s.Pool(ctx, pool.Slug, viewer.ID); err != nil || visiblePool.Count != 0 {
		t.Fatalf("pool exposed quarantined post: pool=%+v err=%v", visiblePool, err)
	}
	if favorites, err := s.Favorites(ctx, viewer.ID); err != nil || len(favorites) != 0 {
		t.Fatalf("favorites exposed quarantine: favorites=%+v err=%v", favorites, err)
	}
	if _, err = s.ExportPosts(ctx, viewer.ID, []int64{post.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("export exposed quarantine: %v", err)
	}
}
