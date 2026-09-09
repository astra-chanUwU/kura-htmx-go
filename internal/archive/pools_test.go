package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestPoolMembershipRequiresOwnershipAndPreservesViewerOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner, _ := s.Register(ctx, "pool-owner", "pool owner password")
	other, _ := s.Register(ctx, "pool-other", "pool other password")
	addPost(t, s, "first", "published")
	addPost(t, s, "second", "published")
	addPost(t, s, "third", "published")

	posts, err := s.ListPosts(ctx, "", 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := posts.Posts[2].ID, posts.Posts[1].ID, posts.Posts[0].ID
	pool, err := s.CreatePool(ctx, owner.ID, "Visual Set", "", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddPostToPool(ctx, other.ID, pool.Slug, first); !errors.Is(err, ErrPermission) {
		t.Fatalf("another viewer added to the pool: %v", err)
	}
	if err = s.AddPostToPool(ctx, owner.ID, pool.Slug, first); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePool(ctx, owner.ID, pool.Slug, pool.Name, pool.Description, pool.Status, fmt.Sprintf("%d %d %d", third, first, second)); err != nil {
		t.Fatal(err)
	}

	updated, err := s.Pool(ctx, pool.Slug, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Posts) != 3 || updated.Posts[0].ID != third || updated.Posts[1].ID != first || updated.Posts[2].ID != second {
		t.Fatalf("pool order was not preserved: %+v", updated.Posts)
	}
	owned, err := s.OwnedPoolsForPost(ctx, owner.ID, first)
	if err != nil || len(owned) != 1 || !owned[0].ContainsPost {
		t.Fatalf("owned pool membership missing: %+v err=%v", owned, err)
	}
}

func TestPoolCandidatesScopeFavoritesAndPoolVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	viewer, _ := s.Register(ctx, "candidate-viewer", "candidate viewer password")
	other, _ := s.Register(ctx, "candidate-other", "candidate other password")
	addPost(t, s, "viewer-favorite", "published", "blue")
	addPost(t, s, "other-favorite", "published", "blue")
	addPost(t, s, "draft-post", "draft", "blue")
	posts, _ := s.ListPosts(ctx, "", 1, 24)
	viewerFavorite := posts.Posts[1].ID
	otherFavorite := posts.Posts[0].ID
	_, err := s.DB.Exec(`INSERT INTO favorites(post_id,user_id) VALUES(?,?),(?,?)`, viewerFavorite, viewer.ID, otherFavorite, other.ID)
	if err != nil {
		t.Fatal(err)
	}

	favorites, err := s.PoolCandidates(ctx, PoolCandidateFilter{Source: "favorites", ViewerID: viewer.ID})
	if err != nil || len(favorites.Posts) != 1 || favorites.Posts[0].ID != viewerFavorite {
		t.Fatalf("favorites were not scoped to the viewer: %+v err=%v", favorites, err)
	}

	published, err := s.CreatePool(ctx, other.ID, "Published source", "", "published", fmt.Sprintf("%d", viewerFavorite))
	if err != nil {
		t.Fatal(err)
	}
	visible, err := s.PoolCandidates(ctx, PoolCandidateFilter{Source: "pool", PoolSlug: published.Slug, ViewerID: viewer.ID})
	if err != nil || len(visible.Posts) != 1 || visible.Posts[0].ID != viewerFavorite {
		t.Fatalf("published pool was not available: %+v err=%v", visible, err)
	}

	ownDraft, err := s.CreatePool(ctx, viewer.ID, "Own draft source", "", "draft", fmt.Sprintf("%d", viewerFavorite))
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.PoolCandidates(ctx, PoolCandidateFilter{Source: "pool", PoolSlug: ownDraft.Slug, ViewerID: viewer.ID})
	if err != nil || len(owned.Posts) != 1 || owned.Posts[0].ID != viewerFavorite {
		t.Fatalf("own draft pool was not available: %+v err=%v", owned, err)
	}

	otherDraft, err := s.CreatePool(ctx, other.ID, "Other draft source", "", "draft", fmt.Sprintf("%d", viewerFavorite))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PoolCandidates(ctx, PoolCandidateFilter{Source: "pool", PoolSlug: otherDraft.Slug, ViewerID: viewer.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other user's draft should be unavailable, got %v", err)
	}
}

func TestPoolCandidatesSearchesWithinSelectedSourceInOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	viewer, _ := s.Register(ctx, "candidate-search", "candidate search password")
	addPost(t, s, "first", "published", "blue")
	addPost(t, s, "second", "published", "red")
	addPost(t, s, "third", "published", "blue")
	posts, _ := s.ListPosts(ctx, "", 1, 24)
	pool, err := s.CreatePool(ctx, viewer.ID, "Ordered source", "", "published", fmt.Sprintf("%d %d %d", posts.Posts[0].ID, posts.Posts[1].ID, posts.Posts[2].ID))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.PoolCandidates(ctx, PoolCandidateFilter{Source: "pool", PoolSlug: pool.Slug, ViewerID: viewer.ID, Query: "blue"})
	if err != nil || len(page.Posts) != 2 || page.Posts[0].SHA256 != "third" || page.Posts[1].SHA256 != "first" {
		t.Fatalf("source search did not preserve source order: %+v err=%v", page, err)
	}
}
