package media

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kura/internal/archive"
)

func TestPermanentDeleteStagesBothFilesAndRestoresOnDatabaseFailure(t *testing.T) {
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.BootstrapSuperAdmin(context.Background(), "media-delete-root", "media delete root password")
	if err != nil {
		t.Fatal(err)
	}
	postID, err := insertMediaDeletePost(store, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	ingestor := Ingestor{Root: filepath.Join(root, "media"), Store: store}
	original := filepath.Join(ingestor.Root, "originals", "delete.png")
	thumbnail := filepath.Join(ingestor.Root, "thumbs", "delete.jpg")
	if err = os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(thumbnail), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}

	ingestor.deletePost = func(context.Context, archive.User, int64) (archive.Post, error) {
		return archive.Post{}, errors.New("simulated database failure")
	}
	if err = ingestor.PermanentlyDelete(context.Background(), admin, postID); err == nil {
		t.Fatal("simulated database failure was swallowed")
	}
	if _, err = os.Stat(original); err != nil {
		t.Fatalf("original was not restored after database failure: %v", err)
	}
	if _, err = os.Stat(thumbnail); err != nil {
		t.Fatalf("thumbnail was not restored after database failure: %v", err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM posts WHERE id=?`, postID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("database row was lost after rollback: count=%d err=%v", count, err)
	}
}

func TestPermanentDeleteRemovesDatabaseAndBothMediaFiles(t *testing.T) {
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.BootstrapSuperAdmin(context.Background(), "media-delete-owner", "media delete owner password")
	if err != nil {
		t.Fatal(err)
	}
	postID, err := insertMediaDeletePost(store, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	ingestor := Ingestor{Root: filepath.Join(root, "media"), Store: store}
	original := filepath.Join(ingestor.Root, "originals", "delete.png")
	thumbnail := filepath.Join(ingestor.Root, "thumbs", "delete.jpg")
	if err = os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(thumbnail), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(original, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}

	if err = ingestor.PermanentlyDelete(context.Background(), admin, postID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(original); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original remained after permanent deletion: %v", err)
	}
	if _, err = os.Stat(thumbnail); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thumbnail remained after permanent deletion: %v", err)
	}
	if _, err = store.Post(context.Background(), postID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("post metadata remained after permanent deletion: %v", err)
	}
}

func insertMediaDeletePost(store *archive.Store, uploaderID int64) (int64, error) {
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at,uploader_id) VALUES('published','originals/delete.png','thumbs/delete.jpg','image/png',10,10,8,'media-delete-sha','2026-09-17T00:00:00Z',?)`, uploaderID)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
