package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCrossOwnerMetadataAndBulkChangesCreateAuditEventsButOwnChangesDoNot(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-root", "audit root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-owner", "audit owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-admin", "audit admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	owned, err := store.CreatePost(ctx, owner, NewPost{
		Status: "published", OriginalPath: "originals/audit-owned.png", ThumbnailPath: "thumbs/audit-owned.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "audit-owned", Source: "https://before.example",
		Tags: []string{"before_tag"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownPost, err := store.CreatePost(ctx, root, NewPost{
		Status: "published", OriginalPath: "originals/audit-own.png", ThumbnailPath: "thumbs/audit-own.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "audit-own", Tags: []string{"root_tag"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = store.UpdatePost(ctx, admin, owned.ID, "https://after.example", "after_tag", "draft"); err != nil {
		t.Fatal(err)
	}
	if err = store.ApplyBulkTagDelta(ctx, admin, []int64{owned.ID}, "bulk_tag", "after_tag"); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, root, ownPost.ID, "", "root_tag changed", "published"); err != nil {
		t.Fatal(err)
	}

	var count int
	if err = store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE post_id=?`, owned.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("cross-owner audit count=%d, want 2", count)
	}
	if err = store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE post_id=?`, ownPost.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("own-upload audit count=%d, want 0", count)
	}
}

func TestAuditEventsRequireCurrentSuperAdminAndKeepExistingModerationEvents(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-events-root", "audit events root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-events-admin", "audit events admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "audit-events-viewer", "audit events viewer password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-events-owner", "audit events owner password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	draft, err := store.CreatePost(ctx, owner, NewPost{Status: "draft", OriginalPath: "originals/events-draft.png", ThumbnailPath: "thumbs/events-draft.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "events-draft"})
	if err != nil {
		t.Fatal(err)
	}
	published, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/events-published.png", ThumbnailPath: "thumbs/events-published.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "events-published"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err = store.RestorePost(ctx, root, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.QuarantinePost(ctx, root, published.ID, "audit review"); err != nil {
		t.Fatal(err)
	}
	if err = store.RestorePost(ctx, root, published.ID); err != nil {
		t.Fatal(err)
	}

	if _, err = store.ListAuditEvents(ctx, admin, AuditFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin read audit log: %v", err)
	}
	if _, err = store.ListAuditEvents(ctx, viewer, AuditFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer read audit log: %v", err)
	}
	page, err := store.ListAuditEvents(ctx, root, AuditFilter{PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range page.Events {
		seen[event.EventType] = true
	}
	for _, eventType := range []string{"quarantine", "restore", "role_change", "role_change_quarantine"} {
		if !seen[eventType] {
			t.Fatalf("audit log omitted %q event: %+v", eventType, page.Events)
		}
	}

	if err = store.UpdatePost(ctx, admin, published.ID, "https://stale.example", "stale_event", "published"); err != nil {
		t.Fatal(err)
	}
	metadataPage, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: published.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(metadataPage.Events) != 1 {
		t.Fatalf("stale-role metadata page=%+v err=%v", metadataPage, err)
	}
	staleEventID := metadataPage.Events[0].ID
	staleRoot := root
	if err = store.TransferSuperAdmin(ctx, root, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListAuditEvents(ctx, staleRoot, AuditFilter{}); !errors.Is(err, ErrPermission) {
		t.Fatalf("stale super-admin read audit log: %v", err)
	}
	if err = store.RevertAuditEvent(ctx, staleRoot, staleEventID); !errors.Is(err, ErrPermission) {
		t.Fatalf("stale super-admin reverted audit event: %v", err)
	}
}

func TestAuditListPaginatesAndFiltersByPostAndActor(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-page-root", "audit page root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-page-owner", "audit page owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-page-admin", "audit page admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/page-first.png", ThumbnailPath: "thumbs/page-first.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "page-first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/page-second.png", ThumbnailPath: "thumbs/page-second.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "page-second"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err = store.UpdatePost(ctx, admin, first.ID, fmt.Sprintf("https://page.example/%d", i), fmt.Sprintf("page_%d", i), "published"); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.UpdatePost(ctx, admin, second.ID, "https://other-page.example", "other_page", "published"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAuditEvents(ctx, root, AuditFilter{EventType: "metadata_change", ActorID: admin.ID, PostID: first.ID, Page: 2, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 25 || page.Page != 2 || page.Pages != 3 || len(page.Events) != 10 {
		t.Fatalf("unexpected filtered audit pagination: %+v", page)
	}
	for _, event := range page.Events {
		if event.PostID != first.ID || event.ActorID != admin.ID || event.Uploader != owner.Username {
			t.Fatalf("filtered audit exposed unrelated identity/event: %+v", event)
		}
	}
}

func TestAuditRevertRestoresExactSnapshotAndRejectsConflictsOrUnavailablePosts(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-revert-root", "audit revert root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-revert-owner", "audit revert owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-revert-admin", "audit revert admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	post, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/revert.png", ThumbnailPath: "thumbs/revert.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "revert-post", Source: "https://before.example", Tags: []string{"artist:before_artist", "before_general"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, "https://after.example", "after_general", "draft"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: post.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("metadata audit page=%+v err=%v", page, err)
	}
	eventID := page.Events[0].ID
	if err = store.RevertAuditEvent(ctx, root, eventID); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Source != before.Source || restored.Status != before.Status || restored.PublishedAt != before.PublishedAt || !sameAuditTags(restored.Tags, before.Tags) {
		t.Fatalf("revert did not restore exact post: before=%+v restored=%+v", before, restored)
	}
	page, err = store.ListAuditEvents(ctx, root, AuditFilter{PostID: post.ID, PerPage: 10})
	if err != nil || len(page.Events) != 2 || page.Events[0].EventType != "revert" || page.Events[0].RevertedEventID != eventID {
		t.Fatalf("revert event page=%+v err=%v", page, err)
	}

	conflict, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/conflict.png", ThumbnailPath: "thumbs/conflict.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "revert-conflict", Tags: []string{"conflict_before"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, conflict.ID, "https://first.example", "first", "published"); err != nil {
		t.Fatal(err)
	}
	conflictPage, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: conflict.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(conflictPage.Events) != 1 {
		t.Fatalf("conflict event page=%+v err=%v", conflictPage, err)
	}
	if err = store.UpdatePost(ctx, admin, conflict.ID, "https://newer.example", "newer", "published"); err != nil {
		t.Fatal(err)
	}
	if err = store.RevertAuditEvent(ctx, root, conflictPage.Events[0].ID); !errors.Is(err, ErrAuditConflict) {
		t.Fatalf("stale revert err=%v, want ErrAuditConflict", err)
	}
	current, err := store.Post(ctx, conflict.ID)
	if err != nil || current.Source != "https://newer.example" {
		t.Fatalf("conflicting edit was overwritten: post=%+v err=%v", current, err)
	}

	quarantined, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/quarantined.png", ThumbnailPath: "thumbs/quarantined.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "revert-quarantined"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, quarantined.ID, "https://quarantined.example", "quarantined", "published"); err != nil {
		t.Fatal(err)
	}
	quarantinePage, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: quarantined.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(quarantinePage.Events) != 1 {
		t.Fatalf("quarantine event page=%+v err=%v", quarantinePage, err)
	}
	if err = store.QuarantinePost(ctx, root, quarantined.ID, "hold"); err != nil {
		t.Fatal(err)
	}
	if err = store.RevertAuditEvent(ctx, root, quarantinePage.Events[0].ID); !errors.Is(err, ErrAuditNotRevertable) {
		t.Fatalf("quarantined revert err=%v, want ErrAuditNotRevertable", err)
	}

	deleted, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/deleted.png", ThumbnailPath: "thumbs/deleted.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "revert-deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, deleted.ID, "https://deleted.example", "deleted", "published"); err != nil {
		t.Fatal(err)
	}
	deletedPage, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: deleted.ID, EventType: "metadata_change", PerPage: 10})
	if err != nil || len(deletedPage.Events) != 1 {
		t.Fatalf("deleted event page=%+v err=%v", deletedPage, err)
	}
	if err = store.SoftDeletePost(ctx, root, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.RevertAuditEvent(ctx, root, deletedPage.Events[0].ID); !errors.Is(err, ErrAuditNotRevertable) {
		t.Fatalf("deleted revert err=%v, want ErrAuditNotRevertable", err)
	}
	if _, err = store.PermanentDeletePost(ctx, root, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if retained, err := store.ListAuditEvents(ctx, root, AuditFilter{PostID: deleted.ID, PerPage: 10}); err != nil || len(retained.Events) != 1 {
		t.Fatalf("post deletion removed audit history: page=%+v err=%v", retained, err)
	}
	if _, err = store.Post(ctx, deleted.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted post still exists: %v", err)
	}
}

func sameAuditTags(left, right []Tag) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name != right[i].Name || left[i].Category != right[i].Category {
			return false
		}
	}
	return true
}

