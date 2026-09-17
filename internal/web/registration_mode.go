package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

const (
	RegistrationModeOpen       = "open"
	RegistrationModeClosed     = "closed"
	RegistrationModeInviteOnly = "invite-only"
)

func validRegistrationMode(mode string) bool {
	return mode == RegistrationModeOpen || mode == RegistrationModeClosed || mode == RegistrationModeInviteOnly
}

var errRegistrationClosed = errors.New("Registration is closed on this Kura instance.")
var errRegistrationInviteRequired = errors.New("Registration requires an invitation.")
var errRegistrationInviteInvalid = errors.New("The invitation is invalid or expired.")

func (s *Server) registrationAllowed(ctx context.Context, token string) error {
	switch s.registrationMode {
	case RegistrationModeClosed:
		return errRegistrationClosed
	case RegistrationModeInviteOnly:
		if token == "" {
			return errRegistrationInviteRequired
		}
		if !s.store.RegistrationInviteValid(ctx, token) {
			return errRegistrationInviteInvalid
		}
	}
	return nil
}

func registrationErrorStatus(err error) int {
	if errors.Is(err, errRegistrationClosed) || errors.Is(err, errRegistrationInviteRequired) {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func inviteURL(token string) string {
	return "/register?invite=" + url.QueryEscape(token)
}
