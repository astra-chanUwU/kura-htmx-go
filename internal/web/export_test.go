package web

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
)

type exportTestPost struct {
	ID       int64
	Bytes    []byte
	MIMEType string
	Path     string
	Filename string
	Hash     string
	ByteSize int64
	TagNames []string
	Category string
}

func insertWebExportPost(t *testing.T, store *archive.Store, status, mimeType, originalFilename string, bytesValue []byte, uploaderID *int64) exportTestPost {
	t.Helper()
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif"}[mimeType]
	hashBytes := sha256.Sum256(bytesValue)
	hash := hex.EncodeToString(hashBytes[:])
	rel := "originals/export/" + hash + ext
	var uploader any
	if uploaderID != nil {
		uploader = *uploaderID
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,original_filename,width,height,byte_size,sha256,source,published_at,uploader_id) VALUES(?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,?)`, status, rel, "thumbs/export/"+hash+".jpg", mimeType, originalFilename, 640, 480, len(bytesValue), hash, "https://example.test/source/"+hash, uploader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return exportTestPost{ID: id, Bytes: bytesValue, MIMEType: mimeType, Path: rel, Filename: originalFilename, Hash: hash, ByteSize: int64(len(bytesValue))}
}

func addWebExportTags(t *testing.T, store *archive.Store, postID int64, tags ...struct{ name, category string }) {
	t.Helper()
	for _, tag := range tags {
		if _, err := store.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?)`, tag.name, strings.ReplaceAll(tag.name, "_", " "), tag.category); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, postID, tag.name); err != nil {
			t.Fatal(err)
		}
	}
}

func writeWebExportMedia(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
}

