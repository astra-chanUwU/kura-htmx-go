package web

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"kura/internal/archive"
	mediafiles "kura/internal/media"
)

//go:embed templates/*.html static/*
var assets embed.FS

const sessionCookie = "kura_session"

type contextKey int

const sessionContext contextKey = iota

type Server struct {
	store           *archive.Store
	mediaRoot       string
	media           mediafiles.Ingestor
	templates       *template.Template
	passkeys        passkeyCeremonies
	loginLimiter    *attemptLimiter
	recoveryLimiter *attemptLimiter
}

type passkeyCeremonies interface {
	BeginRegistration(archive.User) (*protocol.CredentialCreation, *webauthn.SessionData, error)
	FinishRegistration(archive.User, webauthn.SessionData, *http.Request) (*webauthn.Credential, error)
	BeginDiscoverableLogin() (*protocol.CredentialAssertion, *webauthn.SessionData, error)
	FinishPasskeyLogin(webauthn.DiscoverableUserHandler, webauthn.SessionData, *http.Request) (webauthn.User, *webauthn.Credential, error)
	BeginLogin(archive.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error)
	FinishLogin(archive.User, webauthn.SessionData, *http.Request) (*webauthn.Credential, error)
}

type goPasskeys struct{ instance *webauthn.WebAuthn }

func (p goPasskeys) BeginRegistration(user archive.User) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	return p.instance.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()),
	)
}
func (p goPasskeys) FinishRegistration(user archive.User, session webauthn.SessionData, r *http.Request) (*webauthn.Credential, error) {
	return p.instance.FinishRegistration(user, session, r)
}
func (p goPasskeys) BeginDiscoverableLogin() (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return p.instance.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
}
func (p goPasskeys) FinishPasskeyLogin(handler webauthn.DiscoverableUserHandler, session webauthn.SessionData, r *http.Request) (webauthn.User, *webauthn.Credential, error) {
	return p.instance.FinishPasskeyLogin(handler, session, r)
}
func (p goPasskeys) BeginLogin(user archive.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return p.instance.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
}
func (p goPasskeys) FinishLogin(user archive.User, session webauthn.SessionData, r *http.Request) (*webauthn.Credential, error) {
	return p.instance.FinishLogin(user, session, r)
}

type AuthConfig struct {
	RPID    string
	Origins []string
}

type viewData struct {
	Title, ActiveNav, Query, Error, Notice, Next string
	RecoveryCode                                 string
	SetupToken                                   string
	Source, PoolSlug                             string
	Page                                         archive.PostPage
	Post                                         archive.Post
	Posts                                        []archive.Post
	Tags                                         []archive.Tag
	TagGroups                                    map[string][]archive.Tag
	Pools                                        []archive.Pool
	Pool                                         archive.Pool
	Users                                        []archive.User
	User                                         *archive.User
	Security                                     archive.AccountSecurity
	CSRF                                         string
	PostTags                                     string
	PoolPostIDs                                  string
	Selected                                     map[int64]bool
}

func New(store *archive.Store, mediaRoot string) (*Server, error) {
	rpID := os.Getenv("KURA_RP_ID")
	if rpID == "" {
		rpID = "localhost"
	}
	origins := []string{"http://localhost:8080"}
	if raw := os.Getenv("KURA_ORIGINS"); raw != "" {
		origins = nil
		for _, origin := range strings.Split(raw, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				origins = append(origins, origin)
			}
		}
	}
	return NewWithAuth(store, mediaRoot, AuthConfig{RPID: rpID, Origins: origins})
}

func NewWithAuth(store *archive.Store, mediaRoot string, auth AuthConfig) (*Server, error) {
	if err := validateAuthConfig(auth); err != nil {
		return nil, err
	}
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
	passkeys, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Kura",
		RPID:          auth.RPID,
		RPOrigins:     auth.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Server{
		store: store, mediaRoot: mediaRoot, media: mediafiles.Ingestor{Root: mediaRoot, Store: store}, templates: t,
		passkeys: goPasskeys{passkeys}, loginLimiter: newAttemptLimiter(5, time.Minute), recoveryLimiter: newAttemptLimiter(5, 5*time.Minute),
	}, nil
}

