package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestIndividualDownloadUsesTaggedNameAndPreservesOriginalBytes(t *testing.T) {
	server, store := testServer(t)
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,published_at) VALUES('published','originals/2026/09/download.png','thumbs/2026/09/download.jpg','image/png',1,1,14,'download-post',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []struct {
		name, category string
	}{{"room", "general"}, {"black_hair", "artist"}} {
		if _, err = store.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES(?,?,?)`, tag.name, strings.ReplaceAll(tag.name, "_", " "), tag.category); err != nil {
			t.Fatal(err)
		}
		if _, err = store.DB.Exec(`INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, postID, tag.name); err != nil {
			t.Fatal(err)
		}
	}
	writeMediaFile(t, server.mediaRoot, "originals/2026/09/download.png", "original-bytes")

	path := "/posts/" + strconv.FormatInt(postID, 10) + "/download?name=tagged"
	response := mediaRequest(t, server.Handler(), http.MethodGet, path, nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.String() != "original-bytes" {
		t.Fatalf("download changed original bytes: %q", response.Body.String())
	}
	contentDisposition := response.Header().Get("Content-Disposition")
	wantName := `kura-` + strconv.FormatInt(postID, 10) + `__black_hair room.png`
	if !strings.Contains(contentDisposition, wantName) {
		t.Fatalf("Content-Disposition=%q, want tagged name %q", contentDisposition, wantName)
	}
	response = mediaRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10)+"/download?name=original", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "original-bytes" {
		t.Fatalf("legacy original download status=%d body=%q", response.Code, response.Body.String())
	}
	wantFallback := `kura-` + strconv.FormatInt(postID, 10) + `__image.png`
	if !strings.Contains(response.Header().Get("Content-Disposition"), wantFallback) {
		t.Fatalf("legacy Content-Disposition=%q, want fallback name %q", response.Header().Get("Content-Disposition"), wantFallback)
	}
	page := mediaRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10), nil, nil)
	for _, want := range []string{"Tagged filename", "Original filename"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("post page missing download action %q: %s", want, page.Body.String())
		}
	}
}

func TestIndividualDownloadUsesReviewedBasenameAndMatchingExtension(t *testing.T) {
	server, store := testServer(t)
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,original_filename,width,height,byte_size,sha256,published_at) VALUES('published','originals/2026/09/original.jpeg','thumbs/2026/09/original.jpg','image/jpeg','../../yuri-camera-room-dorm.jpeg',1,1,12,'original-name-post',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	writeMediaFile(t, server.mediaRoot, "originals/2026/09/original.jpeg", "jpeg-bytes")

	response := mediaRequest(t, server.Handler(), http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10)+"/download?name=original", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "jpeg-bytes" {
		t.Fatalf("original download status=%d body=%q", response.Code, response.Body.String())
	}
	contentDisposition := response.Header().Get("Content-Disposition")
	wantName := `kura-` + strconv.FormatInt(postID, 10) + `__yuri-camera-room-dorm.jpeg`
	if !strings.Contains(contentDisposition, wantName) || strings.Contains(contentDisposition, "/") || strings.Contains(contentDisposition, `..`) {
		t.Fatalf("unsafe original download name: %q", contentDisposition)
	}
}

func TestIndividualDownloadPreservesPrivateDraftVisibility(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	owner, err := store.Register(ctx, "download-owner", "download owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "download-other", "download other password")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,uploader_id) VALUES('draft','originals/2026/09/private.png','thumbs/2026/09/private.jpg','image/png',1,1,12,'private-download-post',?)`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	writeMediaFile(t, server.mediaRoot, "originals/2026/09/private.png", "private-bytes")
	handler := server.Handler()
	path := "/posts/" + strconv.FormatInt(postID, 10) + "/download"
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("anonymous draft download status=%d", response.Code)
	}
	otherSession, err := store.NewSession(ctx, &other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, &otherSession); response.Code != http.StatusNotFound {
		t.Fatalf("other viewer draft download status=%d", response.Code)
	}
	ownerSession, err := store.NewSession(ctx, &owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response := mediaRequest(t, handler, http.MethodGet, path, nil, &ownerSession); response.Code != http.StatusOK || response.Body.String() != "private-bytes" {
		t.Fatalf("owner draft download status=%d body=%q", response.Code, response.Body.String())
	}
}
