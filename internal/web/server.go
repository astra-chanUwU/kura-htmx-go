package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
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
	store            *archive.Store
	mediaRoot        string
	media            mediafiles.Ingestor
	templates        *template.Template
	passkeys         passkeyCeremonies
	registrationMode string
	loginLimiter     *attemptLimiter
	recoveryLimiter  *attemptLimiter
}

type viewData struct {
	Title, ActiveNav, Query, Error, Notice, Next, Status string
	Sort                                                 string
	ErrorHeading                                         string
	ErrorStatus                                          int
	RegistrationMode                                     string
	InviteToken, InviteURL                               string
	RecoveryCode                                         string
	SetupToken                                           string
	Source, PoolSlug                                     string
	Page                                                 archive.PostPage
	Post                                                 archive.Post
	Posts                                                []archive.Post
	Tags                                                 []archive.Tag
	TagPage                                              archive.TagPage
	TagPreview                                           archive.TagMaintenancePreview
	TagGroups                                            map[string][]archive.Tag
	Pools                                                []archive.Pool
	Pool                                                 archive.Pool
	Users                                                []archive.User
	User                                                 *archive.User
	Security                                             archive.AccountSecurity
	CSRF                                                 string
	PostTags                                             string
	PoolPostIDs                                          string
	Selected                                             map[int64]bool
	BulkPreview                                          archive.BulkTagPreview
	BulkPostIDs, BulkAddTags, BulkRemoveTags             string
	BulkSource                                           string
	Audit                                                archive.AuditPage
	AuditActions                                         []string
	PostContext                                          postContext
	BackURL, PreviousURL, NextURL                        string
	HasPrevious, HasNext                                 bool
	ExportMaxPosts                                       int
	ExportMaxBytes, ExportBytes                          int64
	UploaderID                                           int64
	Invites                                              []archive.RegistrationInvite
	BatchResults                                         []uploadResult
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
	return NewWithAuth(store, mediaRoot, AuthConfig{RPID: rpID, Origins: origins, RegistrationMode: os.Getenv("KURA_REGISTRATION_MODE")})
}