func validateAuthConfig(auth AuthConfig) error {
	if auth.RPID == "" || len(auth.Origins) == 0 {
		return errors.New("WebAuthn RP ID and allowed origins are required")
	}
	for _, raw := range auth.Origins {
		origin, err := url.Parse(raw)
		if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return fmt.Errorf("invalid WebAuthn origin %q", raw)
		}
		host := origin.Hostname()
		if !strings.EqualFold(host, auth.RPID) && !strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(auth.RPID)) {
			return fmt.Errorf("WebAuthn origin %q is outside RP ID %q", raw, auth.RPID)
		}
		if origin.Scheme != "https" && !(origin.Scheme == "http" && strings.EqualFold(auth.RPID, "localhost")) {
			return fmt.Errorf("WebAuthn origin %q must use HTTPS", raw)
		}
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.home)
	mux.HandleFunc("GET /setup", s.setupForm)
	mux.HandleFunc("POST /setup/password", s.setupPassword)
	mux.HandleFunc("POST /setup/passkey/begin", s.setupPasskeyBegin)
	mux.HandleFunc("POST /setup/passkey/finish", s.setupPasskeyFinish)
	mux.HandleFunc("GET /posts", s.posts)
	mux.HandleFunc("GET /posts/grid", s.grid)
	mux.HandleFunc("GET /posts/{id}", s.post)
	mux.HandleFunc("POST /posts/{id}/favorite", s.favorite)
	mux.HandleFunc("GET /favorites", s.favorites)
	mux.HandleFunc("POST /favorites/{id}/remove", s.removeFavorite)
	mux.HandleFunc("POST /posts/{id}/pools", s.addPostToPool)
	mux.HandleFunc("GET /posts/{id}/edit", s.editPost)
	mux.HandleFunc("POST /posts/{id}/edit", s.updatePost)
	mux.HandleFunc("POST /posts/{id}/delete", s.deletePost)
	mux.HandleFunc("GET /random", s.random)
	mux.HandleFunc("GET /pools", s.pools)
	mux.HandleFunc("GET /pools/picker", s.poolPicker)
	mux.HandleFunc("GET /pools/new", s.newPool)
	mux.HandleFunc("POST /pools", s.createPool)
	mux.HandleFunc("GET /pools/{slug}", s.pool)
	mux.HandleFunc("GET /pools/{slug}/edit", s.editPool)
	mux.HandleFunc("POST /pools/{slug}/edit", s.updatePool)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /recover", s.recoveryForm)
	mux.HandleFunc("POST /recover", s.recoverAccount)
	mux.HandleFunc("POST /recover/passkey/begin", s.recoverPasskeyBegin)
	mux.HandleFunc("POST /recover/passkey/finish", s.recoverPasskeyFinish)
	mux.HandleFunc("POST /auth/passkeys/register/begin", s.passkeyRegistrationBegin)
	mux.HandleFunc("POST /auth/passkeys/register/finish", s.passkeyRegistrationFinish)
	mux.HandleFunc("POST /auth/passkeys/login/begin", s.passkeyLoginBegin)
	mux.HandleFunc("POST /auth/passkeys/login/finish", s.passkeyLoginFinish)
	mux.HandleFunc("POST /auth/passkeys/fresh/begin", s.passkeyFreshBegin)
	mux.HandleFunc("POST /auth/passkeys/fresh/finish", s.passkeyFreshFinish)
	mux.HandleFunc("POST /account/passkeys/add/begin", s.passkeyAddBegin)
	mux.HandleFunc("POST /account/passkeys/add/finish", s.passkeyAddFinish)
	mux.HandleFunc("POST /account/passkeys/{id}/remove", s.passkeyRemove)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /account", s.account)
	mux.HandleFunc("POST /account/password/remove", s.removePassword)
	mux.HandleFunc("POST /account/password", s.changePassword)
	mux.HandleFunc("POST /account/recovery/replace", s.replaceRecoveryCode)
	mux.HandleFunc("POST /account/sessions/revoke", s.revokeOtherSessions)
	mux.HandleFunc("GET /uploads/new", s.newUpload)
	mux.HandleFunc("GET /admin/uploads/new", s.newUpload)
	mux.HandleFunc("POST /uploads", s.upload)
	mux.HandleFunc("POST /admin/uploads", s.upload)
	mux.HandleFunc("GET /admin/accounts", s.adminAccounts)
	mux.HandleFunc("POST /admin/accounts/{id}/role", s.adminRole)
	mux.HandleFunc("POST /admin/accounts/{id}/suspension", s.adminSuspension)
	mux.HandleFunc("POST /admin/accounts/{id}/transfer", s.adminTransfer)
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /media/{kind}/{path...}", s.serveMedia)
	return securityHeaders(s.withSession(s.withCSRF(mux)))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/media/") {
			next.ServeHTTP(w, r)
			return
		}
		var session archive.Session
		cookie, err := r.Cookie(sessionCookie)
		if err == nil {
			session, err = s.store.Session(r.Context(), cookie.Value)
		}
		if err != nil || cookie == nil {
			session, err = s.store.NewSession(r.Context(), nil)
			if err != nil {
				http.Error(w, "session unavailable", http.StatusInternalServerError)
				return
			}
			s.setSessionCookie(w, r, session)
		}
		ctx := context.WithValue(r.Context(), sessionContext, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				r.Body = http.MaxBytesReader(w, r.Body, mediafiles.MaxUploadBytes+(2<<20))
				_ = r.ParseMultipartForm(1 << 20)
			} else {
				_ = r.ParseForm()
			}
			want := currentSession(r).CSRF
			got := r.Header.Get("X-CSRF-Token")
			if got == "" {
				got = r.FormValue("csrf")
			}
			if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
				http.Error(w, "invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func currentSession(r *http.Request) archive.Session {
	session, _ := r.Context().Value(sessionContext).(archive.Session)
	return session
}

