package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kura/internal/archive"
	mediafiles "kura/internal/media"
)

type batchUploadFile struct {
	name string
	body []byte
}

func batchPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 9), G: uint8(y * 11), B: 180, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestBatchUploadCreatesIndependentResultsAndCleansInvalidMedia(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "batch-admin", "batch admin password long")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	valid := batchPNG(t, 12, 8)
	first := batchRequestWithHandler(t, server.Handler(), session, []batchUploadFile{{"first.png", valid}}, url.Values{"status": {"published"}})
	if first.Code != http.StatusSeeOther {
		t.Fatalf("seed upload status=%d body=%s", first.Code, first.Body.String())
	}

	files := []batchUploadFile{{"created.png", batchPNG(t, 14, 9)}, {"duplicate.png", valid}, {"invalid.txt", []byte("not an image")}}
	response := batchRequestWithHandler(t, server.Handler(), session, files, url.Values{"source": {"https://example.test/batch"}, "tags": {"batch-tag"}})
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Batch upload results") {
		t.Fatalf("batch response status/body=%d/%s", response.Code, body)
	}
	for _, want := range []string{"created.png", "created", "duplicate.png", "duplicate", "invalid.txt", "validation failure", "Draft"} {
		if !strings.Contains(body, want) {
			t.Fatalf("batch summary missing %q: %s", want, body)
		}
	}
	var posts int
	if err = store.DB.QueryRow("SELECT count(*) FROM posts").Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatalf("batch created %d posts, want seed plus one success", posts)
	}
	var draftCount int
	if err = store.DB.QueryRow("SELECT count(*) FROM posts WHERE status='draft'").Scan(&draftCount); err != nil {
		t.Fatal(err)
	}
	if draftCount != 1 {
		t.Fatalf("batch default status created %d drafts, want 1", draftCount)
	}
	var source string
	if err = store.DB.QueryRow("SELECT source FROM posts WHERE original_filename='created.png'").Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "https://example.test/batch" {
		t.Fatalf("batch source=%q, want shared source", source)
	}
	var sharedTagCount int
	if err = store.DB.QueryRow(`SELECT count(*) FROM post_tags pt JOIN tags t ON t.id=pt.tag_id JOIN posts p ON p.id=pt.post_id WHERE p.original_filename='created.png' AND t.name='batch_tag'`).Scan(&sharedTagCount); err != nil {
		t.Fatal(err)
	}
	if sharedTagCount != 1 {
		t.Fatalf("shared batch tag count=%d, want 1", sharedTagCount)
	}
	assertIncomingEmpty(t, server.mediaRoot)
}

func TestBatchUploadCanExplicitlyPublishAllFiles(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "batch-publish-admin", "batch publish admin password long")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	files := []batchUploadFile{{"published-a.png", batchPNG(t, 3, 3)}, {"published-b.png", batchPNG(t, 4, 4)}}
	response := batchRequestWithHandler(t, server.Handler(), session, files, url.Values{"status": {"draft"}, "publish_batch": {"1"}})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Batch upload results") {
		t.Fatalf("publish batch response status/body=%d/%s", response.Code, response.Body.String())
	}
	var drafts, published int
	if err = store.DB.QueryRow("SELECT count(*) FROM posts WHERE status='draft'").Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if err = store.DB.QueryRow("SELECT count(*) FROM posts WHERE status='published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if drafts != 0 || published != len(files) {
		t.Fatalf("explicit publish statuses draft=%d published=%d", drafts, published)
	}
}

func TestBatchUploadRejectsMoreThanTwentyBeforeIngestion(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "batch-limit-admin", "batch limit admin password long")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]batchUploadFile, mediafiles.MaxUploadCount+1)
	for i := range files {
		files[i] = batchUploadFile{strconv.Itoa(i) + ".png", batchPNG(t, 2, 2)}
	}
	response := batchRequestWithHandler(t, server.Handler(), session, files, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "at most") {
		t.Fatalf("max-count response status/body=%d/%s", response.Code, response.Body.String())
	}
	var posts int
	if err = store.DB.QueryRow("SELECT count(*) FROM posts").Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("max-count rejection ingested %d files", posts)
	}
}

func TestBatchUploadRequiresCurrentUploadRoleAndCSRF(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	viewer, err := store.Register(ctx, "batch-viewer", "batch viewer password long")
	if err != nil {
		t.Fatal(err)
	}
	viewerSession, err := store.NewSession(ctx, &viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	files := []batchUploadFile{{"denied.png", batchPNG(t, 2, 2)}}
	withoutCSRF := batchRequestWithHandler(t, server.Handler(), viewerSession, files, url.Values{"csrf": {"wrong"}})
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing/invalid csrf status=%d body=%s", withoutCSRF.Code, withoutCSRF.Body.String())
	}
	denied := batchRequestWithHandler(t, server.Handler(), viewerSession, files, url.Values{"csrf": {viewerSession.CSRF}})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer batch status=%d body=%s", denied.Code, denied.Body.String())
	}
	var posts int
	if err = store.DB.QueryRow("SELECT count(*) FROM posts").Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("unauthorized batch created %d posts", posts)
	}
}

func TestSingleUploadStillRedirectsAndPublishes(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "single-upload-admin", "single upload admin password long")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.NewSession(ctx, &admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := batchRequestWithHandler(t, server.Handler(), session, []batchUploadFile{{"single.png", batchPNG(t, 8, 6)}}, nil)
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/posts/") {
		t.Fatalf("single upload response status/location=%d/%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var status string
	if err = store.DB.QueryRow("SELECT status FROM posts").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "published" {
		t.Fatalf("single upload status=%q, want published", status)
	}
}

func batchRequestWithHandler(t *testing.T, handler http.Handler, session archive.Session, files []batchUploadFile, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fields == nil || len(fields["csrf"]) == 0 {
		if err := writer.WriteField("csrf", session.CSRF); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		part, err := writer.CreateFormFile("image", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/uploads", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertIncomingEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".incoming"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary upload files remain: %v", entries)
	}
}
