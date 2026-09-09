package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTemplatesUseVendoredHTMX(t *testing.T) {
	body, err := assets.ReadFile("templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, `src="/static/htmx.min.js"`) {
		t.Error("base template does not reference the vendored HTMX asset")
	}
	if strings.Contains(html, "unpkg.com") {
		t.Error("base template still references the unpkg CDN")
	}
	if _, err := assets.ReadFile("static/htmx.min.js"); err != nil {
		t.Errorf("vendored HTMX asset is missing: %v", err)
	}
}

func TestHomeUsesKuraBanner(t *testing.T) {
	body, err := assets.ReadFile("templates/home.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, `src="/static/images/kura-banner.png"`) {
		t.Error("home template does not reference the Kura banner")
	}
	if strings.Contains(html, "safebooru-reference") {
		t.Error("home template still references the temporary Safebooru banner")
	}
	if _, err := assets.ReadFile("static/images/kura-banner.png"); err != nil {
		t.Errorf("Kura banner asset is missing: %v", err)
	}
}

func TestNavigationMarksOnlyTheCurrentSection(t *testing.T) {
	s, err := New(nil, "")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		template  string
		activeNav string
		current   string
	}{
		{name: "home", template: "home", activeNav: "home", current: `href="/" aria-current="page"`},
		{name: "posts", template: "posts", activeNav: "posts", current: `href="/posts" aria-current="page"`},
		{name: "post detail", template: "post", activeNav: "posts", current: `href="/posts" aria-current="page"`},
		{name: "pools", template: "pools", activeNav: "pools", current: `href="/pools" aria-current="page"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			s.render(recorder, tt.template, viewData{Title: "Kura", ActiveNav: tt.activeNav})
			html := recorder.Body.String()
			if !strings.Contains(html, tt.current) {
				t.Errorf("current navigation item missing %q", tt.current)
			}
			if got := strings.Count(html, `aria-current="page"`); got != 1 {
				t.Errorf("current navigation item count = %d, want 1", got)
			}
		})
	}
}