func currentUser(r *http.Request) *archive.User { return currentSession(r).User }

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, session archive.Session) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, Expires: session.ExpiresAt})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data viewData) {
	session := currentSession(r)
	if data.User == nil {
		data.User = session.User
	}
	data.CSRF = session.CSRF
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
	}
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) *archive.User {
	user := currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
		return nil
	}
	return user
}

func (s *Server) requireModerator(w http.ResponseWriter, r *http.Request) *archive.User {
	user := s.requireUser(w, r)
	if user != nil && !user.CanUpload() {
		http.Error(w, "moderator access required", http.StatusForbidden)
		return nil
	}
	return user
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *archive.User {
	user := s.requireUser(w, r)
	if user != nil && !user.CanAdminister() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return nil
	}
	return user
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "home", viewData{Title: "Kura — your image archive", ActiveNav: "home"})
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.store.BootstrapTokenValid(r.Context(), token) {
		http.Error(w, archive.ErrInvalidBootstrap.Error(), http.StatusGone)
		return
	}
	s.render(w, r, "setup", viewData{Title: "Set up Kura — Kura", ActiveNav: "account", SetupToken: token})
}

func (s *Server) setupPassword(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.BootstrapSuperAdminWithPassword(r.Context(), r.FormValue("token"), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrInvalidBootstrap) {
			status = http.StatusGone
		}
		w.WriteHeader(status)
		s.render(w, r, "setup", viewData{Title: "Set up Kura — Kura", ActiveNav: "account", SetupToken: r.FormValue("token"), Error: err.Error()})
		return
	}
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) setupPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Kura-Bootstrap")
	if !s.store.BootstrapTokenValid(r.Context(), token) {
		http.Error(w, archive.ErrInvalidBootstrap.Error(), http.StatusGone)
		return
	}
	var input struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	user, err := archive.NewPasskeyRegistrationUser(input.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 64 {
		http.Error(w, "passkey name must be between 1 and 64 characters", http.StatusBadRequest)
		return
	}
	passwordHash := ""
	if input.Password != "" {
		passwordHash, err = archive.PreparePassword(input.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	creation, session, err := s.passkeys.BeginRegistration(user)
	if err != nil {
		http.Error(w, "passkey setup unavailable", http.StatusInternalServerError)
		return
	}
	payload, err := json.Marshal(passkeyRegistrationState{Session: *session, Username: user.Username, Name: input.Name, Handle: user.PasskeyHandle, PasswordHash: passwordHash})
	if err != nil {
		http.Error(w, "passkey setup unavailable", http.StatusInternalServerError)
		return
	}
	challenge, err := s.store.CreateAuthChallenge(r.Context(), "bootstrap", nil, payload, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey setup unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"options": creation, "challengeToken": challenge})
}

func (s *Server) setupPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "bootstrap")
	if err != nil {
		http.Error(w, archive.ErrInvalidBootstrap.Error(), http.StatusGone)
		return
	}
	var state passkeyRegistrationState
	if err = json.Unmarshal(challenge.Payload, &state); err != nil {
		http.Error(w, "passkey setup failed", http.StatusBadRequest)
		return
	}
	user := archive.User{Username: state.Username, PasskeyHandle: state.Handle}
	credential, err := s.passkeys.FinishRegistration(user, state.Session, r)
	if err != nil {
		http.Error(w, "passkey setup failed", http.StatusBadRequest)
		return
	}
	created, recovery, err := s.store.BootstrapSuperAdminWithPasskeyHash(r.Context(), r.Header.Get("X-Kura-Bootstrap"), state.Username, state.Name, state.Handle, *credential, state.PasswordHash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err = s.replaceSession(w, r, created.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"redirect": "/account", "recoveryCode": recovery})
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
	for _, tag := range tags {
		m[tag.Category] = append(m[tag.Category], tag)
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
	data, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "posts", data)
}

func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	data, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "browse-fragment", data)
}

