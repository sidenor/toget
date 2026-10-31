package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/update"
)

// UpdateView is the public state of the in-daemon update monitor.
type UpdateView = update.MonitorStatus

type updateCheckRequest struct {
	Force bool `json:"force,omitempty"`
}

type updateApplyRequest struct {
	Release string `json:"release"`
}

type updateDismissRequest struct {
	Release string `json:"release"`
	Hours   int    `json:"hours,omitempty"`
}

// UpdateCheckView is returned by an explicit update check.
type UpdateCheckView struct {
	Result *update.CheckResult `json:"result"`
	Update UpdateView          `json:"update"`
}

// UpdateApplyView is returned after staging an update or rolling back.
type UpdateApplyView struct {
	Result any        `json:"result"`
	Update UpdateView `json:"update"`
}

func (s *Server) handleGetUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.updates.View())
}

func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	var req updateCheckRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.updates.Check(r.Context(), req.Force)
	if err != nil {
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, UpdateCheckView{Result: result, Update: s.updates.View()})
}

func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	var req updateApplyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Release = strings.TrimSpace(req.Release)
	if req.Release == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "release is required")
		return
	}
	result, err := s.updates.Apply(r.Context(), req.Release)
	if err != nil {
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, UpdateApplyView{Result: result, Update: s.updates.View()})
}

func (s *Server) handleDismissUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	var req updateDismissRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	duration := time.Duration(req.Hours) * time.Hour
	if req.Hours < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "hours must not be negative")
		return
	}
	if err := s.updates.Dismiss(req.Release, duration); err != nil {
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.updates.View())
}

func (s *Server) handleClearDismissUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	if err := s.updates.ClearDismissal(); err != nil {
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.updates.View())
}

func (s *Server) handleRollbackUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	result, err := s.updates.Rollback(r.Context())
	if err != nil {
		if errors.Is(err, update.ErrNoPrevious) {
			writeError(w, http.StatusConflict, "no_previous_release", "no previous release is available to roll back to")
			return
		}
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, UpdateApplyView{Result: result, Update: s.updates.View()})
}

func (s *Server) handleRestartUpdate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "update_disabled", "update checking is not configured")
		return
	}
	if err := s.updates.Restart(r.Context()); err != nil {
		s.writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
}

func (s *Server) writeUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, update.ErrUpdateNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "update_not_configured",
			"updates are not configured; set update.root, update.channel_url, and update.root_key")
	case errors.Is(err, update.ErrReleaseUnavailable):
		writeError(w, http.StatusConflict, "release_unavailable", "that release is no longer offered by the channel")
	case errors.Is(err, update.ErrNoRestartCommand):
		writeError(w, http.StatusConflict, "restart_not_configured",
			"no restart command is configured; restart Toget manually to finish the update")
	case errors.Is(err, update.ErrDismissalTooLong):
		writeError(w, http.StatusBadRequest, "dismissal_too_long", err.Error())
	default:
		writeError(w, http.StatusBadGateway, "update_failed", err.Error())
	}
}

// decodeOptionalJSON tolerates an empty body for action endpoints whose
// request object is entirely optional.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