func TestAuditSnapshotAndReasonBoundsAreEnforcedAndDecodedDataIsValidated(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "audit-bounds-root", "audit bounds root password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Register(ctx, "audit-bounds-owner", "audit bounds owner password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Register(ctx, "audit-bounds-admin", "audit bounds admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, owner.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, owner, NewPost{Status: "published", OriginalPath: "originals/bounds.png", ThumbnailPath: "thumbs/bounds.jpg", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "audit-bounds"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, strings.Repeat("x", maxAuditSourceLength+1), "bounded", "published"); !errors.Is(err, ErrAuditSnapshot) {
		t.Fatalf("oversized snapshot source err=%v, want ErrAuditSnapshot", err)
	}
	unchanged, err := store.Post(ctx, post.ID)
	if err != nil || unchanged.Source != "" {
		t.Fatalf("oversized snapshot mutation was committed: post=%+v err=%v", unchanged, err)
	}
	if err = store.QuarantinePost(ctx, root, post.ID, strings.Repeat("r", maxAuditReasonLength+100)); err != nil {
		t.Fatal(err)
	}
	var reasonLength int
	if err = store.DB.QueryRowContext(ctx, `SELECT length(reason) FROM audit_events WHERE event_type='quarantine' AND post_id=?`, post.ID).Scan(&reasonLength); err != nil {
		t.Fatal(err)
	}
	if reasonLength != maxAuditReasonLength {
		t.Fatalf("quarantine audit reason length=%d, want %d", reasonLength, maxAuditReasonLength)
	}
	if _, err = store.DB.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,actor_username,post_id,before_snapshot,after_snapshot) VALUES('metadata_change',?,?,?, ?, ?)`, root.ID, root.Username, post.ID, `{"status":"invalid"}`, `{"status":"invalid"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListAuditEvents(ctx, root, AuditFilter{EventType: "metadata_change"}); !errors.Is(err, ErrAuditSnapshot) {
		t.Fatalf("invalid stored snapshot err=%v, want ErrAuditSnapshot", err)
	}
}