func postID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func (s *Server) visiblePost(r *http.Request) (archive.Post, error) {
	id, err := postID(r)
	if err != nil {
		return archive.Post{}, sql.ErrNoRows
	}
	user := currentUser(r)
	if user == nil {
		return s.store.Post(r.Context(), id)
	}
	return s.store.PostForUser(r.Context(), id, user.ID, user.CanUpload())
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	p, err := s.visiblePost(r)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	var pools []archive.Pool
	if user := currentUser(r); user != nil && p.Status == "published" {
		pools, err = s.store.OwnedPoolsForPost(r.Context(), user.ID, p.ID)
		if err != nil {
			http.Error(w, "pools unavailable", http.StatusInternalServerError)
			return
		}
	}
	s.render(w, r, "post", viewData{Title: fmt.Sprintf("Post %d — Kura", p.ID), ActiveNav: "posts", Post: p, Pools: pools})
}

func (s *Server) favorite(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.SetFavorite(r.Context(), user.ID, id, r.FormValue("favorite") == "1"); err != nil {
		http.Error(w, "favorite could not be updated", http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			http.Error(w, "favorite could not be refreshed", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "favorite-control", viewData{Post: post})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func (s *Server) favorites(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	posts, err := s.store.Favorites(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "favorites unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "favorites", viewData{Title: "Favorites — Kura", ActiveNav: "favorites", Posts: posts})
}

func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.SetFavorite(r.Context(), user.ID, id, false); err != nil {
		http.Error(w, "favorite could not be removed", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		posts, err := s.store.Favorites(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "favorites could not be refreshed", http.StatusInternalServerError)
			return
		}
		if len(posts) == 0 {
			s.render(w, r, "favorites-empty", viewData{})
		}
		return
	}
	http.Redirect(w, r, "/favorites", http.StatusSeeOther)
}

func (s *Server) addPostToPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err == nil {
		err = s.store.AddPostToPool(r.Context(), user.ID, r.FormValue("pool"), id)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		}
		http.Error(w, "post could not be added to that pool", status)
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			http.Error(w, "pool action could not be refreshed", http.StatusInternalServerError)
			return
		}
		pools, err := s.store.OwnedPoolsForPost(r.Context(), user.ID, id)
		if err != nil {
			http.Error(w, "pools unavailable", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "pool-control", viewData{Post: post, Pools: pools})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func tagsString(tags []archive.Tag) string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		values = append(values, tag.Name)
	}
	return strings.Join(values, " ")
}

func (s *Server) editPost(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	p, err := s.visiblePost(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "post-edit", viewData{Title: fmt.Sprintf("Edit post %d — Kura", p.ID), ActiveNav: "posts", Post: p, PostTags: tagsString(p.Tags)})
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.UpdatePost(r.Context(), id, r.FormValue("source"), r.FormValue("tags"), r.FormValue("status")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			http.Error(w, "post could not be refreshed", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "quick-edit", viewData{Post: post, PostTags: tagsString(post.Tags), Notice: "Saved."})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	p, err := s.visiblePost(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !user.CanDelete(p) {
		http.Error(w, "you may delete only your own uploads", http.StatusForbidden)
		return
	}
	if err = s.store.SoftDeletePost(r.Context(), p.ID); err != nil {
		http.Error(w, "post could not be deleted", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/posts", http.StatusSeeOther)
}

func (s *Server) random(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.RandomPostID(r.Context())
	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/posts", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func viewerID(r *http.Request) int64 {
	if user := currentUser(r); user != nil {
		return user.ID
	}
	return 0
}

func (s *Server) pools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.store.PoolsForUser(r.Context(), viewerID(r))
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "pools", viewData{Title: "Pools — Kura", ActiveNav: "pools", Pools: pools})
}

func (s *Server) pool(w http.ResponseWriter, r *http.Request) {
	pool, err := s.store.Pool(r.Context(), r.PathValue("slug"), viewerID(r))
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "pool", viewData{Title: pool.Name + " — Kura", ActiveNav: "pools", Pool: pool})
}

func (s *Server) newPool(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	data, err := s.poolEditorData(r, archive.Pool{Status: "draft"}, "")
	if err != nil {
		http.Error(w, "posts unavailable", http.StatusInternalServerError)
		return
	}
	data.Title, data.ActiveNav = "New pool — Kura", "pools"
	s.render(w, r, "pool-edit", data)
}

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	pool, err := s.store.CreatePool(r.Context(), user.ID, r.FormValue("name"), r.FormValue("description"), r.FormValue("status"), r.FormValue("post_ids"))
	if err != nil {
		data, loadErr := s.poolEditorData(r, archive.Pool{Name: r.FormValue("name"), Description: r.FormValue("description"), Status: r.FormValue("status")}, r.FormValue("post_ids"))
		if loadErr != nil {
			http.Error(w, "posts unavailable", http.StatusInternalServerError)
			return
		}
		data.Title, data.ActiveNav, data.Error = "New pool — Kura", "pools", err.Error()
		s.render(w, r, "pool-edit", data)
		return
	}
	http.Redirect(w, r, "/pools/"+pool.Slug, http.StatusSeeOther)
}

func (s *Server) editPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	pool, err := s.store.Pool(r.Context(), r.PathValue("slug"), user.ID)
	if err != nil || pool.OwnerID != user.ID {
		http.Error(w, "only the pool owner may edit it", http.StatusForbidden)
		return
	}
	data, err := s.poolEditorData(r, pool, pool.PostIDs())
	if err != nil {
		http.Error(w, "posts unavailable", http.StatusInternalServerError)
		return
	}
	data.Title, data.ActiveNav = "Edit "+pool.Name+" — Kura", "pools"
	s.render(w, r, "pool-edit", data)
}

