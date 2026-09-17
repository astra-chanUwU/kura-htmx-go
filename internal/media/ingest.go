package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"kura/internal/archive"
)

const MaxUploadBytes = 32 << 20

var ErrDuplicate = errors.New("this image is already in Kura")

type Ingestor struct {
	Root       string
	Store      *archive.Store
	deletePost func(context.Context, archive.User, int64) (archive.Post, error)
}

type deleteManifest struct {
	PostID        int64  `json:"post_id"`
	OriginalPath  string `json:"original_path"`
	ThumbnailPath string `json:"thumbnail_path"`
}

func (i Ingestor) PermanentlyDelete(ctx context.Context, actor archive.User, id int64) error {
	post, err := i.Store.PostForDeletion(ctx, actor, id)
	if err != nil {
		return err
	}
	original, err := i.safePath(post.OriginalPath)
	if err != nil {
		return err
	}
	thumbnail, err := i.safePath(post.ThumbnailPath)
	if err != nil {
		return err
	}
	if original == thumbnail {
		return errors.New("original and thumbnail media paths must differ")
	}
	stageRoot := filepath.Join(i.Root, ".deleting")
	if err = os.MkdirAll(stageRoot, 0700); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(stageRoot, fmt.Sprintf("post-%d-", id))
	if err != nil {
		return err
	}
	_ = os.Chmod(stageDir, 0700)
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stageDir)
		}
	}()
	manifest := deleteManifest{PostID: id, OriginalPath: post.OriginalPath, ThumbnailPath: post.ThumbnailPath}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stageDir, "manifest.json"), manifestBytes, 0600); err != nil {
		return err
	}
	staged := []struct {
		source, name string
	}{
		{original, "original"},
		{thumbnail, "thumbnail"},
	}
	for _, item := range staged {
		if err = os.Rename(item.source, filepath.Join(stageDir, item.name)); err != nil {
			restoreErr := i.restoreStagedDelete(stageDir, manifest)
			if restoreErr != nil {
				keepStage = true
				return fmt.Errorf("media staging failed: %w; private media restore pending: %v", err, restoreErr)
			}
			return err
		}
	}
	deletePost := i.deletePost
	if deletePost == nil {
		deletePost = i.Store.PermanentDeletePost
	}
	if _, err = deletePost(ctx, actor, id); err != nil {
		restoreErr := i.restoreStagedDelete(stageDir, manifest)
		if restoreErr != nil {
			keepStage = true
			return fmt.Errorf("permanent deletion failed: %w; private media restore pending: %v", err, restoreErr)
		}
		return err
	}
	if err = os.RemoveAll(stageDir); err != nil {
		keepStage = true
		return fmt.Errorf("permanent deletion committed; private cleanup pending: %w", err)
	}
	return nil
}

func (i Ingestor) safePath(rel string) (string, error) {
	relPath := filepath.FromSlash(rel)
	if rel == "" || filepath.IsAbs(rel) || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", errors.New("media path is outside the configured media root")
	}
	root, err := filepath.Abs(i.Root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, relPath)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("media path is outside the configured media root")
	}
	return path, nil
}

func (i Ingestor) restoreStagedDelete(stageDir string, manifest deleteManifest) error {
	original, err := i.safePath(manifest.OriginalPath)
	if err != nil {
		return err
	}
	thumbnail, err := i.safePath(manifest.ThumbnailPath)
	if err != nil {
		return err
	}
	for _, item := range []struct{ name, destination string }{{"original", original}, {"thumbnail", thumbnail}} {
		source := filepath.Join(stageDir, item.name)
		if _, statErr := os.Stat(source); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			return statErr
		}
		if _, statErr := os.Stat(item.destination); statErr == nil {
			return fmt.Errorf("cannot restore staged media over existing path %q", item.destination)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err = os.MkdirAll(filepath.Dir(item.destination), 0755); err != nil {
			return err
		}
		if err = os.Rename(source, item.destination); err != nil {
			return err
		}
	}
	return os.RemoveAll(stageDir)
}

