package httpapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// restoreDeadline bounds a restore: extracting a whole world.
const restoreDeadline = 30 * time.Minute

type backupDTO struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	SizeBytes   int64   `json:"size_bytes"`
	Failure     string  `json:"failure"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt *string `json:"completed_at"`
}

func toBackupDTO(b domain.Backup) backupDTO {
	d := backupDTO{ID: b.ID, Kind: string(b.Kind), Status: string(b.Status), SizeBytes: b.SizeBytes,
		Failure: b.Failure, CreatedAt: rfc3339(b.CreatedAt)}
	if !b.CompletedAt.IsZero() {
		c := rfc3339(b.CompletedAt)
		d.CompletedAt = &c
	}
	return d
}

type backupScheduleDTO struct {
	IntervalHours int     `json:"interval_hours"`
	Retain        int     `json:"retain"`
	NextRunAt     *string `json:"next_run_at"`
}

func toBackupScheduleDTO(sc domain.BackupSchedule) backupScheduleDTO {
	d := backupScheduleDTO{IntervalHours: sc.IntervalHours, Retain: sc.Retain}
	if !sc.NextRunAt.IsZero() {
		n := rfc3339(sc.NextRunAt)
		d.NextRunAt = &n
	}
	return d
}

// withBackupID rejects backup IDs the panel could not have generated before any lookup.
func withBackupID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !domain.ValidBackupID(r.PathValue("backupID")) {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		next(w, r)
	}
}

func (a *API) listBackups(w http.ResponseWriter, r *http.Request) {
	list, err := a.backups.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	out := make([]backupDTO, len(list))
	for i, b := range list {
		out[i] = toBackupDTO(b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": out})
}

// createBackup starts a backup and answers at once; clients poll the list until it is no
// longer "creating".
func (a *API) createBackup(w http.ResponseWriter, r *http.Request) {
	b, err := a.backups.Create(r.Context(), r.PathValue("id"), actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toBackupDTO(b))
}

func (a *API) restoreBackup(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, restoreDeadline)
	if err := a.backups.Restore(r.Context(), r.PathValue("id"), r.PathValue("backupID"), actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := a.backups.Delete(r.Context(), r.PathValue("id"), r.PathValue("backupID"), actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// downloadBackup streams a backup archive; like file downloads, a failure after the headers
// were sent aborts the connection instead of delivering a truncated file.
func (a *API) downloadBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := a.backups.Download(r.Context(), id, r.PathValue("backupID"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	defer d.File.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": d.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Length", strconv.FormatInt(d.Size, 10))
	w.WriteHeader(http.StatusOK)
	out := progressWriter{w: w, rc: http.NewResponseController(w)}
	if _, err := io.CopyN(out, d.File, d.Size); err != nil {
		a.log.Info("backup download interrupted", "server_id", id, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func (a *API) getBackupSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := a.backups.Schedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupScheduleDTO(sc))
}

func (a *API) updateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntervalHours *int `json:"interval_hours"`
		Retain        *int `json:"retain"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case req.IntervalHours == nil:
		writeDomainError(w, a.log, &domain.ValidationError{Field: "interval_hours", Message: "is required"})
		return
	case req.Retain == nil:
		writeDomainError(w, a.log, &domain.ValidationError{Field: "retain", Message: "is required"})
		return
	}
	sc, err := a.backups.UpdateSchedule(r.Context(), r.PathValue("id"), *req.IntervalHours, *req.Retain, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupScheduleDTO(sc))
}
