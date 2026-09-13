package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kura/internal/archive"
)

func uploadFile(t *testing.T, body []byte) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "image-*.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(body); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestIngestWritesRelativeHashNamedFilesAndRejectsDuplicates(t *testing.T) {
	root := t.TempDir()
	store, err := archive.Open(filepath.Join(root, "kura.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.Register(context.Background(), "moderator", "moderator password long")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.BootstrapSuperAdmin(context.Background(), "media-root", "media root password long")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(context.Background(), admin, user.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	user, err = store.User(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 12; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 20), B: 180, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	ingestor := Ingestor{Root: filepath.Join(root, "media"), Store: store}
	post, err := ingestor.Ingest(context.Background(), uploadFile(t, encoded.Bytes()), &multipart.FileHeader{Filename: "/private/camera/yuri-camera-room-dorm.jpeg"}, user, "", "artist:Sample_Artist blue", "published")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(post.OriginalPath) || !strings.HasPrefix(post.OriginalPath, "originals/") || !strings.Contains(post.OriginalPath, post.SHA256) {
		t.Fatalf("unsafe or non-hash original path: %q", post.OriginalPath)
	}
	var originalFilename string
	if err = store.DB.QueryRow(`SELECT original_filename FROM posts WHERE id=?`, post.ID).Scan(&originalFilename); err != nil {
		t.Fatal(err)
	}
	if originalFilename != "yuri-camera-room-dorm.jpeg" {
		t.Fatalf("original filename=%q, want basename only", originalFilename)
	}
	stored, err := store.Post(context.Background(), post.ID)
	if err != nil || len(stored.Tags) != 2 || stored.Tags[0].Category != "artist" {
		t.Fatalf("categorized upload tags = %+v, err=%v", stored.Tags, err)
	}
	for _, rel := range []string{post.OriginalPath, post.ThumbnailPath} {
		if info, statErr := os.Stat(filepath.Join(ingestor.Root, filepath.FromSlash(rel))); statErr != nil || info.Size() == 0 {
			t.Fatalf("media missing for %q: %v", rel, statErr)
		}
	}
	_, err = ingestor.Ingest(context.Background(), uploadFile(t, encoded.Bytes()), nil, user, "", "", "draft")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate upload was not rejected: %v", err)
	}
	if err = store.SoftDeletePost(context.Background(), user, post.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(ingestor.Root, filepath.FromSlash(post.OriginalPath))); err != nil {
		t.Fatalf("soft deletion removed original media: %v", err)
	}
}