func (s *Server) updatePool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	err := s.store.UpdatePool(r.Context(), user.ID, r.PathValue("slug"), r.FormValue("name"), r.FormValue("description"), r.FormValue("status"), r.FormValue("post_ids"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/pools/"+r.PathValue("slug"), http.StatusSeeOther)
}

func selectedPostIDs(raw string) map[int64]bool {
	selected := map[int64]bool{}
	for _, value := range strings.Fields(raw) {
		id, err := strconv.ParseInt(value, 10, 64)
		if err == nil && id > 0 {
			selected[id] = true
		}
	}
	return selected
}

func (s *Server) poolEditorData(r *http.Request, pool archive.Pool, postIDs string) (viewData, error) {
	page, err := s.store.ListPosts(r.Context(), "", 1, 100)
	if err != nil {
		return viewData{}, err
	}
	pools, err := s.store.PoolsForUser(r.Context(), viewerID(r))
	if err != nil {
		return viewData{}, err
	}
	return viewData{Pool: pool, PoolPostIDs: postIDs, Page: page, Pools: pools, Selected: selectedPostIDs(postIDs), Source: "all"}, nil
}

func (s *Server) poolPicker(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	query := r.URL.Query()
	page, err := s.store.PoolCandidates(r.Context(), archive.PoolCandidateFilter{
		Source:   query.Get("source"),
		PoolSlug: query.Get("pool"),
		Query:    query.Get("q"),
		ViewerID: user.ID,
		Page:     1,
		PerPage:  100,
	})
	if err == sql.ErrNoRows {
		if query.Get("source") == "pool" && query.Get("pool") == "" {
			s.render(w, r, "pool-picker", viewData{Page: archive.PostPage{}, Selected: selectedPostIDs(query.Get("post_ids")), Source: "pool"})
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "posts unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "pool-picker", viewData{Page: page, Selected: selectedPostIDs(query.Get("post_ids")), Source: query.Get("source"), PoolSlug: query.Get("pool")})
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, "auth", viewData{Title: "Register — Kura", ActiveNav: "account", Next: "/account"})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.Register(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.render(w, r, "auth", viewData{Title: "Register — Kura", ActiveNav: "account", Error: err.Error(), Next: "/account"})
		return
	}
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

type passkeyRegistrationState struct {
	Session      webauthn.SessionData `json:"session"`
	Username     string               `json:"username"`
	Name         string               `json:"name"`
	Handle       []byte               `json:"handle"`
	UserID       int64                `json:"userId,omitempty"`
	PasswordHash string               `json:"passwordHash,omitempty"`
	RecoveryHash string               `json:"recoveryHash,omitempty"`
}

func (s *Server) passkeyRegistrationBegin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 64 {
		http.Error(w, "passkey name must be between 1 and 64 characters", http.StatusBadRequest)
		return
	}
	user, err := archive.NewPasskeyRegistrationUser(input.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	creation, session, err := s.passkeys.BeginRegistration(user)
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	state, err := json.Marshal(passkeyRegistrationState{Session: *session, Username: user.Username, Name: input.Name, Handle: user.PasskeyHandle})
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateAuthChallenge(r.Context(), "register", nil, state, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"options": creation, "challengeToken": token})
}

func (s *Server) passkeyRegistrationFinish(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "register")
	if err != nil {
		http.Error(w, archive.ErrInvalidChallenge.Error(), http.StatusBadRequest)
		return
	}
	var state passkeyRegistrationState
	if err = json.Unmarshal(challenge.Payload, &state); err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	user := archive.User{Username: state.Username, PasskeyHandle: state.Handle}
	credential, err := s.passkeys.FinishRegistration(user, state.Session, r)
	if err != nil {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	created, recovery, err := s.store.CreatePasskeyOnlyAccountWithHandle(r.Context(), state.Username, state.Name, state.Handle, *credential)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrUsernameTaken) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	if err = s.replaceSession(w, r, created.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"redirect": "/account", "recoveryCode": recovery})
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, session, err := s.passkeys.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, "passkey sign-in unavailable", http.StatusInternalServerError)
		return
	}
	payload, err := json.Marshal(session)
	if err != nil {
		http.Error(w, "passkey sign-in unavailable", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateAuthChallenge(r.Context(), "login", nil, payload, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey sign-in unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"options": assertion, "challengeToken": token})
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "login")
	if err != nil {
		http.Error(w, "passkey sign-in failed", http.StatusBadRequest)
		return
	}
	var session webauthn.SessionData
	if err = json.Unmarshal(challenge.Payload, &session); err != nil {
		http.Error(w, "passkey sign-in failed", http.StatusBadRequest)
		return
	}
	validated, credential, err := s.passkeys.FinishPasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		user, loadErr := s.store.PasskeyUser(r.Context(), rawID, userHandle)
		if loadErr != nil {
			return nil, archive.ErrInvalidLogin
		}
		return user, nil
	}, session, r)
	if err != nil {
		http.Error(w, "passkey sign-in failed", http.StatusBadRequest)
		return
	}
	user, ok := validated.(archive.User)
	if !ok || !user.Active() {
		http.Error(w, "passkey sign-in failed", http.StatusBadRequest)
		return
	}
	if err = s.store.UpdatePasskey(r.Context(), user.ID, *credential); err != nil {
		http.Error(w, "passkey sign-in failed", http.StatusBadRequest)
		return
	}
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"redirect": safeNext(r.URL.Query().Get("next"))})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) passkeyFreshBegin(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	authUser, err := s.store.WebAuthnUser(r.Context(), user.ID)
	if err != nil || len(authUser.Credentials) == 0 {
		http.Error(w, "passkey verification unavailable", http.StatusBadRequest)
		return
	}
	assertion, session, err := s.passkeys.BeginLogin(authUser)
	if err != nil {
		http.Error(w, "passkey verification unavailable", http.StatusInternalServerError)
		return
	}
	payload, err := json.Marshal(session)
	if err != nil {
		http.Error(w, "passkey verification unavailable", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateAuthChallenge(r.Context(), "fresh", &user.ID, payload, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey verification unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"options": assertion, "challengeToken": token})
}

func (s *Server) passkeyAddBegin(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 64 {
		http.Error(w, "passkey name must be between 1 and 64 characters", http.StatusBadRequest)
		return
	}
	authUser, err := s.store.WebAuthnUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusBadRequest)
		return
	}
	creation, session, err := s.passkeys.BeginRegistration(authUser)
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	payload, err := json.Marshal(passkeyRegistrationState{Session: *session, Name: input.Name, UserID: user.ID})
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateAuthChallenge(r.Context(), "add", &user.ID, payload, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey registration unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"options": creation, "challengeToken": token})
}

