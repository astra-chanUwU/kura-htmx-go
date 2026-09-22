package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestTagSuggestionsAreEditorOnlyBoundedAndCategoryAware(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "suggest-admin", "suggest admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "suggest-viewer", "suggest viewer password")
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := store.Register(ctx, "suggest-moderator", "suggest moderator password")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetUserRole(ctx, admin, moderator.ID, "moderator"); err != nil {
		t.Fatal(err)
	}
	moderator, err = store.User(ctx, moderator.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		_, err = store.CreatePost(ctx, admin, archive.NewPost{
			Status: "draft", OriginalPath: fmt.Sprintf("originals/suggest-%02d.png", i), ThumbnailPath: fmt.Sprintf("thumbs/suggest-%02d.jpg", i),
			MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: fmt.Sprintf("suggest-%02d", i), Tags: []string{fmt.Sprintf("meta:needle_%02d", i)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.CreatePost(ctx, admin, archive.NewPost{
		Status: "draft", OriginalPath: "originals/hidden.png", ThumbnailPath: "thumbs/hidden.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "hidden-suggestion", Tags: []string{"character:hidden_only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreatePost(ctx, admin, archive.NewPost{
		Status: "draft", OriginalPath: "originals/artist.png", ThumbnailPath: "thumbs/artist.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "artist-suggestion", Tags: []string{"artist:needle_artist"},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := server.Handler()
	viewerSession, _ := store.NewSession(ctx, &viewer.ID)
	denied := sessionRequest(t, handler, http.MethodGet, "/tags/suggest?q=hidden", nil, viewerSession)
	if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "hidden_only") {
		t.Fatalf("viewer suggestion access leaked or was allowed: status=%d body=%s", denied.Code, denied.Body.String())
	}

	moderatorSession, _ := store.NewSession(ctx, &moderator.ID)
	allowed := sessionRequest(t, handler, http.MethodGet, "/tags/suggest?q=needle", nil, moderatorSession)
	body := allowed.Body.String()
	if allowed.Code != http.StatusOK || allowed.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(body, "needle_00") || !strings.Contains(body, "meta") {
		t.Fatalf("moderator suggestions missing expected category-aware result: status=%d body=%s", allowed.Code, body)
	}
	if got := strings.Count(body, `role="option"`); got > 8 {
		t.Fatalf("suggestion result count=%d, want at most 8", got)
	}
	category := sessionRequest(t, handler, http.MethodGet, "/tags/suggest?q=artist:needle", nil, moderatorSession)
	if category.Code != http.StatusOK || !strings.Contains(category.Body.String(), "needle_artist") || strings.Contains(category.Body.String(), "needle_00") {
		t.Fatalf("category-prefixed suggestions were not scoped: status=%d body=%s", category.Code, category.Body.String())
	}
}

func TestPublicTagSuggestionsOnlyExposePublishedVisibleTags(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "public-suggest-admin", "public suggest admin password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Register(ctx, "public-suggest-viewer", "public suggest viewer password")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, err = store.CreatePost(ctx, admin, archive.NewPost{
			Status: "published", OriginalPath: fmt.Sprintf("originals/public-suggest-%02d.png", i), ThumbnailPath: fmt.Sprintf("thumbs/public-suggest-%02d.jpg", i),
			MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: fmt.Sprintf("public-suggest-%02d", i), Tags: []string{fmt.Sprintf("meta:needle_%02d", i)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		name, status         string
		quarantined, deleted bool
	}{
		{name: "draft_secret", status: "draft"},
		{name: "quarantined_secret", status: "published", quarantined: true},
		{name: "deleted_secret", status: "published", deleted: true},
	} {
		post, createErr := store.CreatePost(ctx, admin, archive.NewPost{
			Status: item.status, OriginalPath: "originals/" + item.name + ".png", ThumbnailPath: "thumbs/" + item.name + ".jpg",
			MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: item.name, Tags: []string{"character:" + item.name},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if item.quarantined {
			if _, updateErr := store.DB.Exec(`UPDATE posts SET quarantined_at=CURRENT_TIMESTAMP WHERE id=?`, post.ID); updateErr != nil {
				t.Fatal(updateErr)
			}
		}
		if item.deleted {
			if _, updateErr := store.DB.Exec(`UPDATE posts SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, post.ID); updateErr != nil {
				t.Fatal(updateErr)
			}
		}
	}
	_, err = store.CreatePost(ctx, admin, archive.NewPost{
		Status: "published", OriginalPath: "originals/public-artist.png", ThumbnailPath: "thumbs/public-artist.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "public-artist", Tags: []string{"artist:needle_artist"},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := server.Handler()
	publicRequest := httptest.NewRequest(http.MethodGet, "/tags/public-suggest?q=needle", nil)
	public := httptest.NewRecorder()
	handler.ServeHTTP(public, publicRequest)
	if public.Code != http.StatusOK || public.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(public.Body.String(), "needle_00") || strings.Count(public.Body.String(), `role="option"`) != 8 {
		t.Fatalf("public suggestions missing or not bounded: status=%d body=%s", public.Code, public.Body.String())
	}
	viewerSession, _ := store.NewSession(ctx, &viewer.ID)
	viewerResponse := sessionRequest(t, handler, http.MethodGet, "/tags/public-suggest?q=needle", nil, viewerSession)
	if viewerResponse.Code != http.StatusOK || !strings.Contains(viewerResponse.Body.String(), "needle_00") {
		t.Fatalf("viewer could not use public suggestions: status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}
	categoryRequest := httptest.NewRequest(http.MethodGet, "/tags/public-suggest?q=-artist:needle", nil)
	category := httptest.NewRecorder()
	handler.ServeHTTP(category, categoryRequest)
	if category.Code != http.StatusOK || !strings.Contains(category.Body.String(), "needle_artist") || strings.Contains(category.Body.String(), "needle_00") {
		t.Fatalf("negative category prefix was not scoped: status=%d body=%s", category.Code, category.Body.String())
	}
	hiddenRequest := httptest.NewRequest(http.MethodGet, "/tags/public-suggest?q=secret", nil)
	hidden := httptest.NewRecorder()
	handler.ServeHTTP(hidden, hiddenRequest)
	if hidden.Code != http.StatusOK || strings.Contains(hidden.Body.String(), "secret") {
		t.Fatalf("non-public tags leaked through suggestions: status=%d body=%s", hidden.Code, hidden.Body.String())
	}
	viewerEditor := sessionRequest(t, handler, http.MethodGet, "/tags/suggest?q=needle", nil, viewerSession)
	if viewerEditor.Code != http.StatusForbidden {
		t.Fatalf("viewer gained access to editor suggestions: status=%d body=%s", viewerEditor.Code, viewerEditor.Body.String())
	}
}

func TestTagAutocompleteIsPresentOnUploadAndMetadataForms(t *testing.T) {
	server, store := testServer(t)
	ctx := context.Background()
	admin, err := store.BootstrapSuperAdmin(ctx, "form-admin", "form admin password")
	if err != nil {
		t.Fatal(err)
	}
	post, err := store.CreatePost(ctx, admin, archive.NewPost{
		Status: "published", OriginalPath: "originals/form.png", ThumbnailPath: "thumbs/form.jpg",
		MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1, SHA256: "form-post", Tags: []string{"artist:sample_artist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := store.NewSession(ctx, &admin.ID)
	handler := server.Handler()
	upload := sessionRequest(t, handler, http.MethodGet, "/uploads/new", nil, session)
	if upload.Code != http.StatusOK || !strings.Contains(upload.Body.String(), `data-tag-autocomplete`) {
		t.Fatalf("upload form lacks tag autocomplete: status=%d body=%s", upload.Code, upload.Body.String())
	}
	edit := sessionRequest(t, handler, http.MethodGet, fmt.Sprintf("/posts/%d/edit", post.ID), nil, session)
	body := edit.Body.String()
	if edit.Code != http.StatusOK || !strings.Contains(body, `artist:sample_artist`) || !strings.Contains(body, `data-tag-autocomplete`) {
		t.Fatalf("metadata form did not preserve categorized token/autocomplete: status=%d body=%s", edit.Code, body)
	}
	updated := sessionRequest(t, handler, http.MethodPost, fmt.Sprintf("/posts/%d/edit", post.ID), url.Values{"tags": {"meta:source_note"}, "status": {"published"}}, session)
	if updated.Code != http.StatusSeeOther || updated.Header().Get("Location") != fmt.Sprintf("/posts/%d", post.ID) {
		t.Fatalf("ordinary metadata form did not redirect: status=%d location=%q", updated.Code, updated.Header().Get("Location"))
	}
	saved, err := store.Post(ctx, post.ID)
	if err != nil || len(saved.Tags) != 1 || saved.Tags[0].Name != "source_note" || saved.Tags[0].Category != "meta" {
		t.Fatalf("ordinary metadata form did not save category: %+v err=%v", saved, err)
	}
}
