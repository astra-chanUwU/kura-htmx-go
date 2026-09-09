package archive

import (
	"context"
	"database/sql"
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

func TestMigrateReplacesLegacyOwnerKeyFavorites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO schema_migrations(version) VALUES ('001_initial.sql');
		CREATE TABLE posts (id INTEGER PRIMARY KEY, status TEXT NOT NULL DEFAULT 'draft', original_path TEXT NOT NULL, thumbnail_path TEXT NOT NULL, mime_type TEXT NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, byte_size INTEGER NOT NULL, sha256 TEXT NOT NULL UNIQUE, source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, published_at TEXT);
		CREATE TABLE pools (id INTEGER PRIMARY KEY, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE favorites (post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE, owner_key TEXT NOT NULL DEFAULT 'local', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(post_id,owner_key));
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("legacy database did not migrate: %v", err)
	}
	defer store.Close()
	rows, err := store.DB.Query(`PRAGMA table_info(favorites)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if !columns["user_id"] || columns["owner_key"] {
		t.Fatalf("favorites columns after migration: %+v", columns)
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