func (s *Server) passkeyAddFinish(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "add")
	if err != nil || challenge.UserID == nil || *challenge.UserID != user.ID {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	var state passkeyRegistrationState
	if err = json.Unmarshal(challenge.Payload, &state); err != nil || state.UserID != user.ID {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	authUser, err := s.store.WebAuthnUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	credential, err := s.passkeys.FinishRegistration(authUser, state.Session, r)
	if err != nil {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	if _, err = s.store.AddPasskey(r.Context(), user.ID, state.Name, *credential); err != nil {
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	status, err := s.store.SecurityStatus(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "account security unavailable", http.StatusInternalServerError)
		return
	}
	recovery := ""
	if !status.RecoveryCodeActive {
		recovery, err = s.store.ReplaceRecoveryCode(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "recovery code unavailable", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"added": true, "recoveryCode": recovery, "redirect": "/account"})
}

func (s *Server) passkeyFreshFinish(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "fresh")
	if err != nil || challenge.UserID == nil || *challenge.UserID != user.ID {
		http.Error(w, "passkey verification failed", http.StatusBadRequest)
		return
	}
	var session webauthn.SessionData
	if err = json.Unmarshal(challenge.Payload, &session); err != nil {
		http.Error(w, "passkey verification failed", http.StatusBadRequest)
		return
	}
	authUser, err := s.store.WebAuthnUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "passkey verification failed", http.StatusBadRequest)
		return
	}
	credential, err := s.passkeys.FinishLogin(authUser, session, r)
	if err != nil || s.store.UpdatePasskey(r.Context(), user.ID, *credential) != nil {
		http.Error(w, "passkey verification failed", http.StatusBadRequest)
		return
	}
	if err = s.store.MarkSessionPasskeyVerified(r.Context(), currentSession(r).Token, user.ID); err != nil {
		http.Error(w, "passkey verification failed", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"verified": true})
}

func (s *Server) removePassword(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	if r.FormValue("confirmation") != "remove" {
		http.Error(w, "confirmation required", http.StatusBadRequest)
		return
	}
	fresh, err := s.store.SessionHasFreshPasskey(r.Context(), currentSession(r).Token, 5*time.Minute)
	if err != nil || !fresh {
		http.Error(w, "fresh passkey verification required", http.StatusForbidden)
		return
	}
	if err = s.store.RemovePassword(r.Context(), user.ID, currentSession(r).Token); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/account?notice=password-removed", http.StatusSeeOther)
}

