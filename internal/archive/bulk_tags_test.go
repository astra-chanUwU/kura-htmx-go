package archive

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestApplyBulkTagDeltaPreservesOtherTagsAndIsIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-admin", "bulk admin password")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreatePost(ctx, admin, NewPost{
		Status: "published", OriginalPath: "originals/bulk-first.png", ThumbnailPath: "thumbs/bulk-first.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-first",
		Tags: []string{"keep_first", "remove_me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreatePost(ctx, admin, NewPost{
		Status: "published", OriginalPath: "originals/bulk-second.png", ThumbnailPath: "thumbs/bulk-second.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-second",
		Tags: []string{"keep_second", "remove_me"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ids := []int64{first.ID, second.ID, first.ID}
	if err = store.ApplyBulkTagDelta(ctx, admin, ids, "added_tag added_tag", "remove_me absent_tag"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		post, err := store.Post(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got := tagNames(post.Tags)
		for _, want := range []string{"added_tag", "keep_first", "keep_second"} {
			if want == "keep_first" && id == second.ID || want == "keep_second" && id == first.ID {
				continue
			}
			if !got[want] {
				t.Errorf("post %d lost or missed tag %q: %v", id, want, got)
			}
		}
		if got["remove_me"] || got["absent_tag"] {
			t.Errorf("post %d retained removed tag: %v", id, got)
		}
	}

	if err = store.ApplyBulkTagDelta(ctx, admin, ids, "added_tag", "absent_tag"); err != nil {
		t.Fatal(err)
	}
	var absentCount int
	if err = store.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='absent_tag'`).Scan(&absentCount); err != nil {
		t.Fatal(err)
	}
	if absentCount != 0 {
		t.Fatal("removing an absent tag created a global tag")
	}
	for _, id := range []int64{first.ID, second.ID} {
		post, err := store.Post(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(post.Tags) != 2 {
			t.Errorf("idempotent apply changed tag count for post %d: %+v", id, post.Tags)
		}
	}
}

func TestBulkTagDeltaRejectsMoreThanTheBoundedBatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-limit", "bulk limit password")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, MaxBulkTagPosts+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if err = store.ApplyBulkTagDelta(ctx, admin, ids, "too_many", ""); !errors.Is(err, ErrBulkSelectionLimit) {
		t.Fatalf("batch limit error=%v, want ErrBulkSelectionLimit", err)
	}
}

func TestPreviewBulkTagDeltaReportsChangesWithoutMutation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-preview", "bulk preview password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, NewPost{
		Status: "published", OriginalPath: "originals/preview.png", ThumbnailPath: "thumbs/preview.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-preview",
		Tags: []string{"keep", "remove_me"},
	})
	if err != nil {
		t.Fatal(err)
	}

	preview, err := store.PreviewBulkTagDelta(ctx, admin, []int64{post.ID}, "add_me keep", "remove_me absent")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Posts) != 1 || len(preview.Posts[0].Additions) != 1 || preview.Posts[0].Additions[0].Name != "add_me" || len(preview.Posts[0].AddNoOps) != 1 || preview.Posts[0].AddNoOps[0].Name != "keep" || len(preview.Posts[0].Removals) != 1 || preview.Posts[0].Removals[0].Name != "remove_me" || len(preview.Posts[0].RemoveNoOps) != 1 || preview.Posts[0].RemoveNoOps[0].Name != "absent" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	after, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tags) != 2 || !tagNames(after.Tags)["remove_me"] {
		t.Fatalf("preview mutated post tags: %+v", after.Tags)
	}
}

func TestBulkTagDeltaRejectsContradictionsAndInvalidSelectionsAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "bulk-atomic", "bulk atomic password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, NewPost{
		Status: "published", OriginalPath: "originals/atomic.png", ThumbnailPath: "thumbs/atomic.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-atomic",
		Tags: []string{"artist:person"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ApplyBulkTagDelta(ctx, admin, []int64{post.ID}, "new_tag", "new_tag"); !errors.Is(err, ErrTagCategoryConflict) {
		t.Fatalf("contradictory delta error=%v, want category conflict", err)
	}
	if err = store.ApplyBulkTagDelta(ctx, admin, []int64{post.ID}, "character:person", ""); !errors.Is(err, ErrTagCategoryConflict) {
		t.Fatalf("category conflict error=%v, want category conflict", err)
	}
	if _, err = store.PreviewBulkTagDelta(ctx, admin, []int64{post.ID}, "character:person", ""); !errors.Is(err, ErrTagCategoryConflict) {
		t.Fatalf("preview category conflict error=%v, want category conflict", err)
	}
	if err = store.ApplyBulkTagDelta(ctx, admin, []int64{post.ID, 999999}, "new_tag", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid selection error=%v, want sql.ErrNoRows", err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='new_tag'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid selection left a new tag behind")
	}
	after, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tags) != 1 || after.Tags[0].Name != "person" {
		t.Fatalf("rejected bulk operation changed tags: %+v", after.Tags)
	}
}

func TestBulkTagDeltaUsesCurrentActorOnPreviewAndApply(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	root, err := store.BootstrapSuperAdmin(ctx, "bulk-authority", "bulk authority password")
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := store.Register(ctx, "bulk-moderator", "bulk moderator password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, moderator, NewPost{
		Status: "published", OriginalPath: "originals/authority.png", ThumbnailPath: "thumbs/authority.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "bulk-authority",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PreviewBulkTagDelta(ctx, moderator, []int64{post.ID}, "preview_tag", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, root, moderator.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err = store.ApplyBulkTagDelta(ctx, moderator, []int64{post.ID}, "applied_tag", ""); !errors.Is(err, ErrPermission) {
		t.Fatalf("stale moderator applied tags: %v", err)
	}
	after, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tags) != 0 {
		t.Fatalf("stale actor changed tags: %+v", after.Tags)
	}
}

func tagNames(tags []Tag) map[string]bool {
	result := map[string]bool{}
	for _, tag := range tags {
		result[tag.Name] = true
	}
	return result
}
