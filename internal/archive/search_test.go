package archive

import (
	"context"
	"errors"
	"testing"
)

func TestParseSearchQueryNormalizesCategoriesAndExclusions(t *testing.T) {
	query, err := ParseSearchQuery("  artist:Sample-Artist blue -spoiler -character:Villain blue ")
	if err != nil {
		t.Fatal(err)
	}
	if got := query.String(); got != "artist:sample_artist blue -spoiler -character:villain" {
		t.Fatalf("normalized search query = %q", got)
	}
	if len(query.Include) != 2 || len(query.Exclude) != 2 {
		t.Fatalf("parsed search query = %+v", query)
	}
}

func TestParseSearchQueryRejectsMalformedAndContradictoryTerms(t *testing.T) {
	for _, raw := range []string{"-", "artist:", "unknown:tag", "cat -cat", "artist:cat character:cat"} {
		if _, err := ParseSearchQuery(raw); !errors.Is(err, ErrInvalidSearchQuery) {
			t.Errorf("ParseSearchQuery(%q) error = %v, want ErrInvalidSearchQuery", raw, err)
		}
	}
}

func TestListPostsTreatsSearchInputAsBoundedValues(t *testing.T) {
	s := testStore(t)
	addPost(t, s, "safe", "published", "needle")
	page, err := s.ListPosts(context.Background(), "needle' OR 1=1", 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("injection-like search broadened results: %+v", page)
	}
}

func TestListPostsRejectsUnsupportedSort(t *testing.T) {
	s := testStore(t)
	if _, err := s.ListPostsSorted(context.Background(), "", "random", 1, 24); !errors.Is(err, ErrInvalidSearchQuery) {
		t.Fatalf("unsupported sort error = %v, want ErrInvalidSearchQuery", err)
	}
}

func TestListPostsSupportsCategoriesExclusionsAndSort(t *testing.T) {
	s := testStore(t)
	insertSearchPost(t, s, "new", "2026-09-17T03:00:00Z", searchTag{name: "sample_artist", category: "artist"}, searchTag{name: "blue", category: "general"})
	insertSearchPost(t, s, "old", "2026-09-16T03:00:00Z", searchTag{name: "sample_artist", category: "artist"}, searchTag{name: "blue", category: "general"}, searchTag{name: "spoiler", category: "general"})
	insertSearchPost(t, s, "wrong-category", "2026-09-18T03:00:00Z", searchTag{name: "wrong_category", category: "character"}, searchTag{name: "blue", category: "general"})

	page, err := s.ListPostsSorted(context.Background(), "artist:sample-artist blue -spoiler", SearchSortNewest, 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Posts) != 1 || page.Posts[0].SHA256 != "new" {
		t.Fatalf("newest category/exclusion results = %+v", page)
	}

	page, err = s.ListPostsSorted(context.Background(), "artist:sample_artist blue", SearchSortOldest, 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Posts) != 2 || page.Posts[0].SHA256 != "old" || page.Posts[1].SHA256 != "new" {
		t.Fatalf("oldest results = %+v", page)
	}
	page, err = s.ListPostsSorted(context.Background(), "artist:wrong_category", SearchSortNewest, 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("category mismatch should not match: %+v", page)
	}
}

func TestPostNeighborsPreserveExcludedSearchAndOldestSort(t *testing.T) {
	s := testStore(t)
	old := insertSearchPost(t, s, "neighbor-old", "2026-09-15T03:00:00Z", searchTag{name: "needle", category: "general"})
	current := insertSearchPost(t, s, "neighbor-current", "2026-09-16T03:00:00Z", searchTag{name: "needle", category: "general"})
	newer := insertSearchPost(t, s, "neighbor-new", "2026-09-17T03:00:00Z", searchTag{name: "needle", category: "general"})
	insertSearchPost(t, s, "neighbor-excluded", "2026-09-14T03:00:00Z", searchTag{name: "needle", category: "general"}, searchTag{name: "spoiler", category: "general"})

	navigation, err := s.PostNeighbors(context.Background(), current, 0, PostNavigationContext{Source: "browse", Query: "needle -spoiler", Sort: SearchSortOldest})
	if err != nil {
		t.Fatal(err)
	}
	if navigation.PreviousID != old || navigation.NextID != newer {
		t.Fatalf("oldest neighbors = %+v, want previous=%d next=%d", navigation, old, newer)
	}
}

type searchTag struct {
	name, category string
}

func insertSearchPost(t *testing.T, s *Store, hash, publishedAt string, tags ...searchTag) int64 {
	t.Helper()
	result, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published',?,?,?,?,?,?,?,?)`, hash+".png", hash+"-t.png", "image/png", 10, 10, 50, hash, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if _, err = s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, tag.name, tag.name, tag.category); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, id, tag.name); err != nil {
			t.Fatal(err)
		}
	}
	return id
}
