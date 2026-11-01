//go:build !js

package hosted

import (
	"errors"
	"net/http"
)

func (s *Service) handleVaultGet(w http.ResponseWriter, r *http.Request) {
	acct, _ := s.authenticate(w, r)
	if acct == nil {
		return
	}
	vault, err := s.store.GetVault(r.Context(), acct.ID)
	if errors.Is(err, ErrVaultNotFound) {
		writeJSON(w, http.StatusOK, VaultResponse{Protocol: Protocol})
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	blob := vault.Blob
	writeJSON(w, http.StatusOK, VaultResponse{
		Protocol: Protocol, Blob: &blob, Version: vault.Version, UpdatedAt: vault.UpdatedAt,
	})
}

func (s *Service) handleVaultPut(w http.ResponseWriter, r *http.Request) {
	acct, _ := s.authenticate(w, r)
	if acct == nil {
		return
	}
	var request VaultPutRequest
	if err := decodeJSON(w, r, &request); err != nil || request.Protocol != Protocol ||
		!validVaultBlob(request.Blob) || request.BaseVersion < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid vault")
		return
	}
	if len(request.Blob.Nonce) > 64 || len(request.Blob.Ciphertext) > MaxVaultSize {
		writeError(w, http.StatusRequestEntityTooLarge, "vault_too_large", "vault is too large")
		return
	}
	vault, err := s.store.PutVault(r.Context(), acct.ID, request.Blob, request.BaseVersion)
	if errors.Is(err, ErrVaultConflict) {
		writeError(w, http.StatusConflict, "vault_conflict", "vault version is stale")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, VaultPutResponse{
		Protocol: Protocol, Version: vault.Version, UpdatedAt: vault.UpdatedAt,
	})
}
