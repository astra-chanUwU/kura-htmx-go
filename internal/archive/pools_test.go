package archive

import (
	"context"
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
