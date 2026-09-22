package archive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseTagInputReadsCategoryPrefixBeforeNormalization(t *testing.T) {
	tags, err := parseTagInput([]string{"ARTIST:Sample_Artist", "black_hair", "meta:Source-Note"})
	if err != nil {
		t.Fatal(err)
	}
	want := []parsedTag{
		{Name: "black_hair", Category: "general"},
		{Name: "sample_artist", Category: "artist", Explicit: true},
		{Name: "source_note", Category: "meta", Explicit: true},
	}
	if len(tags) != len(want) {
		t.Fatalf("parsed %d tags, want %d: %+v", len(tags), len(want), tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Errorf("tag %d = %+v, want %+v", i, tags[i], want[i])
		}
	}
}

func TestCreateAndUpdateTagsPreserveCanonicalCategoriesAndRejectConflictsAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "tag-admin", "tag admin password")
	if err != nil {
		t.Fatal(err)
	}

	post, err := store.CreatePost(ctx, admin, NewPost{
		Status: "published", OriginalPath: "originals/tagged.png", ThumbnailPath: "thumbs/tagged.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "tagged-post",
		Tags: []string{"artist:Sample_Artist", "black_hair"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(post.Tags) != 2 || post.Tags[0].Category != "artist" || post.Tags[1].Category != "general" {
		t.Fatalf("created tags lost categories: %+v", post.Tags)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, "unchanged", "sample_artist", "published"); err != nil {
		t.Fatal(err)
	}
	plain, err := store.Post(ctx, post.ID)
	if err != nil || len(plain.Tags) != 1 || plain.Tags[0].Category != "artist" {
		t.Fatalf("unprefixed existing tag changed category: %+v err=%v", plain, err)
	}

	before, err := store.Post(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, admin, post.ID, "unchanged", "character:sample_artist", "draft"); !errors.Is(err, ErrTagCategoryConflict) {
		t.Fatalf("category conflict error = %v, want ErrTagCategoryConflict", err)
	}
	after, err := store.PostForUser(ctx, post.ID, admin.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Source != before.Source || len(after.Tags) != len(before.Tags) {
		t.Fatalf("conflicting update was not atomic: before=%+v after=%+v", before, after)
	}

	if _, err = store.CreatePost(ctx, admin, NewPost{
		Status: "draft", OriginalPath: "originals/conflict.png", ThumbnailPath: "thumbs/conflict.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "conflict-post",
		Tags: []string{"character:sample_artist"},
	}); !errors.Is(err, ErrTagCategoryConflict) {
		t.Fatalf("create conflict error = %v, want ErrTagCategoryConflict", err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM posts WHERE sha256='conflict-post'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("conflicting create left a post behind")
	}
}

func TestTagSuggestionsRequireCurrentModerator(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	viewer, err := store.Register(ctx, "suggestion-viewer", "suggestion viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.TagSuggestions(ctx, viewer, "sample"); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer suggestion query error = %v, want ErrPermission", err)
	}
}

func TestPublicTagSuggestionsRespectVisibilityCategoryAndLimit(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		addPost(t, store, fmt.Sprintf("public-suggestion-%02d", i), "published", fmt.Sprintf("needle_%02d", i))
	}
	addPost(t, store, "public-suggestion-artist", "published", "needle_artist")
	if _, err := store.DB.Exec(`UPDATE tags SET category='artist' WHERE name='needle_artist'`); err != nil {
		t.Fatal(err)
	}
	addPost(t, store, "public-suggestion-draft", "draft", "secret_draft")
	addPost(t, store, "public-suggestion-quarantine", "published", "secret_quarantine")
	addPost(t, store, "public-suggestion-deleted", "published", "secret_deleted")
	if _, err := store.DB.Exec(`UPDATE posts SET quarantined_at=CURRENT_TIMESTAMP WHERE sha256='public-suggestion-quarantine'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`UPDATE posts SET deleted_at=CURRENT_TIMESTAMP WHERE sha256='public-suggestion-deleted'`); err != nil {
		t.Fatal(err)
	}

	tags, err := store.PublicTagSuggestions(ctx, "needle")
	if err != nil || len(tags) != tagSuggestionLimit {
		t.Fatalf("public suggestions returned %d tags, err=%v; want %d", len(tags), err, tagSuggestionLimit)
	}
	for _, tag := range tags {
		if strings.HasPrefix(tag.Name, "secret_") {
			t.Fatalf("non-public tag leaked from query: %+v", tag)
		}
	}
	categoryTags, err := store.PublicTagSuggestions(ctx, "-artist:needle")
	if err != nil || len(categoryTags) != 1 || categoryTags[0].Category != "artist" {
		t.Fatalf("negative category suggestion = %+v err=%v; want only artist tag", categoryTags, err)
	}
	hiddenTags, err := store.PublicTagSuggestions(ctx, "secret")
	if err != nil || len(hiddenTags) != 0 {
		t.Fatalf("non-public tags were returned: %+v err=%v", hiddenTags, err)
	}
	longTags, err := store.PublicTagSuggestions(ctx, strings.Repeat("a", maxSearchQueryLength+1))
	if err != nil || len(longTags) != 0 {
		t.Fatalf("overlong suggestion query returned %+v err=%v", longTags, err)
	}
}
