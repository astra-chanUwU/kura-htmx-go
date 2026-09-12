package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"kura/internal/archive"
)

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
