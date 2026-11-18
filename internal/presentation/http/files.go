package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	// streamIdle is how long a download or upload may make no progress before the connection
	// is dropped. Transfers have no total deadline: worlds can be large.
	streamIdle = 60 * time.Second
	// extractDeadline bounds a zip extraction (up to domain.ExtractMaxBytes written).
	extractDeadline = 30 * time.Minute
	// maxTextBody leaves room for JSON escaping of a file at the editor's size limit.
	maxTextBody = 6*domain.EditableFileMaxBytes + 64<<10
	// maxUploadBody adds room for multipart headers to the upload limit.
	maxUploadBody = domain.UploadMaxBytes + 1<<20
)

type fileEntryDTO struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

func toFileEntryDTO(e domain.FileEntry) fileEntryDTO {
	return fileEntryDTO{Name: e.Name, Type: string(e.Type), Size: e.Size, ModifiedAt: rfc3339(e.ModifiedAt)}
}

// progressWriter and progressReader push the connection deadline forward on every chunk,
// so a transfer only fails when it stalls (streamIdle), never because it is long.
type progressWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (p progressWriter) Write(b []byte) (int, error) {
	_ = p.rc.SetWriteDeadline(time.Now().Add(streamIdle))
	return p.w.Write(b)
}

// progressReader also remembers a failure to read the request body, which is the client's
// fault (interrupted or oversized upload) rather than the server's.
type progressReader struct {
	r   io.Reader
	rc  *http.ResponseController
	err error
}

func (p *progressReader) Read(b []byte) (int, error) {
	_ = p.rc.SetReadDeadline(time.Now().Add(streamIdle))
	n, err := p.r.Read(b)
	if err != nil && !errors.Is(err, io.EOF) {
		p.err = err
	}
	return n, err
}

func boolParam(r *http.Request, name string) (bool, error) {
	switch r.URL.Query().Get(name) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, &domain.ValidationError{Field: name, Message: `must be "true" or "false"`}
}

func (a *API) listFiles(w http.ResponseWriter, r *http.Request) {
	l, err := a.files.List(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	entries := make([]fileEntryDTO, len(l.Entries))
	for i, e := range l.Entries {
		entries[i] = toFileEntryDTO(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": l.Path, "entries": entries, "truncated": l.Truncated})
}

// downloadFile streams a file, or a directory as a zip archive. Once streaming has started an
// error can no longer be reported as JSON, so the connection is aborted instead: the browser
// then shows a failed download rather than a silently truncated file.
func (a *API) downloadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := a.files.Download(r.Context(), id, r.URL.Query().Get("path"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": d.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Disposition", disposition)
	out := progressWriter{w: w, rc: http.NewResponseController(w)}

	if d.File != nil {
		defer d.File.Close()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.FormatInt(d.Size, 10))
		w.WriteHeader(http.StatusOK)
		if _, err := io.CopyN(out, d.File, d.Size); err != nil {
			a.log.Info("download interrupted", "server_id", id, "err", err)
			panic(http.ErrAbortHandler)
		}
		return
	}
	h.Set("Content-Type", "application/zip")
	w.WriteHeader(http.StatusOK)
	if err := a.files.WriteZip(id, d, out); err != nil {
		a.log.Info("zip download interrupted", "server_id", id, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func (a *API) readFileContent(w http.ResponseWriter, r *http.Request) {
	ft, err := a.files.ReadText(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": ft.Path, "content": ft.Content, "version": ft.Version, "size": ft.Size})
}

func (a *API) writeFileContent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if !decodeJSONLimit(w, r, &req, maxTextBody) {
		return
	}
	v, err := a.files.WriteText(r.Context(), r.PathValue("id"), req.Path, req.Content, req.Version, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": v})
}

// uploadFiles streams every file part of a multipart body to disk, one at a time; nothing
// is buffered in memory or in temporary files outside the server directory.
func (a *API) uploadFiles(w http.ResponseWriter, r *http.Request) {
	overwrite, err := boolParam(r, "overwrite")
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	rc := http.NewResponseController(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "request body must be multipart/form-data")
		return
	}
	dir := r.URL.Query().Get("path")
	saved := []fileEntryDTO{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.uploadError(w, rc, err, true)
			return
		}
		if part.FileName() == "" {
			part.Close() // ordinary form fields are ignored
			continue
		}
		body := &progressReader{r: part, rc: rc}
		e, err := a.files.Upload(r.Context(), r.PathValue("id"), dir, part.FileName(), body, overwrite, actorOf(r))
		part.Close()
		if err != nil {
			a.uploadError(w, rc, err, body.err != nil)
			return
		}
		saved = append(saved, toFileEntryDTO(e))
	}
	_ = rc.SetWriteDeadline(time.Now().Add(streamIdle))
	if len(saved) == 0 {
		writeDomainError(w, a.log, &domain.ValidationError{Field: "files", Message: "no file in the request"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"files": saved})
}

// uploadError answers a failed upload. clientSide marks failures to read the request body.
func (a *API) uploadError(w http.ResponseWriter, rc *http.ResponseController, err error, clientSide bool) {
	_ = rc.SetWriteDeadline(time.Now().Add(streamIdle))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "uploads are limited to 10 GiB per request")
	case clientSide:
		writeError(w, http.StatusBadRequest, "invalid_multipart", "the upload was interrupted or malformed")
	default:
		writeDomainError(w, a.log, err)
	}
}

func (a *API) makeDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.files.Mkdir(r.Context(), r.PathValue("id"), req.Path, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) renameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.files.Rename(r.Context(), r.PathValue("id"), req.From, req.To, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, stopDeadline) // a large directory tree takes a while
	if err := a.files.Delete(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"), actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) extractArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path        string `json:"path"`
		Destination string `json:"destination"`
		Overwrite   bool   `json:"overwrite"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	extendDeadlines(w, extractDeadline)
	n, err := a.files.Extract(r.Context(), r.PathValue("id"), req.Path, req.Destination, req.Overwrite, actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"entries": n})
}

func (a *API) getModpack(w http.ResponseWriter, r *http.Request) {
	info, err := a.files.ModpackInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *API) configureModpack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstallScript string `json:"install_script"`
		StartScript   string `json:"start_script"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.files.ConfigureModpack(r.Context(), r.PathValue("id"), req.InstallScript, req.StartScript, actorOf(r)); err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	info, err := a.files.ModpackInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *API) installModpack(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, createDeadline)
	v, err := a.files.InstallModpack(r.Context(), r.PathValue("id"), actorOf(r))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toServerDTO(v))
}