func (s *Server) passkeyRemove(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.RemovePasskey(r.Context(), user.ID, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/account?notice=passkey-removed", http.StatusSeeOther)
}

func (s *Server) replaceRecoveryCode(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	fresh, err := s.store.SessionHasFreshPasskey(r.Context(), currentSession(r).Token, 5*time.Minute)
	if err != nil || !fresh {
		http.Error(w, "fresh passkey verification required", http.StatusForbidden)
		return
	}
	code, err := s.store.ReplaceRecoveryCode(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "recovery code unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "recovery", viewData{Title: "Recovery code replaced — Kura", ActiveNav: "account", User: user, RecoveryCode: code})
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	if err := s.store.RevokeOtherSessions(r.Context(), user.ID, currentSession(r).Token); err != nil {
		http.Error(w, "sessions could not be revoked", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account?notice=sessions-revoked", http.StatusSeeOther)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	status, err := s.store.SecurityStatus(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "account security unavailable", http.StatusInternalServerError)
		return
	}
	if status.PasswordEnabled {
		if _, err = s.store.Authenticate(r.Context(), user.Username, r.FormValue("current_password")); err != nil {
			http.Error(w, archive.ErrInvalidLogin.Error(), http.StatusForbidden)
			return
		}
	} else {
		fresh, freshErr := s.store.SessionHasFreshPasskey(r.Context(), currentSession(r).Token, 5*time.Minute)
		if freshErr != nil || !fresh {
			http.Error(w, "fresh passkey verification required", http.StatusForbidden)
			return
		}
	}
	if err = s.store.SetPassword(r.Context(), user.ID, r.FormValue("password")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err = s.store.RevokeOtherSessions(r.Context(), user.ID, currentSession(r).Token); err != nil {
		http.Error(w, "sessions could not be revoked", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account?notice=password-updated", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, "auth", viewData{Title: "Sign in — Kura", ActiveNav: "account", Next: safeNext(r.URL.Query().Get("next"))})
}

func safeNext(raw string) string {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	return "/account"
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	key := authenticationRateKey(r, r.FormValue("username"))
	if !s.loginLimiter.Allow(key) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, r, "auth", viewData{Title: "Sign in — Kura", ActiveNav: "account", Error: archive.ErrInvalidLogin.Error(), Next: safeNext(r.FormValue("next"))})
		return
	}
	user, err := s.store.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.render(w, r, "auth", viewData{Title: "Sign in — Kura", ActiveNav: "account", Error: archive.ErrInvalidLogin.Error(), Next: safeNext(r.FormValue("next"))})
		return
	}
	s.loginLimiter.Reset(key)
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

func authenticationRateKey(r *http.Request, username string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host + "|" + strings.ToLower(username)
}

func (s *Server) recoveryForm(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, "recovery", viewData{Title: "Recover account — Kura", ActiveNav: "account"})
}

func (s *Server) recoverAccount(w http.ResponseWriter, r *http.Request) {
	key := authenticationRateKey(r, r.FormValue("username"))
	if !s.recoveryLimiter.Allow(key) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, r, "recovery", viewData{Title: "Recover account — Kura", ActiveNav: "account", Error: archive.ErrInvalidRecovery.Error()})
		return
	}
	user, replacement, err := s.store.RecoverPassword(r.Context(), r.FormValue("username"), r.FormValue("recovery_code"), r.FormValue("password"))
	if err != nil {
		errorMessage := archive.ErrInvalidRecovery.Error()
		if !errors.Is(err, archive.ErrInvalidRecovery) {
			errorMessage = err.Error()
		}
		s.render(w, r, "recovery", viewData{Title: "Recover account — Kura", ActiveNav: "account", Error: errorMessage})
		return
	}
	s.recoveryLimiter.Reset(key)
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "recovery", viewData{Title: "Recovery complete — Kura", ActiveNav: "account", User: &user, RecoveryCode: replacement})
}

