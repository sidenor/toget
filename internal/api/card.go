package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/qr"
)

type cardResponse struct {
	Card *node.CardView `json:"card"`
}

type cardsResponse struct {
	Cards []node.CardView `json:"cards"`
}

type addCardRequest struct {
	Card  string `json:"card"`
	Label string `json:"label"`
}

func (s *Server) handleGetCard(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the node transport is disabled")
		return
	}
	view, err := s.node.OwnCard(r.Context(), r.URL.Query().Get("label"))
	if err != nil {
		writeCardError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cardResponse{Card: view})
}

func (s *Server) handleCardQR(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the node transport is disabled")
		return
	}
	scale := 8
	if raw := r.URL.Query().Get("scale"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2 || parsed > 32 {
			writeError(w, http.StatusBadRequest, "invalid_scale", "scale must be between 2 and 32")
			return
		}
		scale = parsed
	}
	view, err := s.node.OwnCard(r.Context(), r.URL.Query().Get("label"))
	if err != nil {
		writeCardError(w, err)
		return
	}
	png, err := qr.PNG(view.Card, scale)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "card_too_large", "the card does not fit in a QR code")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `inline; filename="toget-card.png"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) handleListCards(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeJSON(w, http.StatusOK, cardsResponse{Cards: []node.CardView{}})
		return
	}
	cards := s.node.Cards()
	if cards == nil {
		cards = []node.CardView{}
	}
	writeJSON(w, http.StatusOK, cardsResponse{Cards: cards})
}

func (s *Server) handleAddCard(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the node transport is disabled")
		return
	}
	var input addCardRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.Card) == "" {
		writeError(w, http.StatusBadRequest, "invalid_card", "a card is required")
		return
	}
	view, err := s.node.AddCard(r.Context(), input.Card, input.Label)
	if err != nil {
		writeCardError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cardResponse{Card: view})
}

func (s *Server) handleRemoveCard(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the node transport is disabled")
		return
	}
	err := s.node.RemoveCard(r.PathValue("id"))
	if errors.Is(err, node.ErrCardNotFound) {
		writeError(w, http.StatusNotFound, "card_not_found", "saved card was not found")
		return
	}
	if err != nil {
		writeCardError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeCardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, node.ErrCardNotFound):
		writeError(w, http.StatusNotFound, "card_not_found", "saved card was not found")
	case errors.Is(err, node.ErrInvalidCardLabel):
		writeError(w, http.StatusBadRequest, "invalid_label", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "invalid_card", err.Error())
	}
}
