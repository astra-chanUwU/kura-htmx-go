package archive

import (
	"context"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func addPost(t *testing.T, s *Store, hash, status string, tags ...string) {
	t.Helper()
	r, err := s.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)`, status, hash+".png", hash+"-t.png", "image/png", 10, 10, 50, hash)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	for _, name := range tags {
		if _, err = s.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, name, name, "general"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, id, name); err != nil {
			t.Fatal(err)
		}
	}
}
func TestListPostsUsesANDTagSemanticsAndHidesDrafts(t *testing.T) {
	s := testStore(t)
	addPost(t, s, "both", "published", "red", "sky")
	addPost(t, s, "one", "published", "red")
	addPost(t, s, "draft", "draft", "red", "sky")
	page, err := s.ListPosts(context.Background(), "red sky", 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Posts) != 1 || page.Posts[0].SHA256 != "both" {
		t.Fatalf("unexpected results: %+v", page)
	}
}
func TestListPostsPaginates(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 5; i++ {
		addPost(t, s, string(rune('a'+i)), "published")
	}
	page, err := s.ListPosts(context.Background(), "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 || page.Pages != 3 || page.Page != 2 || len(page.Posts) != 2 {
		t.Fatalf("unexpected page: %+v", page)
	}
}
