package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"kura/internal/archive"
)

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
