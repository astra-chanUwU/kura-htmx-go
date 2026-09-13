package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func insertExportPost(t *testing.T, s *Store, status, mimeType, hash string, uploaderID *int64, byteSize int64) int64 {
	t.Helper()
	var uploader any
	if uploaderID != nil {
		uploader = *uploaderID
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,source,published_at,uploader_id,original_filename) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,?,?,?)`, status, "originals/"+hash+".jpg", "thumbs/"+hash+".jpg", mimeType, 640, 480, byteSize, hash, "https://example.test/"+hash, uploader, "camera/"+hash+".jpg")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestExportPostsPreservesSelectionOrderAndDeduplicatesIDs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := insertExportPost(t, s, "published", "image/jpeg", "export-first", nil, 12)
	second := insertExportPost(t, s, "published", "image/png", "export-second", nil, 23)
	if _, err := s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES('blue','blue','general'),('sample_artist','sample artist','artist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name='blue' UNION ALL SELECT ?,id FROM tags WHERE name='sample_artist'`, second, second); err != nil {
		t.Fatal(err)
	}

	plan, err := s.ExportPosts(ctx, 0, []int64{second, first, second})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source.Type != "selected" || len(plan.Posts) != 2 || plan.Posts[0].ID != second || plan.Posts[1].ID != first || plan.TotalBytes != 35 {
		t.Fatalf("unexpected export plan: %+v", plan)
	}
	if len(plan.Posts[0].Tags) != 2 || plan.Posts[0].Tags[0].Category != "artist" || plan.Posts[0].Tags[1].Category != "general" {
		t.Fatalf("export plan lost canonical tag categories: %+v", plan.Posts[0].Tags)
	}
}

func TestExportPoolPreservesPoolOrderAndRequiresCurrentVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner, err := s.Register(ctx, "export-pool-owner", "export pool owner password")
	if err != nil {
		t.Fatal(err)
	}
	first := insertExportPost(t, s, "published", "image/gif", "export-pool-first", nil, 12)
	second := insertExportPost(t, s, "published", "image/png", "export-pool-second", nil, 23)
	pool, err := s.CreatePool(ctx, owner, "Ordered Export", "portable", "published", fmt.Sprintf("%d %d", second, first))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := s.ExportPool(ctx, owner.ID, pool.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source.Type != "pool" || plan.Source.Slug != pool.Slug || plan.Source.Name != pool.Name || len(plan.Posts) != 2 || plan.Posts[0].ID != second || plan.Posts[1].ID != first {
		t.Fatalf("unexpected ordered pool plan: %+v", plan)
	}

	if _, err = s.DB.Exec(`UPDATE posts SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, first); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExportPool(ctx, owner.ID, pool.Slug); !errors.Is(err, ErrExportUnavailable) {
		t.Fatalf("deleted pool member was silently omitted: %v", err)
	}
}

func TestExportPostsUsesCurrentPostAuthorizationAndRejectsUnsupportedMIME(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner, err := s.Register(ctx, "export-post-owner", "export post owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register(ctx, "export-post-other", "export post other password")
	if err != nil {
		t.Fatal(err)
	}
	draft := insertExportPost(t, s, "draft", "image/png", "export-private-draft", &owner.ID, 10)
	unsupported := insertExportPost(t, s, "published", "image/webp", "export-webp", nil, 10)

	if _, err = s.ExportPosts(ctx, other.ID, []int64{draft}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other viewer accessed private draft: %v", err)
	}
	if _, err = s.ExportPosts(ctx, owner.ID, []int64{draft}); err != nil {
		t.Fatalf("owner could not export own draft: %v", err)
	}
	if _, err = s.ExportPosts(ctx, 0, []int64{unsupported}); !errors.Is(err, ErrExportUnsupportedMIME) {
		t.Fatalf("unsupported MIME was accepted: %v", err)
	}
}

func TestExportPostsEnforcesCountAndByteLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ids := make([]int64, 0, ExportMaxPosts+1)
	for i := 0; i < ExportMaxPosts+1; i++ {
		ids = append(ids, insertExportPost(t, s, "published", "image/png", fmt.Sprintf("export-count-%d", i), nil, 1))
	}
	if _, err := s.ExportPosts(ctx, 0, ids); !errors.Is(err, ErrExportSelectionLimit) {
		t.Fatalf("count limit was not enforced: %v", err)
	}
	overBytes := insertExportPost(t, s, "published", "image/png", "export-over-bytes", nil, ExportMaxBytes+1)
	if _, err := s.ExportPosts(ctx, 0, []int64{overBytes}); !errors.Is(err, ErrExportBytesLimit) {
		t.Fatalf("byte limit was not enforced: %v", err)
	}
}
