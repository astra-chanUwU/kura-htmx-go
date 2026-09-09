package main

import (
	"context"
	"path/filepath"
	"testing"

	demomedia "kura/demo"
	"kura/internal/archive"
)

func TestSeedDemoIncludesAllEmbeddedImageFormats(t *testing.T) {
	entries, err := demomedia.Files.ReadDir("images")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 60 {
		t.Fatalf("embedded image count = %d, want 60", len(entries))
	}

	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := seedDemo(context.Background(), store, filepath.Join(root, "media")); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 48 {
		t.Fatalf("seeded unique post count = %d, want 48", count)
	}

	for _, want := range []string{"image/png", "image/gif"} {
		var found int
		if err := store.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE mime_type=?`, want).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found == 0 {
			t.Fatalf("seeded posts contain no %s entries", want)
		}
	}
}