func (s *Server) recoverPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username     string `json:"username"`
		RecoveryCode string `json:"recoveryCode"`
		Name         string `json:"name"`
		Password     string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, archive.ErrInvalidRecovery.Error(), http.StatusBadRequest)
		return
	}
	key := authenticationRateKey(r, input.Username)
	if !s.recoveryLimiter.Allow(key) {
		http.Error(w, archive.ErrInvalidRecovery.Error(), http.StatusTooManyRequests)
		return
	}
	user, recoveryHash, err := s.store.VerifyRecoveryCode(r.Context(), input.Username, input.RecoveryCode)
	if err != nil {
		http.Error(w, archive.ErrInvalidRecovery.Error(), http.StatusBadRequest)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 64 {
		http.Error(w, "passkey name must be between 1 and 64 characters", http.StatusBadRequest)
		return
	}
	passwordHash := ""
	if input.Password != "" {
		passwordHash, err = archive.PreparePassword(input.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	creation, session, err := s.passkeys.BeginRegistration(user)
	if err != nil {
		http.Error(w, "passkey recovery unavailable", http.StatusInternalServerError)
		return
	}
	payload, err := json.Marshal(passkeyRegistrationState{Session: *session, Name: input.Name, UserID: user.ID, PasswordHash: passwordHash, RecoveryHash: recoveryHash})
	if err != nil {
		http.Error(w, "passkey recovery unavailable", http.StatusInternalServerError)
		return
	}
	challenge, err := s.store.CreateAuthChallenge(r.Context(), "recover-passkey", &user.ID, payload, 5*time.Minute)
	if err != nil {
		http.Error(w, "passkey recovery unavailable", http.StatusInternalServerError)
		return
	}
	s.recoveryLimiter.Reset(key)
	writeJSON(w, map[string]any{"options": creation, "challengeToken": challenge})
}

func (s *Server) recoverPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "recover-passkey")
	if err != nil || challenge.UserID == nil {
		http.Error(w, "passkey recovery failed", http.StatusBadRequest)
		return
	}
	var state passkeyRegistrationState
	if err = json.Unmarshal(challenge.Payload, &state); err != nil || state.UserID != *challenge.UserID {
		http.Error(w, "passkey recovery failed", http.StatusBadRequest)
		return
	}
	user, err := s.store.WebAuthnUser(r.Context(), state.UserID)
	if err != nil {
		http.Error(w, "passkey recovery failed", http.StatusBadRequest)
		return
	}
	credential, err := s.passkeys.FinishRegistration(user, state.Session, r)
	if err != nil {
		http.Error(w, "passkey recovery failed", http.StatusBadRequest)
		return
	}
	replacement, err := s.store.CompletePasskeyRecovery(r.Context(), user.ID, state.RecoveryHash, state.Name, *credential, state.PasswordHash)
	if err != nil {
		http.Error(w, "passkey recovery failed", http.StatusBadRequest)
		return
	}
	if err = s.replaceSession(w, r, user.ID); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"redirect": "/account", "recoveryCode": replacement})
}

func (s *Server) replaceSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	_ = s.store.DeleteSession(r.Context(), currentSession(r).Token)
	session, err := s.store.NewSession(r.Context(), &userID)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, session)
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.store.DeleteSession(r.Context(), currentSession(r).Token)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	posts, err := s.store.Favorites(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "account unavailable", http.StatusInternalServerError)
		return
	}
	pools, err := s.store.PoolsForUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "account unavailable", http.StatusInternalServerError)
		return
	}
	owned := pools[:0]
	for _, pool := range pools {
		if pool.OwnerID == user.ID {
			owned = append(owned, pool)
		}
	}
	security, err := s.store.SecurityStatus(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "account unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "account", viewData{Title: user.Username + " — Kura", ActiveNav: "account", Posts: posts, Pools: owned, Security: security, Notice: r.URL.Query().Get("notice")})
}

func (s *Server) newUpload(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload"})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediafiles.MaxUploadBytes+(2<<20))
	file, header, err := r.FormFile("image")
	if err != nil {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "Choose an image to upload."})
		return
	}
	defer file.Close()
	post, err := s.media.Ingest(r.Context(), file, header, user.ID, r.FormValue("source"), r.FormValue("tags"), r.FormValue("status"))
	if err != nil {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: err.Error()})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", post.ID), http.StatusSeeOther)
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		http.Error(w, "accounts unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "admin", viewData{Title: "Accounts — Kura", ActiveNav: "admin", Users: users})
}

func targetID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (s *Server) adminRole(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.SetUserRole(r.Context(), *actor, id, r.FormValue("role"))
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminSuspension(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.SetUserSuspended(r.Context(), *actor, id, r.FormValue("suspended") == "1")
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminTransfer(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.TransferSuperAdmin(r.Context(), *actor, id)
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminResult(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	if isHTMX(r) {
		users, err := s.store.Users(r.Context())
		if err != nil {
			http.Error(w, "accounts unavailable", http.StatusInternalServerError)
			return
		}
		actor, err := s.store.User(r.Context(), currentUser(r).ID)
		if err != nil {
			http.Error(w, "account unavailable", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "admin-account-list", viewData{Users: users, User: &actor})
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "originals" && kind != "thumbs" {
		http.NotFound(w, r)
		return
	}
	rel := filepath.Clean(r.PathValue("path"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	root, err := filepath.Abs(s.mediaRoot)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(root, kind, rel)
	if _, err = os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
