// Package card implements portable Toget server cards: signed identity data
// that can be copied as text or rendered as a QR code. A card is only a
// discovery hint; the receiver verifies every embedded manifest before using
// it and re-verifies the live node before connecting.
package card

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Prefix identifies a portable card string.
	Prefix = "toget-card:v1:"
	// Format identifies the card container.
	Format = "toget-card"
	// Version is the current card container version.
	Version = 1

	maxLabelRunes   = 128
	maxEncodedBytes = 32 << 10
	maxJSONBytes    = 128 << 10
)

// Card is a portable, self-contained description of one node. Identity and
// Node hold the signed manifests exactly as they were issued.
type Card struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Label    string          `json:"label,omitempty"`
	Identity json.RawMessage `json:"identity"`
	Node     json.RawMessage `json:"node"`
}

// New builds a card. The caller is responsible for supplying signed manifests.
func New(label string, identity, node json.RawMessage) (*Card, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxLabelRunes {
		return nil, errors.New("card label is too long")
	}
	if !json.Valid(identity) || !json.Valid(node) {
		return nil, errors.New("card manifests must be valid JSON")
	}
	return &Card{Format: Format, Version: Version, Label: label, Identity: identity, Node: node}, nil
}

// Encode returns the portable text form. The JSON container is gzip-compressed
// before base64 encoding so a card fits in a QR code at a useful error
// correction level.
func (c *Card) Encode() (string, error) {
	if c == nil {
		return "", errors.New("card is nil")
	}
	if c.Format != Format || c.Version != Version {
		return "", errors.New("unsupported card format")
	}
	if !json.Valid(c.Identity) || !json.Valid(c.Node) {
		return "", errors.New("card manifests must be valid JSON")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if len(data) > maxJSONBytes {
		return "", errors.New("card is too large")
	}
	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write(data); err != nil {
		zw.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	encoded := Prefix + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	if len(encoded) > maxEncodedBytes {
		return "", errors.New("card is too large")
	}
	return encoded, nil
}

// Decode parses a portable card. It only checks the container; signature and
// identity verification belong to the node package.
func Decode(raw string) (*Card, error) {
	raw = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	if raw == "" {
		return nil, errors.New("card is empty")
	}
	if !strings.HasPrefix(raw, Prefix) {
		return nil, fmt.Errorf("card must start with %q", Prefix)
	}
	if len(raw) > maxEncodedBytes {
		return nil, errors.New("card is too large")
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, Prefix))
	if err != nil {
		return nil, errors.New("card encoding is invalid")
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, errors.New("card compression is invalid")
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, maxJSONBytes+1))
	if err != nil {
		return nil, errors.New("card compression is invalid")
	}
	if len(data) > maxJSONBytes {
		return nil, errors.New("card is too large")
	}
	var decoded Card
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, errors.New("card JSON is invalid")
	}
	if decoded.Format != Format || decoded.Version != Version {
		return nil, errors.New("unsupported card format")
	}
	if !json.Valid(decoded.Identity) || !json.Valid(decoded.Node) {
		return nil, errors.New("card manifests must be valid JSON")
	}
	if utf8.RuneCountInString(decoded.Label) > maxLabelRunes {
		return nil, errors.New("card label is too long")
	}
	if _, err := json.Marshal(decoded.Identity); err != nil {
		return nil, errors.New("card identity is invalid")
	}
	if _, err := json.Marshal(decoded.Node); err != nil {
		return nil, errors.New("card node manifest is invalid")
	}
	return &decoded, nil
}

// Entry is a stored card. The portable text is kept verbatim so it can be
// re-verified when the card is used.
type Entry struct {
	ID         string    `json:"id"`
	Label      string    `json:"label,omitempty"`
	ServerCode string    `json:"server_code"`
	AccountID  string    `json:"account_id"`
	Card       string    `json:"card"`
	AddedAt    time.Time `json:"added_at"`
}
