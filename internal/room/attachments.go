package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

const (
	// MaxAttachmentSize is the largest attachment accepted by a local node.
	MaxAttachmentSize = 25 << 20
	// MaxAttachmentsPerMessage bounds attachments referenced by one message.
	MaxAttachmentsPerMessage = 10
	maxAttachmentNameLength  = 255
)

var (
	// ErrAttachmentNotFound means the attachment id is unknown.
	ErrAttachmentNotFound = errors.New("attachment not found")
	// ErrAttachmentTooLarge means the attachment exceeds MaxAttachmentSize.
	ErrAttachmentTooLarge = errors.New("attachment is too large")
	// ErrInvalidAttachment means the attachment metadata or reference is invalid.
	ErrInvalidAttachment = errors.New("invalid attachment")
	// ErrAttachmentUsed means an attachment is already bound to a message.
	ErrAttachmentUsed = errors.New("attachment is already attached")
	// ErrAttachmentsDisabled means the node has no attachment directory.
	ErrAttachmentsDisabled = errors.New("attachment storage is not configured")
)

// Attachment is an encrypted file owned by a room.
type Attachment struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	EventID   string    `json:"event_id,omitempty"`
	Name      string    `json:"name"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Path      string    `json:"-"`
	KeySealed string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// WithDataDir enables encrypted attachment storage under dir.
func WithDataDir(dir string) Option {
	return func(s *Service) {
		s.dataDir = dir
	}
}

func (s *Service) attachmentPath(roomID, attachmentID string) (string, error) {
	if s.dataDir == "" {
		return "", ErrAttachmentsDisabled
	}
	return filepath.Join(s.dataDir, "attachments", roomID, attachmentID+".bin"), nil
}

// UploadAttachment encrypts and stores a file for a room. The blob is sealed
// with the room's current epoch key, so only members holding that key can read
// it.
func (s *Service) UploadAttachment(ctx context.Context, roomID string, actor Actor, name, mime string, data []byte) (*Attachment, error) {
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || len(name) > maxAttachmentNameLength || strings.ContainsRune(name, 0) {
		return nil, ErrInvalidAttachment
	}
	if len(data) == 0 {
		return nil, ErrInvalidAttachment
	}
	if len(data) > MaxAttachmentSize {
		return nil, ErrAttachmentTooLarge
	}
	mime = strings.TrimSpace(mime)
	if mime == "" {
		mime = "application/octet-stream"
	}
	if len(mime) > maxAttachmentNameLength {
		return nil, ErrInvalidAttachment
	}

	id := identity.NewID()
	purpose := "attachment:" + id
	epoch, sealed, err := s.SealBlob(ctx, roomID, purpose, data)
	if err != nil {
		return nil, err
	}
	path, err := s.attachmentPath(roomID, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}
	if err := writeFileAtomic(path, sealed); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	now := s.now().UTC().Truncate(time.Millisecond)
	attachment := &Attachment{
		ID:        id,
		RoomID:    roomID,
		Name:      name,
		MIME:      mime,
		Size:      int64(len(data)),
		SHA256:    hex.EncodeToString(sum[:]),
		Path:      path,
		KeySealed: strconv.Itoa(epoch),
		CreatedAt: now,
	}
	if err := s.store.CreateAttachment(ctx, attachment); err != nil {
		os.Remove(path)
		return nil, err
	}
	return attachment, nil
}

// GetAttachment returns metadata after checking room membership.
func (s *Service) GetAttachment(ctx context.Context, attachmentID string, actor Actor) (*Attachment, error) {
	a, err := s.store.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, err
	}
	if err := s.RequireMember(ctx, a.RoomID, actor); err != nil {
		return nil, err
	}
	return a, nil
}

// OpenAttachment decrypts an attachment after checking membership and
// verifying its digest and size.
func (s *Service) OpenAttachment(ctx context.Context, attachmentID string, actor Actor) (*Attachment, []byte, error) {
	a, err := s.GetAttachment(ctx, attachmentID, actor)
	if err != nil {
		return nil, nil, err
	}
	epoch, err := strconv.Atoi(a.KeySealed)
	if err != nil || epoch <= 0 {
		return nil, nil, ErrInvalidAttachment
	}
	sealed, err := os.ReadFile(a.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrAttachmentNotFound
		}
		return nil, nil, fmt.Errorf("read attachment: %w", err)
	}
	plaintext, err := s.OpenBlob(ctx, a.RoomID, epoch, "attachment:"+a.ID, sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("open attachment: %w", err)
	}
	sum := sha256.Sum256(plaintext)
	if hex.EncodeToString(sum[:]) != a.SHA256 || int64(len(plaintext)) != a.Size {
		return nil, nil, ErrInvalidAttachment
	}
	return a, plaintext, nil
}

// ListAttachments returns attachment metadata for a room.
func (s *Service) ListAttachments(ctx context.Context, roomID string, actor Actor) ([]Attachment, error) {
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	return s.store.ListAttachments(ctx, roomID)
}

// validateAttachmentRefs checks that every attachment is unbound and belongs
// to the room. It de-duplicates the list in place order.
func (s *Service) validateAttachmentRefs(ctx context.Context, roomID string, ids []string) ([]string, error) {
	if len(ids) > MaxAttachmentsPerMessage {
		return nil, ErrInvalidAttachment
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, ErrInvalidAttachment
		}
		seen[id] = true
		a, err := s.store.GetAttachment(ctx, id)
		if err != nil {
			if errors.Is(err, ErrAttachmentNotFound) {
				return nil, ErrInvalidAttachment
			}
			return nil, err
		}
		if a.RoomID != roomID {
			return nil, ErrInvalidAttachment
		}
		if a.EventID != "" {
			return nil, ErrAttachmentUsed
		}
		out = append(out, id)
	}
	return out, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return fmt.Errorf("create attachment file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set attachment mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write attachment: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync attachment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close attachment: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store attachment: %w", err)
	}
	return nil
}
