package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// decodeStrict rejects unknown fields and trailing data. Signed release
// documents are versioned, so a new field requires a new document version
// rather than being silently ignored by an old client.
func decodeStrict(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func decodeBase64(value string) ([]byte, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return nil, errors.New("empty base64 value")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(s)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("invalid base64 value: %w", lastErr)
}

func encodeBase64(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func parseTime(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is empty", field)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is not RFC 3339: %w", field, err)
	}
	return parsed.UTC(), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func marshalDocument(value any) ([]byte, error) {
	return json.Marshal(value)
}
