// Package httpapi is the HTTP presentation layer: routing, validation and JSON serialization.
// It holds no business rules; it translates between HTTP and application use cases.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// maxJSONBody bounds every JSON request body.
const maxJSONBody = 64 << 10

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeDomainError maps domain/application errors to the documented error contract.
func writeDomainError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{Code: "validation_failed", Message: ve.Message, Field: ve.Field}})
	case errors.Is(err, domain.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	case errors.Is(err, domain.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, domain.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
	case errors.Is(err, domain.ErrSetupCompleted):
		writeError(w, http.StatusConflict, "setup_completed", "initial setup has already been completed")
	case errors.Is(err, domain.ErrSetupTokenInvalid):
		writeError(w, http.StatusForbidden, "setup_token_invalid", "invalid setup token")
	case errors.Is(err, domain.ErrDockerUnavailable):
		log.Warn("docker unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "docker_unavailable", "Docker is not reachable")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	default:
		log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// decodeJSON reads a size-limited JSON object, rejecting unknown fields and trailing data.
// On failure it writes the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a valid JSON object")
		}
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object")
		return false
	}
	return true
}
