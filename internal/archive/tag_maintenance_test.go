package archive

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTagMaintenanceRenameRequiresFreshPreviewAndKeepsCategory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "tag-maintenance-root", "tag maintenance root password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := s.CreatePost(ctx, root, NewPost{Status: "published", OriginalPath: "originals/tag-maintenance-rename.png", ThumbnailPath: "thumbs/tag-maintenance-rename.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tag-maintenance-rename", Tags: []string{"artist:old_artist"}})
	if err != nil {
		t.Fatal(err)
	}
	noOp, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_artist", Target: "old_artist"})
	if err != nil || !noOp.NoOp || noOp.AffectedPostCount != 1 {
		t.Fatalf("no-op preview=%+v err=%v", noOp, err)
	}
	if err = s.ApplyTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_artist", Target: "old_artist"}, noOp); !errors.Is(err, ErrTagMaintenanceNoOp) {
		t.Fatalf("no-op apply error=%v, want ErrTagMaintenanceNoOp", err)
	}

	preview, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_artist", Target: "new_artist", Reason: "canonical spelling"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.AffectedPostCount != 1 || preview.Source.Category != "artist" || preview.Fingerprint == "" {
		t.Fatalf("unexpected rename preview: %+v", preview)
	}
	if err = s.ApplyTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_artist", Target: "new_artist", Reason: "canonical spelling"}, preview); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Post(ctx, post.ID)
	if err != nil || len(updated.Tags) != 1 || updated.Tags[0].Name != "new_artist" || updated.Tags[0].Category != "artist" {
		t.Fatalf("rename lost category or relation: post=%+v err=%v", updated, err)
	}
	var eventType, reason, actor string
	if err = s.DB.QueryRow(`SELECT event_type,reason,actor_username FROM audit_events ORDER BY id DESC LIMIT 1`).Scan(&eventType, &reason, &actor); err != nil {
		t.Fatal(err)
	}
	if eventType != "tag_rename" || actor != root.Username || !containsAll(reason, "old_artist", "new_artist", "artist", "1", "canonical spelling") {
		t.Fatalf("rename audit event = type=%q actor=%q reason=%q", eventType, actor, reason)
	}

	stale, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "new_artist", Target: "final_artist"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256) VALUES('published','x','x','image/png',1,1,1,'tag-maintenance-stale')`)
	if err != nil {
		t.Fatal(err)
	}
	stalePostID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name='new_artist'`, stalePostID); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "new_artist", Target: "final_artist"}, stale); !errors.Is(err, ErrTagMaintenanceStale) {
		t.Fatalf("stale rename error=%v, want ErrTagMaintenanceStale", err)
	}
}

func TestTagMaintenanceMergeDeduplicatesAndRejectsCategoryConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "tag-maintenance-merge-root", "tag maintenance merge root password")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreatePost(ctx, root, NewPost{Status: "published", OriginalPath: "originals/tag-maintenance-first.png", ThumbnailPath: "thumbs/tag-maintenance-first.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tag-maintenance-first", Tags: []string{"artist:source_artist", "artist:target_artist"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreatePost(ctx, root, NewPost{Status: "published", OriginalPath: "originals/tag-maintenance-second.png", ThumbnailPath: "thumbs/tag-maintenance-second.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tag-maintenance-second", Tags: []string{"artist:source_artist"}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "merge", Source: "source_artist", Target: "target_artist", Reason: "merge duplicate vocabulary"})
	if err != nil || preview.AffectedPostCount != 2 {
		t.Fatalf("merge preview=%+v err=%v", preview, err)
	}
	if err = s.ApplyTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "merge", Source: "source_artist", Target: "target_artist", Reason: "merge duplicate vocabulary"}, preview); err != nil {
		t.Fatal(err)
	}
	var sourceCount, targetCount, firstRelations, secondRelations int
	if err = s.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='source_artist'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='target_artist'`).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=? AND t.name='target_artist'`, first.ID).Scan(&firstRelations); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=? AND t.name='target_artist'`, second.ID).Scan(&secondRelations); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || targetCount != 1 || firstRelations != 1 || secondRelations != 1 {
		t.Fatalf("merge did not deduplicate: source=%d target=%d first=%d second=%d", sourceCount, targetCount, firstRelations, secondRelations)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE event_type='tag_merge'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("merge audit count=%d", sourceCount)
	}

	if _, err = s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES('general_target','general target','general')`); err != nil {
		t.Fatal(err)
	}
	conflictPreview, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "merge", Source: "target_artist", Target: "general_target"})
	if !errors.Is(err, ErrTagMaintenanceCategoryConflict) || len(conflictPreview.Conflicts) == 0 {
		t.Fatalf("category conflict preview=%+v err=%v", conflictPreview, err)
	}
}

func TestListTagInventoryIsBoundedAndSuperAdminOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "tag-inventory-root", "tag inventory root password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.Register(ctx, "tag-inventory-viewer", "tag inventory viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListTagInventory(ctx, viewer, TagInventoryFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer inventory error=%v, want ErrPermission", err)
	}
	for i := 0; i < 3; i++ {
		if _, err = s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?)`, "inventory_"+string(rune('a'+i)), "Inventory "+string(rune('A'+i)), "meta"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListTagInventory(ctx, root, TagInventoryFilter{Query: "inventory", Category: "meta", Page: 2, PerPage: 2})
	if err != nil || page.Total != 3 || page.Pages != 2 || page.Page != 2 || len(page.Tags) != 1 {
		t.Fatalf("inventory page=%+v err=%v", page, err)
	}
}

func TestTagMaintenanceRenameCollisionAndApplyRollback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "tag-maintenance-rollback-root", "tag maintenance rollback root password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePost(ctx, root, NewPost{Status: "published", OriginalPath: "originals/tag-collision.png", ThumbnailPath: "thumbs/tag-collision.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tag-collision", Tags: []string{"old_collision", "new_collision"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_collision", Target: "new_collision"}); !errors.Is(err, ErrTagMaintenanceConflict) {
		t.Fatalf("rename collision error=%v, want ErrTagMaintenanceConflict", err)
	}
	preview, err := s.PreviewTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_collision", Target: "rolled_back"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE TRIGGER fail_tag_rename_audit BEFORE INSERT ON audit_events WHEN NEW.event_type='tag_rename' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyTagMaintenance(ctx, root, TagMaintenanceRequest{Operation: "rename", Source: "old_collision", Target: "rolled_back", Reason: strings.Repeat("x", 501)}, preview); err == nil {
		t.Fatal("rename unexpectedly committed after audit failure")
	}
	var oldCount, newCount int
	if err = s.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='old_collision'`).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='rolled_back'`).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 1 || newCount != 0 {
		t.Fatalf("failed rename was not atomic: old=%d new=%d", oldCount, newCount)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
