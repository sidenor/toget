package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/sidenor/toget/internal/room"
)

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	items, err := s.rooms.ListAttachments(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": items})
}

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.Header.Get("X-Toget-Filename"))
	}
	mimeType := strings.TrimSpace(r.URL.Query().Get("mime"))
	if mimeType == "" {
		mimeType = strings.TrimSpace(r.Header.Get("Content-Type"))
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		if parsed := mime.TypeByExtension(extensionOf(name)); parsed != "" {
			mimeType = parsed
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, room.MaxAttachmentSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeRoomError(w, room.ErrAttachmentTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read attachment body")
		return
	}
	attachment, err := s.rooms.UploadAttachment(r.Context(), r.PathValue("id"), actor, name, mimeType, data)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, attachment)
}

func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	attachment, err := s.rooms.GetAttachment(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attachment)
}

func (s *Server) handleAttachmentContent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	attachment, data, err := s.rooms.OpenAttachment(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	w.Header().Set("Content-Type", attachment.MIME)
	w.Header().Set("Content-Length", itoa(int64(len(data))))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(attachment.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func extensionOf(name string) string {
	idx := strings.LastIndexByte(name, '.')
	if idx < 0 {
		return ""
	}
	return name[idx:]
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
