package archive

import (
	"context"
	"errors"
	"testing"
)

func TestListPostsForUploaderScopesCurrentActiveOwnerAndStatus(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "uploads-root", "uploads root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Register(ctx, "uploads-owner", "uploads owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "uploads-other", "uploads other password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}

	insert := func(hash, status string, uploaderID int64, deleted bool) int64 {
		t.Helper()
		deletedAt := any(nil)
		if deleted {
			deletedAt = "2026-09-17T00:00:00Z"
		}
		result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, status, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", 10, 20, 100, hash, "2026-09-17T00:00:00Z", uploaderID, deletedAt)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	ownedDraft := insert("uploads-owned-draft", "draft", owner.ID, false)
	ownedPublished := insert("uploads-owned-published", "published", owner.ID, false)
	insert("uploads-other-published", "published", other.ID, false)
	insert("uploads-owned-deleted", "published", owner.ID, true)

	page, err := s.ListPostsForUploader(ctx, owner, UploaderPostFilter{Status: "all", Page: 1, PerPage: 24})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Posts) != 2 || page.Posts[0].UploaderID != owner.ID || page.Posts[1].UploaderID != owner.ID {
		t.Fatalf("owner query was not isolated: total=%d posts=%+v", page.Total, page.Posts)
	}

	draftPage, err := s.ListPostsForUploader(ctx, owner, UploaderPostFilter{Status: "draft", Page: 1, PerPage: 24})
	if err != nil || draftPage.Total != 1 || len(draftPage.Posts) != 1 || draftPage.Posts[0].ID != ownedDraft {
		t.Fatalf("draft filter wrong: page=%+v err=%v", draftPage, err)
	}
	publishedPage, err := s.ListPostsForUploader(ctx, owner, UploaderPostFilter{Status: "published", Page: 1, PerPage: 24})
	if err != nil || publishedPage.Total != 1 || len(publishedPage.Posts) != 1 || publishedPage.Posts[0].ID != ownedPublished {
		t.Fatalf("published filter wrong: page=%+v err=%v", publishedPage, err)
	}

	stale := owner
	if err = s.SetUserRole(ctx, root, owner.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListPostsForUploader(ctx, stale, UploaderPostFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("stale viewer could enumerate uploads: %v", err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserSuspended(ctx, root, owner.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListPostsForUploader(ctx, stale, UploaderPostFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("suspended owner could enumerate uploads: %v", err)
	}
}

func TestOwnedPostStatusMutationRequiresCurrentOwnerAndPreservesMetadata(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "status-root", "status root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Register(ctx, "status-owner", "status owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "status-other", "status other password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	post, err := s.CreatePost(ctx, owner, NewPost{Status: "draft", OriginalPath: "originals/status.png", ThumbnailPath: "thumbs/status.jpg", MIMEType: "image/png", OriginalFilename: "status.png", Width: 10, Height: 20, ByteSize: 100, SHA256: "status-post", Source: "https://example.test/source", Tags: []string{"status_tag"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOwnedPostStatus(ctx, other, post.ID, "published"); !errors.Is(err, ErrPermission) {
		t.Fatalf("other moderator changed status: %v", err)
	}
	if err = s.SetOwnedPostStatus(ctx, owner, post.ID, "published"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Post(ctx, post.ID)
	if err != nil || updated.Status != "published" || updated.Source != post.Source || len(updated.Tags) != 1 || updated.Tags[0].Name != "status_tag" || updated.PublishedAt == "" {
		t.Fatalf("publish lost metadata: post=%+v err=%v", updated, err)
	}
	if err = s.SetOwnedPostStatus(ctx, owner, post.ID, "draft"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.PostForUser(ctx, post.ID, owner.ID, false)
	if err != nil || updated.Status != "draft" || updated.PublishedAt != "" {
		t.Fatalf("unpublish did not clear publication: post=%+v err=%v", updated, err)
	}
	if _, err = s.DB.Exec(`UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetOwnedPostStatus(ctx, owner, post.ID, "published"); !errors.Is(err, ErrPermission) {
		t.Fatalf("suspended owner changed status: %v", err)
	}
}
