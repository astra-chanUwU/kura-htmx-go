package web

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"kura/internal/archive"
)

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
