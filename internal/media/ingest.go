package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	Root  string
	Store *archive.Store
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
