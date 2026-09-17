package archive

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func insertNavigationPost(t *testing.T, s *Store, hash, status, publishedAt string, uploaderID int64, deleted bool, tags ...string) int64 {
	t.Helper()
	var deletedAt any
	if deleted {
		deletedAt = "2026-09-17T00:00:00Z"
	}
	var owner any
	if uploaderID > 0 {
		owner = uploaderID
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, status, "originals/"+hash+".png", "thumbs/"+hash+".jpg", "image/png", 10, 10, 100, hash, publishedAt, owner, deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	for _, tag := range tags {
		if _, err = s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, tag, tag, "general"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, id, tag); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestPostNeighborsUsePublicBrowseOrderingAndSearchContext(t *testing.T) {
	s := testStore(t)
	first := insertNavigationPost(t, s, "browse-first", "published", "2026-09-17T03:00:00Z", 0, false, "needle")
	current := insertNavigationPost(t, s, "browse-current", "published", "2026-09-17T02:00:00Z", 0, false, "needle")
	last := insertNavigationPost(t, s, "browse-last", "published", "2026-09-17T01:00:00Z", 0, false, "needle")
	insertNavigationPost(t, s, "browse-other", "published", "2026-09-17T04:00:00Z", 0, false, "other")
	insertNavigationPost(t, s, "browse-draft", "draft", "2026-09-17T05:00:00Z", 0, false, "needle")
	insertNavigationPost(t, s, "browse-deleted", "published", "2026-09-17T06:00:00Z", 0, true, "needle")

	navigation, err := s.PostNeighbors(context.Background(), current, 0, PostNavigationContext{Source: "browse", Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.PreviousID != first || navigation.NextID != last {
		t.Fatalf("browse neighbors = %+v, want previous=%d next=%d", navigation, first, last)
	}

	firstNavigation, err := s.PostNeighbors(context.Background(), first, 0, PostNavigationContext{Source: "browse", Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if firstNavigation.PreviousID != 0 || firstNavigation.NextID != current {
		t.Fatalf("first browse neighbors = %+v", firstNavigation)
	}
}

func TestPostNeighborsUsePoolMembershipOrderAndPoolAuthorization(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner, err := s.Register(ctx, "navigation-pool-owner", "navigation pool owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "navigation-pool-other", "navigation pool other password")
	if err != nil {
		t.Fatal(err)
	}
	first := insertNavigationPost(t, s, "pool-first", "published", "2026-09-17T01:00:00Z", 0, false)
	current := insertNavigationPost(t, s, "pool-current", "published", "2026-09-17T02:00:00Z", 0, false)
	last := insertNavigationPost(t, s, "pool-last", "published", "2026-09-17T03:00:00Z", 0, false)
	pool, err := s.CreatePool(ctx, owner, "Navigation pool", "", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePool(ctx, owner, pool.Slug, pool.Name, pool.Description, pool.Status, formatIDs(last, current, first)); err != nil {
		t.Fatal(err)
	}

	navigation, err := s.PostNeighbors(ctx, current, owner.ID, PostNavigationContext{Source: "pool", PoolSlug: pool.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.PreviousID != last || navigation.NextID != first {
		t.Fatalf("pool neighbors = %+v, want previous=%d next=%d", navigation, last, first)
	}
	if _, err = s.PostNeighbors(ctx, current, other.ID, PostNavigationContext{Source: "pool", PoolSlug: pool.Slug}); !errors.Is(err, ErrNavigationUnavailable) {
		t.Fatalf("private pool was navigable by another viewer: %v", err)
	}
}

func TestPostNeighborsUseOwnedUploadsStatusAndCurrentOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "navigation-uploads-root", "navigation uploads root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Register(ctx, "navigation-uploads-owner", "navigation uploads owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "navigation-uploads-other", "navigation uploads other password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, other.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	first := insertNavigationPost(t, s, "uploads-first", "draft", "2026-09-17T01:00:00Z", owner.ID, false)
	current := insertNavigationPost(t, s, "uploads-current", "draft", "2026-09-17T02:00:00Z", owner.ID, false)
	last := insertNavigationPost(t, s, "uploads-last", "draft", "2026-09-17T03:00:00Z", owner.ID, false)
	insertNavigationPost(t, s, "uploads-published", "published", "2026-09-17T04:00:00Z", owner.ID, false)
	insertNavigationPost(t, s, "uploads-other", "draft", "2026-09-17T05:00:00Z", other.ID, false)

	navigation, err := s.PostNeighbors(ctx, current, owner.ID, PostNavigationContext{Source: "uploads", Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.PreviousID != last || navigation.NextID != first {
		t.Fatalf("uploads neighbors = %+v, want previous=%d next=%d", navigation, last, first)
	}
	if _, err = s.PostNeighbors(ctx, current, other.ID, PostNavigationContext{Source: "uploads", Status: "draft"}); !errors.Is(err, ErrNavigationUnavailable) {
		t.Fatalf("another uploader navigated private uploads: %v", err)
	}
	if err = s.SetUserRole(ctx, root, owner.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PostNeighbors(ctx, current, owner.ID, PostNavigationContext{Source: "uploads", Status: "draft"}); !errors.Is(err, ErrNavigationUnavailable) {
		t.Fatalf("demoted uploader retained navigation: %v", err)
	}
}

func TestPostNeighborsUseSuperAdminFiltersAndRejectChangedAuthorization(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "navigation-admin-root", "navigation admin root password")
	if err != nil {
		t.Fatal(err)
	}
	uploader, err := s.Register(ctx, "navigation-admin-uploader", "navigation admin uploader password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "navigation-admin-other", "navigation admin other password")
	if err != nil {
		t.Fatal(err)
	}
	first := insertNavigationPost(t, s, "admin-first", "draft", "2026-09-17T01:00:00Z", uploader.ID, false)
	current := insertNavigationPost(t, s, "admin-current", "draft", "2026-09-17T02:00:00Z", uploader.ID, false)
	last := insertNavigationPost(t, s, "admin-last", "draft", "2026-09-17T03:00:00Z", uploader.ID, false)
	insertNavigationPost(t, s, "admin-other-uploader", "draft", "2026-09-17T04:00:00Z", other.ID, false)
	insertNavigationPost(t, s, "admin-published", "published", "2026-09-17T05:00:00Z", uploader.ID, false)
	insertNavigationPost(t, s, "admin-deleted", "draft", "2026-09-17T06:00:00Z", uploader.ID, true)

	navigation, err := s.PostNeighbors(ctx, current, root.ID, PostNavigationContext{Source: "admin", Status: "draft", UploaderID: uploader.ID})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.PreviousID != last || navigation.NextID != first {
		t.Fatalf("admin neighbors = %+v, want previous=%d next=%d", navigation, last, first)
	}
	if _, err = s.PostNeighbors(ctx, current, uploader.ID, PostNavigationContext{Source: "admin", Status: "draft", UploaderID: uploader.ID}); !errors.Is(err, ErrNavigationUnavailable) {
		t.Fatalf("non-super-admin navigated oversight context: %v", err)
	}
	if _, err = s.DB.Exec(`UPDATE users SET role='viewer',is_super_admin=0 WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PostNeighbors(ctx, current, root.ID, PostNavigationContext{Source: "admin", Status: "draft", UploaderID: uploader.ID}); !errors.Is(err, ErrNavigationUnavailable) {
		t.Fatalf("former super admin retained oversight navigation: %v", err)
	}
}

func formatIDs(ids ...int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, " ")
}