func NewWithAuth(store *archive.Store, mediaRoot string, auth AuthConfig) (*Server, error) {
	if err := validateAuthConfig(auth); err != nil {
		return nil, err
	}
	registrationMode := auth.RegistrationMode
	if registrationMode == "" {
		registrationMode = string(RegistrationModeOpen)
	}
	if !validRegistrationMode(registrationMode) {
		return nil, fmt.Errorf("invalid registration mode %q", registrationMode)
	}
	funcs := template.FuncMap{
		"listCategories": func() []string { return []string{"artist", "character", "copyright", "general", "meta"} },
		"media":          func(p string) string { return "/media/" + strings.TrimLeft(p, "/") },
		"queryEscape":    url.QueryEscape,
		"tagLabel": func(tag archive.Tag) string {
			if tag.Category == "general" {
				return tag.Name
			}
			return tag.Category + ":" + tag.Name
		},
		"auditLabel":       auditEventLabel,
		"auditActionLabel": auditActionLabel,
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
		"passkeyDefault":   passkeyDefaultName,
		"recoveryDetails":  recoveryDetails,
		"recoveryFilename": recoveryDownloadFilename,
		"inviteStatus": func(invite archive.RegistrationInvite) string {
			if invite.ConsumedAt != "" {
				return "used"
			}
			if invite.RevokedAt != "" {
				return "revoked"
			}
			if !invite.ExpiresAt.After(time.Now()) {
				return "expired"
			}
			return "active"
		},
		"postLink": postLink,
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
	ingestor := mediafiles.Ingestor{Root: mediaRoot, Store: store}
	if err = ingestor.ReconcileStagedDeletes(context.Background()); err != nil {
		return nil, err
	}
	return &Server{
		store: store, mediaRoot: mediaRoot, media: ingestor, templates: t,
		registrationMode: registrationMode,
		passkeys:         goPasskeys{passkeys}, loginLimiter: newAttemptLimiter(5, time.Minute), recoveryLimiter: newAttemptLimiter(5, 5*time.Minute),
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
	mux.HandleFunc("GET /posts/export", s.selectedExport)
	mux.HandleFunc("POST /posts/bulk-tags/preview", s.previewBulkTags)
	mux.HandleFunc("POST /posts/bulk-tags/apply", s.applyBulkTags)
	mux.HandleFunc("GET /tags/suggest", s.tagSuggestions)
	mux.HandleFunc("GET /posts/{id}", s.post)
	mux.HandleFunc("GET /posts/{id}/download", s.download)
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
	mux.HandleFunc("GET /pools/{slug}/export", s.poolExport)
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
	mux.HandleFunc("GET /uploads", s.uploads)
	mux.HandleFunc("POST /uploads/{id}/status", s.uploadStatus)
	mux.HandleFunc("POST /uploads/{id}/delete", s.deleteUpload)
	mux.HandleFunc("POST /uploads/{id}/permanent-delete", s.permanentDeleteUpload)
	mux.HandleFunc("GET /admin/accounts", s.adminAccounts)
	mux.HandleFunc("GET /admin/invites", s.adminInvites)
	mux.HandleFunc("POST /admin/invites", s.createAdminInvite)
	mux.HandleFunc("POST /admin/invites/{id}/revoke", s.revokeAdminInvite)
	mux.HandleFunc("GET /admin/images", s.adminImages)
	mux.HandleFunc("GET /admin/tags", s.adminTags)
	mux.HandleFunc("POST /admin/tags/preview", s.adminTagsPreview)
	mux.HandleFunc("POST /admin/tags/apply", s.adminTagsApply)
	mux.HandleFunc("GET /admin/audit", s.adminAudit)
	mux.HandleFunc("POST /admin/audit/{id}/revert", s.adminAuditRevert)
	mux.HandleFunc("GET /admin/images/{id}/review", s.adminImageReview)
	mux.HandleFunc("POST /admin/images/{id}/quarantine", s.adminQuarantine)
	mux.HandleFunc("POST /admin/images/{id}/restore", s.adminRestore)
	mux.HandleFunc("POST /admin/images/{id}/permanent-delete", s.adminPermanentDelete)
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
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		var session archive.Session
		cookie, err := r.Cookie(sessionCookie)
		if err == nil {
			session, err = s.store.Session(r.Context(), cookie.Value)
		}
		if err != nil || cookie == nil {
			if strings.HasPrefix(r.URL.Path, "/media/") {
				next.ServeHTTP(w, r)
				return
			}
			session, err = s.store.NewSession(r.Context(), nil)
			if err != nil {
				s.respondError(w, r, http.StatusInternalServerError, "")
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
				r.Body = http.MaxBytesReader(w, r.Body, mediafiles.MaxUploadRequestBytes)
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
				if r.MultipartForm != nil {
					_ = r.MultipartForm.RemoveAll()
				}
				s.respondError(w, r, http.StatusForbidden, "This form could not be verified. Please try again.")
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
	if data.RegistrationMode == "" {
		data.RegistrationMode = s.registrationMode
	}
	var body bytes.Buffer
	if err := s.templates.ExecuteTemplate(&body, name, data); err != nil {
		if name == "error" {
			writeProtocolError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body.Bytes())
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
		s.respondError(w, r, http.StatusForbidden, "")
		return nil
	}
	return user
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *archive.User {
	user := s.requireUser(w, r)
	if user != nil && !user.CanAdminister() {
		s.respondError(w, r, http.StatusForbidden, "")
		return nil
	}
	return user
}

func (s *Server) requireSuperAdmin(w http.ResponseWriter, r *http.Request) *archive.User {
	user := s.requireUser(w, r)
	if user != nil && !user.IsSuperAdmin {
		s.respondError(w, r, http.StatusForbidden, "")
		return nil
	}
	return user
}
