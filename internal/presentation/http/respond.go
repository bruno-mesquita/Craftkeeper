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
	case errors.Is(err, domain.ErrServerNotRunning):
		writeError(w, http.StatusConflict, "server_not_running", "the server is not running")
	case errors.Is(err, domain.ErrServerRunning):
		writeError(w, http.StatusConflict, "server_running", "stop the server first")
	case errors.Is(err, domain.ErrContainerMissing):
		writeError(w, http.StatusConflict, "container_missing", "the server's container no longer exists; delete the server")
	case errors.Is(err, domain.ErrNotManaged):
		log.Warn("container ownership check failed", "err", err)
		writeError(w, http.StatusConflict, "container_not_managed", "the container does not belong to this server")
	case errors.Is(err, domain.ErrPortInUse):
		writeError(w, http.StatusConflict, "port_in_use", "the server's port is already in use on the host")
	case errors.Is(err, domain.ErrDockerFailed):
		log.Error("docker operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "docker_error", "Docker rejected the operation; see the panel log")
	case errors.Is(err, domain.ErrFileExists):
		writeError(w, http.StatusConflict, "file_exists", "a file or directory with that name already exists")
	case errors.Is(err, domain.ErrNotDirectory):
		writeError(w, http.StatusConflict, "not_a_directory", "the path is not a directory")
	case errors.Is(err, domain.ErrIsDirectory):
		writeError(w, http.StatusConflict, "is_a_directory", "the path is a directory")
	case errors.Is(err, domain.ErrFileChanged):
		writeError(w, http.StatusConflict, "file_changed", "the file changed since it was opened")
	case errors.Is(err, domain.ErrFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "the file is too large to edit")
	case errors.Is(err, domain.ErrNotText):
		writeError(w, http.StatusUnsupportedMediaType, "not_text", "the file is not UTF-8 text")
	case errors.Is(err, domain.ErrInvalidArchive):
		log.Info("invalid archive", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "invalid_archive", "the file is not a valid or safe zip archive")
	case errors.Is(err, domain.ErrArchiveTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "archive_too_large", "the archive is too large to extract")
	case errors.Is(err, domain.ErrInsufficientStorage):
		log.Warn("disk full", "err", err)
		writeError(w, http.StatusInsufficientStorage, "insufficient_storage", "not enough disk space")
	case errors.Is(err, domain.ErrBackupInProgress):
		writeError(w, http.StatusConflict, "backup_in_progress", "a backup or restore of this server is in progress")
	case errors.Is(err, domain.ErrBackupNotReady):
		writeError(w, http.StatusConflict, "backup_not_ready", "the backup is not ready")
	case errors.Is(err, domain.ErrServerBusy):
		writeError(w, http.StatusConflict, "server_busy", "the server is being created, started or restarted; try again shortly")
	case errors.Is(err, domain.ErrModpackServer):
		writeError(w, http.StatusConflict, "modpack_server", "the server runs a modpack, whose scripts choose the game version")
	case errors.Is(err, domain.ErrModsUnsupported):
		writeError(w, http.StatusConflict, "mods_unsupported", "the server has no mod loader")
	case errors.Is(err, domain.ErrModIncompatible):
		writeError(w, http.StatusConflict, "mod_incompatible", err.Error())
	case errors.Is(err, domain.ErrModrinthUnavailable):
		log.Warn("modrinth unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "modrinth_unavailable", "Modrinth could not be reached or sent an invalid file; try again later")
	case errors.Is(err, domain.ErrInvalidWorld):
		writeError(w, http.StatusUnprocessableEntity, "invalid_world", "the archive does not contain a Minecraft world (level.dat)")
	case errors.Is(err, domain.ErrBackupFailed):
		log.Error("automatic backup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_failed", "the automatic backup failed, so nothing was changed")
	case errors.Is(err, domain.ErrInstallerUnavailable):
		log.Warn("version source unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "installer_unavailable", "the version list could not be downloaded; try again later")
	case errors.Is(err, domain.ErrGameUnreachable):
		writeError(w, http.StatusServiceUnavailable, "game_unreachable", "the game is not answering yet; it may still be starting")
	case errors.Is(err, domain.ErrAccountLookupUnavailable):
		log.Warn("mojang account lookup failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "account_lookup_unavailable", "Mojang could not be reached to look up the player; try again later or start the server")
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
	return decodeJSONLimit(w, r, dst, maxJSONBody)
}

// decodeJSONLimit is decodeJSON with a specific body size limit.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
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