type exportManifestFixture struct {
	SchemaVersion int `json:"schema_version"`
	Source        struct {
		Type   string `json:"type"`
		Slug   string `json:"slug"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"source"`
	Posts []struct {
		Filename string `json:"filename"`
		PostID   int64  `json:"post_id"`
		Tags     []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"tags"`
		Source           string `json:"source"`
		SHA256           string `json:"sha256"`
		OriginalBasename string `json:"original_basename"`
		MIMEType         string `json:"mime_type"`
		Width            int    `json:"width"`
		Height           int    `json:"height"`
		ByteSize         int64  `json:"byte_size"`
	} `json:"posts"`
}

func readExportZip(t *testing.T, body []byte) (map[string][]byte, []string) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response was not a valid ZIP: %v", err)
	}
	files := make(map[string][]byte, len(reader.File))
	order := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		if filepath.IsAbs(file.Name) || strings.Contains(file.Name, "..") || strings.ContainsAny(file.Name, "/\\") && file.Name != "manifest.json" {
			t.Fatalf("unsafe ZIP member name %q", file.Name)
		}
		contents, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(contents)
		contents.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
		order = append(order, file.Name)
	}
	return files, order
}

func TestSelectedExportProducesPortableZIPAndExactManifestMapping(t *testing.T) {
	server, store := testServer(t)
	first := insertWebExportPost(t, store, "published", "image/png", "../first.png", []byte("first-original"), nil)
	second := insertWebExportPost(t, store, "published", "image/gif", "camera/second.gif", []byte("GIF89a animated-original"), nil)
	addWebExportTags(t, store, second.ID, struct{ name, category string }{"blue", "general"}, struct{ name, category string }{"sample_artist", "artist"})
	writeWebExportMedia(t, server.mediaRoot, first.Path, first.Bytes)
	writeWebExportMedia(t, server.mediaRoot, second.Path, second.Bytes)

	path := "/posts/export?name=tagged&post_ids=" + strconv.FormatInt(second.ID, 10) + "&post_ids=" + strconv.FormatInt(first.ID, 10) + "&post_ids=" + strconv.FormatInt(second.ID, 10)
	response := mediaRequest(t, server.Handler(), http.MethodGet, path, nil, nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("selected export status=%d type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	files, _ := readExportZip(t, response.Body.Bytes())
	var manifest exportManifestFixture
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.Source.Type != "selected" || len(manifest.Posts) != 2 {
		t.Fatalf("unexpected manifest header: %+v", manifest)
	}
	if len(files) != len(manifest.Posts)+1 {
		t.Fatalf("ZIP has members not represented by the manifest: %v", files)
	}
	if manifest.Posts[0].PostID != second.ID || manifest.Posts[1].PostID != first.ID {
		t.Fatalf("selection order was not preserved: %+v", manifest.Posts)
	}
	for _, record := range manifest.Posts {
		data, ok := files[record.Filename]
		if !ok {
			t.Fatalf("manifest references missing ZIP member %q", record.Filename)
		}
		hash := sha256.Sum256(data)
		if record.SHA256 != hex.EncodeToString(hash[:]) || record.ByteSize != int64(len(data)) || record.MIMEType == "" || record.Width != 640 || record.Height != 480 {
			t.Fatalf("manifest does not describe original bytes: %+v", record)
		}
	}
	if _, ok := files[fmt.Sprintf("kura-%d__sample_artist blue.gif", second.ID)]; !ok {
		t.Fatalf("tagged filename omitted canonical names: %v", files)
	}
	if strings.Contains(response.Header().Get("Content-Disposition"), "/") {
		t.Fatalf("unsafe response filename: %q", response.Header().Get("Content-Disposition"))
	}
}

func TestPoolExportPreservesOrderAndOriginalBasenameMode(t *testing.T) {
	server, store := testServer(t)
	owner, err := store.Register(context.Background(), "web-export-owner", "web export owner password")
	if err != nil {
		t.Fatal(err)
	}
	first := insertWebExportPost(t, store, "published", "image/png", "../../first.png", []byte("pool-first"), nil)
	second := insertWebExportPost(t, store, "published", "image/jpeg", "camera/second.jpeg", []byte("pool-second"), nil)
	writeWebExportMedia(t, server.mediaRoot, first.Path, first.Bytes)
	writeWebExportMedia(t, server.mediaRoot, second.Path, second.Bytes)
	pool, err := store.CreatePool(context.Background(), owner, "My Export Pool", "portable set", "published", fmt.Sprintf("%d %d", second.ID, first.ID))
	if err != nil {
		t.Fatal(err)
	}

	response := mediaRequest(t, server.Handler(), http.MethodGet, "/pools/"+pool.Slug+"/export?name=original", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("pool export status=%d body=%s", response.Code, response.Body.String())
	}
	files, order := readExportZip(t, response.Body.Bytes())
	if len(order) != 3 || order[0] != "manifest.json" || order[1] != fmt.Sprintf("kura-%d__second.jpeg", second.ID) || order[2] != fmt.Sprintf("kura-%d__first.png", first.ID) {
		t.Fatalf("pool order or names were not preserved: %v", order)
	}
	var manifest exportManifestFixture
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Source.Type != "pool" || manifest.Source.Slug != pool.Slug || manifest.Source.Name != pool.Name || manifest.Source.Status != "published" || manifest.Posts[0].PostID != second.ID || manifest.Posts[1].PostID != first.ID {
		t.Fatalf("pool metadata/order missing from manifest: %+v", manifest)
	}
}

func TestExportRejectsMissingOrChangedOriginalBeforeSendingZIP(t *testing.T) {
	server, store := testServer(t)
	post := insertWebExportPost(t, store, "published", "image/png", "missing.png", []byte("expected"), nil)
	response := mediaRequest(t, server.Handler(), http.MethodGet, "/posts/export?post_ids="+strconv.FormatInt(post.ID, 10), nil, nil)
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("missing original produced a successful-looking export: status=%d type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	writeWebExportMedia(t, server.mediaRoot, post.Path, []byte("changed"))
	response = mediaRequest(t, server.Handler(), http.MethodGet, "/posts/export?post_ids="+strconv.FormatInt(post.ID, 10), nil, nil)
	if response.Code != http.StatusConflict || response.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("changed original produced a successful-looking export: status=%d type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestExportUsesExistingDraftAndPoolVisibilityBoundaries(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	owner, err := store.Register(ctx, "boundary-export-owner", "boundary export owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "boundary-export-other", "boundary export other password")
	if err != nil {
		t.Fatal(err)
	}
	draft := insertWebExportPost(t, store, "draft", "image/gif", "private.gif", []byte("private-gif"), &owner.ID)
	writeWebExportMedia(t, server.mediaRoot, draft.Path, draft.Bytes)
	handler := server.Handler()
	path := "/posts/export?post_ids=" + strconv.FormatInt(draft.ID, 10)
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("anonymous draft export status=%d", response.Code)
	}
	otherSession, err := store.NewSession(ctx, &other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, &otherSession); response.Code != http.StatusNotFound {
		t.Fatalf("other viewer draft export status=%d", response.Code)
	}
	ownerSession, err := store.NewSession(ctx, &owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, &ownerSession); response.Code != http.StatusOK {
		t.Fatalf("owner draft export status=%d body=%s", response.Code, response.Body.String())
	}

	pool, err := store.CreatePool(ctx, owner, "Private Export Pool", "", "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	if response := mediaRequest(t, handler, http.MethodGet, "/pools/"+pool.Slug+"/export", nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("anonymous draft pool export status=%d", response.Code)
	}
	if response := mediaRequest(t, handler, http.MethodGet, "/pools/"+pool.Slug+"/export", nil, &otherSession); response.Code != http.StatusNotFound {
		t.Fatalf("other viewer draft pool export status=%d", response.Code)
	}
}

func TestExportMemberFilenameHasBoundedSafeLength(t *testing.T) {
	tags := make([]archive.Tag, 0, 8)
	for i := 0; i < 8; i++ {
		tags = append(tags, archive.Tag{Name: strings.Repeat("long_tag_", 24)})
	}
	name := exportMemberFilename(archive.Post{ID: 42, MIMEType: "image/png", Tags: tags}, "tagged")
	if len([]rune(name)) > 220 || !strings.HasPrefix(name, "kura-42__") || !strings.HasSuffix(name, ".png") || strings.ContainsAny(name, "/\\") {
		t.Fatalf("export member filename is not bounded and safe: %q (%d runes)", name, len([]rune(name)))
	}
}

func TestBrowseShowsExportSelectionToAnonymousWithoutEditorControls(t *testing.T) {
	server, store := testServer(t)
	post := insertWebExportPost(t, store, "published", "image/png", "browse.png", []byte("browse"), nil)
	writeWebExportMedia(t, server.mediaRoot, post.Path, post.Bytes)
	response := mediaRequest(t, server.Handler(), http.MethodGet, "/posts", nil, nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `data-selection-toolbar`) || !strings.Contains(body, `data-export-post="`+strconv.FormatInt(post.ID, 10)+`"`) || !strings.Contains(body, "Tagged ZIP") || strings.Contains(body, "bulk-tag-toolbar") || strings.Contains(body, "data-bulk-post") {
		t.Fatalf("anonymous browse export/editor controls are wrong: status=%d body=%s", response.Code, body)
	}
}
