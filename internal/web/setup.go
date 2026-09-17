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
		s.respondError(w, r, http.StatusGone, "")
		return
	}
	s.render(w, r, "setup", viewData{Title: "Set up Kura — Kura", ActiveNav: "account", SetupToken: token})
}

func (s *Server) setupPassword(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.BootstrapSuperAdminWithPassword(r.Context(), r.FormValue("token"), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		if errors.Is(err, archive.ErrInvalidBootstrap) {
			s.respondError(w, r, http.StatusGone, "")
			return
		}
		s.respondFormError(w, r, "setup", http.StatusBadRequest, viewData{Title: "Set up Kura — Kura", ActiveNav: "account", SetupToken: r.FormValue("token"), Error: "The setup details could not be accepted."})
		return
	}
	if err = s.replaceSession(w, r, user.ID); err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) setupPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Kura-Bootstrap")
	if !s.store.BootstrapTokenValid(r.Context(), token) {
		s.respondError(w, r, http.StatusGone, "")
		return
	}
	var input struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "The setup request could not be read.")
		return
	}
	user, err := archive.NewPasskeyRegistrationUser(input.Username)
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "The username could not be accepted.")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 64 {
		s.respondError(w, r, http.StatusBadRequest, "Passkey name must be between 1 and 64 characters.")
		return
	}
	passwordHash := ""
	if input.Password != "" {
		passwordHash, err = archive.PreparePassword(input.Password)
		if err != nil {
			s.respondError(w, r, http.StatusBadRequest, "The password could not be prepared.")
			return
		}
	}
	creation, session, err := s.passkeys.BeginRegistration(user)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	payload, err := json.Marshal(passkeyRegistrationState{Session: *session, Username: user.Username, Name: input.Name, Handle: user.PasskeyHandle, PasswordHash: passwordHash})
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	challenge, err := s.store.CreateAuthChallenge(r.Context(), "bootstrap", nil, payload, 5*time.Minute)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	writeJSON(w, map[string]any{"options": creation, "challengeToken": challenge})
}

func (s *Server) setupPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	challenge, err := s.store.ConsumeAuthChallenge(r.Context(), r.Header.Get("X-Kura-Challenge"), "bootstrap")
	if err != nil {
		s.respondError(w, r, http.StatusGone, "")
		return
	}
	var state passkeyRegistrationState
	if err = json.Unmarshal(challenge.Payload, &state); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "Passkey setup could not be completed.")
		return
	}
	user := archive.User{Username: state.Username, PasskeyHandle: state.Handle}
	credential, err := s.passkeys.FinishRegistration(user, state.Session, r)
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "Passkey setup could not be completed.")
		return
	}
	created, recovery, err := s.store.BootstrapSuperAdminWithPasskeyHash(r.Context(), r.Header.Get("X-Kura-Bootstrap"), state.Username, state.Name, state.Handle, *credential, state.PasswordHash)
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "Passkey setup could not be completed.")
		return
	}
	if err = s.replaceSession(w, r, created.ID); err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	writeJSON(w, map[string]any{"redirect": "/account", "recoveryCode": recovery})
}
