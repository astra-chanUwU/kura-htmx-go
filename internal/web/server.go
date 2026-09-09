package web

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kura/internal/archive"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	store     *archive.Store
	mediaRoot string
	templates *template.Template
}
type viewData struct {
	Title     string
	ActiveNav string
	Page      archive.PostPage
	Post      archive.Post
	Tags      []archive.Tag
	TagGroups map[string][]archive.Tag
	Pools     []archive.Pool
	Query     string
}

func New(store *archive.Store, mediaRoot string) (*Server, error) {
	funcs := template.FuncMap{
		"listCategories": func() []string { return []string{"artist", "character", "copyright", "general", "meta"} },
		"media":          func(p string) string { return "/media/" + strings.TrimLeft(p, "/") },
		"queryEscape":    url.QueryEscape,
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i + 1
			}
			return out
		},
		"humanBytes": func(n int64) string {
			if n < 1024 {
				return fmt.Sprintf("%d B", n)
			}
			if n < 1024*1024 {
				return fmt.Sprintf("%.1f KB", float64(n)/1024)
			}
			return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
		},
	}
	t, err := template.New("base").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: store, mediaRoot: mediaRoot, templates: t}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.home)
	mux.HandleFunc("GET /posts", s.posts)
	mux.HandleFunc("GET /posts/grid", s.grid)
	mux.HandleFunc("GET /posts/{id}", s.post)
	mux.HandleFunc("GET /random", s.random)
	mux.HandleFunc("GET /pools", s.pools)
	mux.HandleFunc("GET /pools/{slug}", s.pool)
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(s.mediaRoot))))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) render(w http.ResponseWriter, name string, data viewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render failed", 500)
	}
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, "home", viewData{Title: "Kura — your image archive", ActiveNav: "home"})
}
func pageNumber(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		return 1
	}
	return p
}
func groupTags(tags []archive.Tag) map[string][]archive.Tag {
	m := map[string][]archive.Tag{}
	for _, t := range tags {
		m[t.Category] = append(m[t.Category], t)
	}
	return m
}
func (s *Server) browseData(r *http.Request) (viewData, error) {
	q := r.URL.Query().Get("q")
	p, err := s.store.ListPosts(r.Context(), q, pageNumber(r), 24)
	if err != nil {
		return viewData{}, err
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		return viewData{}, err
	}
	return viewData{Title: "Posts — Kura", ActiveNav: "posts", Page: p, Tags: tags, TagGroups: groupTags(tags), Query: p.Query}, nil
}
func (s *Server) posts(w http.ResponseWriter, r *http.Request) {
	d, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", 500)
		return
	}
	s.render(w, "posts", d)
}
func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	d, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", 500)
		return
	}
	s.render(w, "browse-fragment", d)
}
func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.Post(r.Context(), id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", 500)
		return
	}
	s.render(w, "post", viewData{Title: fmt.Sprintf("Post %d — Kura", p.ID), ActiveNav: "posts", Post: p})
}
func (s *Server) random(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.RandomPostID(r.Context())
	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/posts", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}
func (s *Server) pools(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Pools(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", 500)
		return
	}
	s.render(w, "pools", viewData{Title: "Pools — Kura", ActiveNav: "pools", Pools: p})
}
func (s *Server) pool(w http.ResponseWriter, r *http.Request) { // Keep collection querying explicit until ordering UI lands.
	http.Redirect(w, r, "/pools", http.StatusSeeOther)
}
