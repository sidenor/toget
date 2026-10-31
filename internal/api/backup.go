package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/sidenor/toget/internal/backup"
	"github.com/sidenor/toget/internal/update"
)

// BackupView is the public list and create shape for local backups.
type BackupView = backup.Archive

// RestoreUploadView is returned after an uploaded archive has been fully
// verified. The upload is not staged until the user confirms.
type RestoreUploadView = backup.Upload

// RestorePendingView is a staged restore waiting for the next daemon start.
type RestorePendingView = backup.Pending

// RestoreStatusView reports whether a restore is staged and whether this node
// can restart itself to apply it.
type RestoreStatusView struct {
	Staged            *RestorePendingView `json:"staged"`
	RestartConfigured bool                `json:"restart_configured"`
	RestartSupported  bool                `json:"restart_supported"`
}

type stageRestoreRequest struct {
	BackupName string `json:"backup_name,omitempty"`
	UploadID   string `json:"upload_id,omitempty"`
}

func (s *Server) backupsAvailable(w http.ResponseWriter) bool {
	if s.backups != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "backup_disabled", "backup support is not available")
	return false
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	items, err := s.backups.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": items})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	archive, err := s.backups.Create(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, archive)
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	name := r.PathValue("name")
	path, err := s.backups.Path(name)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not open the backup")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	if err := s.backups.Delete(r.PathValue("name")); err != nil {
		writeBackupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadRestore streams an archive to a private upload area and verifies
// every entry before returning its manifest. The body is intentionally raw, so
// large archives do not pass through JSON or multipart parsing.
func (s *Server) handleUploadRestore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	if r.ContentLength > backup.MaxArchiveSize {
		writeError(w, http.StatusRequestEntityTooLarge, "backup_too_large", "backup archive is too large")
		return
	}
	body := http.MaxBytesReader(w, r.Body, backup.MaxArchiveSize)
	upload, err := s.backups.SaveUpload(body)
	if err != nil {
		writeBackupError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, upload)
}

func (s *Server) handleDeleteRestoreUpload(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	if err := s.backups.DiscardUpload(r.PathValue("id")); err != nil {
		writeBackupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStageRestore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	var req stageRestoreRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.BackupName = strings.TrimSpace(req.BackupName)
	req.UploadID = strings.TrimSpace(req.UploadID)
	if (req.BackupName == "") == (req.UploadID == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "provide exactly one of backup_name or upload_id")
		return
	}
	var (
		pending *backup.Pending
		err     error
	)
	if req.UploadID != "" {
		pending, err = s.backups.StageUpload(req.UploadID)
	} else {
		pending, err = s.backups.StageBackup(req.BackupName)
	}
	if err != nil {
		writeBackupError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pending)
}

func (s *Server) handleGetRestore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	resp := RestoreStatusView{}
	staged, err := s.backups.Pending()
	if err == nil {
		resp.Staged = staged
	} else if !errors.Is(err, backup.ErrNoPendingRestore) {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	if s.updates != nil {
		view := s.updates.View()
		resp.RestartConfigured = view.RestartConfigured
		resp.RestartSupported = view.RestartSupported
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCancelRestore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	if err := s.backups.CancelPending(); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestartRestore applies a staged restore by restarting the daemon. The
// restore itself runs in the new process before the database is opened.
func (s *Server) handleRestartRestore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.backupsAvailable(w) {
		return
	}
	if _, err := s.backups.Pending(); err != nil {
		if errors.Is(err, backup.ErrNoPendingRestore) {
			writeError(w, http.StatusConflict, "no_pending_restore", "no restore is staged")
			return
		}
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	if s.updates == nil {
		writeError(w, http.StatusConflict, "restart_not_configured", "restart Toget manually to apply the restore")
		return
	}
	if err := s.updates.Restart(r.Context()); err != nil {
		if errors.Is(err, update.ErrNoRestartCommand) {
			writeError(w, http.StatusConflict, "restart_not_configured", "restart Toget manually to apply the restore")
			return
		}
		writeError(w, http.StatusBadGateway, "restart_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
}

func writeBackupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrBackupNotFound):
		writeError(w, http.StatusNotFound, "backup_not_found", "backup was not found")
	case errors.Is(err, backup.ErrNoPendingRestore):
		writeError(w, http.StatusNotFound, "no_pending_restore", "no restore is staged")
	case errors.Is(err, backup.ErrArchiveTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "backup_too_large", err.Error())
	case errors.Is(err, backup.ErrInvalidBackup), errors.Is(err, backup.ErrUnsafePath):
		writeError(w, http.StatusBadRequest, "invalid_backup", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
	}
}
