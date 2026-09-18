package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestBrowseSearchSortAndHTMXPreserveContext(t *testing.T) {
	server, store := testServer(t)
	for i := 0; i < 25; i++ {
		insertWebNavigationPost(t, store.DB, "search-page-"+strconv.Itoa(i), "published", "2026-09-17T"+strconv.FormatInt(int64(i), 10)+":00:00Z", nil, "needle")
	}
	insertWebNavigationPost(t, store.DB, "search-excluded", "published", "2026-09-18T00:00:00Z", nil, "needle", "spoiler")

	query := url.QueryEscape("needle -spoiler")
	path := "/posts?q=" + query + "&sort=oldest"
	page := searchRequest(t, server.Handler(), path, false)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `option value="oldest" selected`) || !strings.Contains(body, `hx-get="/posts/grid?`) || !strings.Contains(body, `sort=oldest`) || !strings.Contains(body, `page=2`) {
		t.Fatalf("browse search/sort context was not rendered: status=%d body=%s", page.Code, body)
	}
	queryRow := strings.Index(body, `class="search-query-row"`)
	sortRow := strings.Index(body, `class="search-sort-row"`)
	if queryRow < 0 || sortRow < 0 || queryRow > sortRow || !strings.Contains(body[queryRow:sortRow], `name="q"`) || !strings.Contains(body[sortRow:], `name="sort"`) || !strings.Contains(body[sortRow:], `class="search-submit"`) {
		t.Fatalf("browse search controls lack semantic query/sort rows: status=%d body=%s", page.Code, body)
	}
	if !strings.Contains(body, `data-tag-categories`) || !strings.Contains(body, `data-tag-categories open`) || !strings.Contains(body, `>Filter by tags</summary>`) {
		t.Fatalf("browse tag categories lack disclosure grouping: status=%d body=%s", page.Code, body)
	}
	detail := searchRequest(t, server.Handler(), "/posts/1?context=browse&q="+query+"&sort=oldest", false)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "sort=oldest") {
		t.Fatalf("post context did not preserve oldest sort: status=%d body=%s", detail.Code, detail.Body.String())
	}

	hxPath := "/posts/grid?q=" + query + "&sort=oldest&page=2"
	hx := searchRequest(t, server.Handler(), hxPath, true)
	if hx.Code != http.StatusOK || strings.Contains(hx.Body.String(), `class="site-header"`) || !strings.Contains(hx.Body.String(), `sort=oldest`) {
		t.Fatalf("HTMX grid did not preserve search/sort context: status=%d body=%s", hx.Code, hx.Body.String())
	}
}

func TestBrowseRejectsMalformedSearchWithoutBroadening(t *testing.T) {
	server, _ := testServer(t)
	response := searchRequest(t, server.Handler(), "/posts?q=artist%3A", false)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Search query") {
		t.Fatalf("malformed search response: status=%d body=%s", response.Code, response.Body.String())
	}
	unsupportedSort := searchRequest(t, server.Handler(), "/posts?sort=random", false)
	if unsupportedSort.Code != http.StatusBadRequest {
		t.Fatalf("unsupported sort status=%d body=%s", unsupportedSort.Code, unsupportedSort.Body.String())
	}
}

func searchRequest(t *testing.T, handler http.Handler, path string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
