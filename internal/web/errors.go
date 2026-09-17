package web

import (
	"html/template"
	"io"
	"net/http"
	"strings"
)

func isJSONRequest(r *http.Request) bool {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(contentType, "application/json") ||
		strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/auth/passkeys/") ||
		strings.HasPrefix(r.URL.Path, "/setup/passkey/") ||
		strings.HasPrefix(r.URL.Path, "/recover/passkey/")
}

func errorCopy(status int, requested string) (heading, message string) {
	switch status {
	case http.StatusForbidden:
		if requested != "" {
			return "Access denied", requested
		}
		return "Access denied", "This page or action is not available to your account."
	case http.StatusNotFound:
		return "Not found", "The page or item you requested is not available."
	case http.StatusGone:
		return "Link expired", "This setup link has expired or has already been used."
	case http.StatusTooManyRequests:
		return "Too many attempts", "Please wait a moment before trying again."
	case http.StatusInternalServerError:
		return "Something went wrong", "Kura could not complete that request."
	default:
		if requested == "" {
			return "Request could not be completed", "Kura could not complete that request."
		}
		return "Request could not be completed", requested
	}
}

func (s *Server) respondError(w http.ResponseWriter, r *http.Request, status int, requested string) {
	heading, message := errorCopy(status, requested)
	if isJSONRequest(r) {
		writeJSONError(w, status, message)
		return
	}
	if isHTMX(r) {
		writeHTMXError(w, status, message)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.render(w, r, "error", viewData{
		Title:        heading + " — Kura",
		Error:        message,
		ErrorHeading: heading,
		ErrorStatus:  status,
	})
}

func (s *Server) respondFormError(w http.ResponseWriter, r *http.Request, name string, status int, data viewData) {
	if isJSONRequest(r) || isHTMX(r) {
		s.respondError(w, r, status, data.Error)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.render(w, r, name, data)
}

func writeHTMXError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<p class="alert" role="alert" tabindex="-1" data-kura-error>`+template.HTMLEscapeString(message)+`</p>`)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": message})
}

func writeProtocolError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, message+"\n")
}
