package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/node"
)

const maxPairRequestBody = 128 << 10

func (s *Server) handleCreatePairing(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	view, err := s.node.CreatePairing(r.Context())
	if err != nil {
		writePairingAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleListPairings(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pairings": []any{}})
		return
	}
	views, err := s.node.ListPairings(r.Context())
	if err != nil {
		writePairingAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": views})
}

func (s *Server) handleApprovePairing(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	view, err := s.node.ApprovePairing(r.Context(), r.PathValue("id"))
	if err != nil {
		writePairingAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCancelPairing(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	if err := s.node.CancelPairing(r.Context(), r.PathValue("id")); err != nil {
		writePairingAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartPairJoin(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPairRequestBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read pairing code")
		return
	}
	var req struct {
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
	}
	if err := json.Unmarshal(data, &req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a pairing code is required")
		return
	}
	view, err := s.node.StartPairJoin(r.Context(), req.Code, req.DeviceName)
	if err != nil {
		writePairingAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handlePairJoinStatus(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	view, err := s.node.PairJoinStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writePairingAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCancelPairJoin(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
		return
	}
	if err := s.node.CancelPairJoin(r.Context(), r.PathValue("id")); err != nil {
		writePairingAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writePairingAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, node.ErrPairingDisabled):
		writeError(w, http.StatusConflict, "node_disabled", "LAN pairing is disabled on this device")
	case errors.Is(err, node.ErrPairingNotFound), errors.Is(err, node.ErrPairJoinNotFound), errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, "pairing_not_found", "pairing not found")
	case errors.Is(err, node.ErrPairingExpired):
		writeError(w, http.StatusGone, "pairing_expired", "this pairing code has expired")
	case errors.Is(err, node.ErrPairingCanceled):
		writeError(w, http.StatusGone, "pairing_canceled", "this pairing was canceled")
	case errors.Is(err, node.ErrPairingState), errors.Is(err, account.ErrAlreadyExists), errors.Is(err, account.ErrCurrentDevice):
		writeError(w, http.StatusConflict, "pairing_conflict", err.Error())
	case errors.Is(err, account.ErrInvalidImport):
		writeError(w, http.StatusBadRequest, "invalid_pairing", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