func (i Ingestor) ReconcileStagedDeletes(ctx context.Context) error {
	stageRoot := filepath.Join(i.Root, ".deleting")
	entries, err := os.ReadDir(stageRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stageDir := filepath.Join(stageRoot, entry.Name())
		body, readErr := os.ReadFile(filepath.Join(stageDir, "manifest.json"))
		if readErr != nil {
			return readErr
		}
		var manifest deleteManifest
		if readErr = json.Unmarshal(body, &manifest); readErr != nil || manifest.PostID < 1 {
			if readErr == nil {
				readErr = errors.New("invalid staged delete manifest")
			}
			return readErr
		}
		var count int
		if err = i.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM posts WHERE id=?`, manifest.PostID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if err = os.RemoveAll(stageDir); err != nil {
				return err
			}
			continue
		}
		if err = i.restoreStagedDelete(stageDir, manifest); err != nil {
			return err
		}
	}
	return nil
}

func (i Ingestor) Ingest(ctx context.Context, file multipart.File, header *multipart.FileHeader, actor archive.User, source, tags, status string) (archive.Post, error) {
	current, err := i.Store.User(ctx, actor.ID)
	if err != nil || !current.CanUpload() {
		return archive.Post{}, archive.ErrPermission
	}
	if err := os.MkdirAll(filepath.Join(i.Root, ".incoming"), 0700); err != nil {
		return archive.Post{}, err
	}
	temp, err := os.CreateTemp(filepath.Join(i.Root, ".incoming"), "upload-*")
	if err != nil {
		return archive.Post{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(file, MaxUploadBytes+1))
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return archive.Post{}, err
	}
	if written == 0 {
		return archive.Post{}, errors.New("the upload is empty")
	}
	if written > MaxUploadBytes {
		return archive.Post{}, fmt.Errorf("image exceeds the %d MB limit", MaxUploadBytes>>20)
	}
	hash := fmt.Sprintf("%x", hasher.Sum(nil))
	if id, err := i.Store.PostIDByHash(ctx, hash); err == nil {
		return archive.Post{}, fmt.Errorf("%w (post #%d)", ErrDuplicate, id)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return archive.Post{}, err
	}

	f, err := os.Open(tempName)
	if err != nil {
		return archive.Post{}, err
	}
	head := make([]byte, 512)
	n, readErr := io.ReadFull(f, head)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		f.Close()
		return archive.Post{}, readErr
	}
	mimeType := http.DetectContentType(head[:n])
	ext, ok := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif"}[mimeType]
	if !ok {
		f.Close()
		return archive.Post{}, errors.New("only JPEG, PNG, and GIF images are accepted")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return archive.Post{}, err
	}
	decoded, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return archive.Post{}, errors.New("the uploaded image could not be decoded")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 || int64(bounds.Dx())*int64(bounds.Dy()) > 100_000_000 {
		return archive.Post{}, errors.New("image dimensions are invalid or too large")
	}

	now := time.Now().UTC()
	dir := filepath.Join(now.Format("2006"), now.Format("01"))
	originalRel := filepath.Join("originals", dir, hash+ext)
	thumbRel := filepath.Join("thumbs", dir, hash+".jpg")
	if err = os.MkdirAll(filepath.Join(i.Root, "originals", dir), 0755); err != nil {
		return archive.Post{}, err
	}
	if err = os.MkdirAll(filepath.Join(i.Root, "thumbs", dir), 0755); err != nil {
		return archive.Post{}, err
	}
	thumbTemp, err := os.CreateTemp(filepath.Join(i.Root, ".incoming"), "thumb-*.jpg")
	if err != nil {
		return archive.Post{}, err
	}
	thumbTempName := thumbTemp.Name()
	defer os.Remove(thumbTempName)
	thumb := thumbnail(decoded, 420, 420)
	if err = jpeg.Encode(thumbTemp, thumb, &jpeg.Options{Quality: 86}); err != nil {
		thumbTemp.Close()
		return archive.Post{}, err
	}
	if err = thumbTemp.Sync(); err == nil {
		err = thumbTemp.Close()
	} else {
		thumbTemp.Close()
	}
	if err != nil {
		return archive.Post{}, err
	}
	if err = moveNoReplace(tempName, filepath.Join(i.Root, originalRel)); err != nil {
		return archive.Post{}, err
	}
	if err = moveNoReplace(thumbTempName, filepath.Join(i.Root, thumbRel)); err != nil {
		return archive.Post{}, err
	}
	post, err := i.Store.CreatePost(ctx, current, archive.NewPost{
		Status: status, OriginalPath: filepath.ToSlash(originalRel), ThumbnailPath: filepath.ToSlash(thumbRel),
		MIMEType: mimeType, Width: bounds.Dx(), Height: bounds.Dy(), ByteSize: written, SHA256: hash, Source: source, Tags: strings.Fields(tags),
		OriginalFilename: uploadBasename(header),
	})
	if err != nil {
		// Hash-named files are intentionally retained if metadata insertion fails;
		// an explicit maintenance command can reconcile unreferenced media safely.
		return archive.Post{}, err
	}
	_ = header
	return post, nil
}

func uploadBasename(header *multipart.FileHeader) string {
	if header == nil || header.Filename == "" {
		return ""
	}
	name := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "." || name == ".." {
		return ""
	}
	if runes := []rune(name); len(runes) > 255 {
		name = string(runes[:255])
	}
	return name
}

func moveNoReplace(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return ErrDuplicate
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}

func thumbnail(source image.Image, maxWidth, maxHeight int) image.Image {
	b := source.Bounds()
	width, height := b.Dx(), b.Dy()
	scale := min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	if scale > 1 {
		scale = 1
	}
	tw, th := max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))
	destination := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(destination, destination.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	// A compact nearest-neighbor sampler is sufficient for the archive grid and avoids a
	// second image dependency in the single-process server.
	for y := 0; y < th; y++ {
		sy := b.Min.Y + y*height/th
		for x := 0; x < tw; x++ {
			sx := b.Min.X + x*width/tw
			destination.Set(x, y, source.At(sx, sy))
		}
	}
	return destination
}
