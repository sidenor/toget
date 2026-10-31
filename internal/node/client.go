package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/room"
)

const (
	// maxNodeResponse leaves room for a 25 MiB attachment after base64
	// expansion by the room ciphertext format.
	maxNodeResponse = 48 << 20
	requestTimeout  = 20 * time.Second
)

// ErrUnreachable means the home node could not be reached over the network.
var ErrUnreachable = errors.New("home node is unreachable")

// RemoteError is a structured error returned by a node.
type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RemoteError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("node request failed (%d)", e.Status)
	}
	return e.Message
}

// Client talks to one pinned home node.
type Client struct {
	endpoint  string
	accountID string
	deviceID  string
	key       ed25519.PrivateKey
	http      *http.Client
}

// NewClient creates a pinned node client. endpoint must be an HTTPS origin
// such as https://192.168.1.4:7717.
func NewClient(endpoint, fingerprint, accountID, deviceID string, key ed25519.PrivateKey) (*Client, error) {
	fingerprint, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	config, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       config,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		endpoint:  strings.TrimSuffix(endpoint, "/"),
		accountID: accountID,
		deviceID:  deviceID,
		key:       key,
		http:      &http.Client{Transport: transport, Timeout: requestTimeout},
	}, nil
}

// NewClientForURL builds a client from a URL containing #fp=<fingerprint>.
func NewClientForURL(raw, accountID, deviceID string, key ed25519.PrivateKey) (*Client, error) {
	endpoint, fingerprint, err := ParsePinnedURL(raw)
	if err != nil {
		return nil, err
	}
	return NewClient(endpoint, fingerprint, accountID, deviceID, key)
}

// Close releases idle connections.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

// Info fetches the home node's signed manifest.
func (c *Client) Info(ctx context.Context) (*Manifest, error) {
	var manifest Manifest
	if err := c.do(ctx, http.MethodGet, "/toget/v1/info", nil, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// RedeemRequest asks the home node to admit a joined device.
type RedeemRequest struct {
	Secret string            `json:"secret"`
	Joiner room.RemoteJoiner `json:"joiner"`
}

// Redeem joins a room and returns its bundle.
func (c *Client) Redeem(ctx context.Context, secret string, joiner room.RemoteJoiner) (*room.JoinBundle, error) {
	var bundle room.JoinBundle
	err := c.do(ctx, http.MethodPost, "/toget/v1/invites/redeem", RedeemRequest{Secret: secret, Joiner: joiner}, &bundle)
	if err != nil {
		return nil, err
	}
	return &bundle, nil
}

type syncRequest struct {
	RoomID string `json:"room_id"`
	After  int64  `json:"after"`
}

// Sync fetches events after a local cursor.
func (c *Client) Sync(ctx context.Context, roomID string, after int64) (*room.SyncBundle, error) {
	var bundle room.SyncBundle
	err := c.do(ctx, http.MethodPost, "/toget/v1/rooms/sync", syncRequest{RoomID: roomID, After: after}, &bundle)
	if err != nil {
		return nil, err
	}
	return &bundle, nil
}

type eventRequest struct {
	Event room.WireEvent `json:"event"`
}

// PostEvent submits a sealed, signed event to its home node.
func (c *Client) PostEvent(ctx context.Context, event *room.Event) error {
	return c.do(ctx, http.MethodPost, "/toget/v1/rooms/events", eventRequest{Event: event.ToWire()}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode node request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build node request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.accountID != "" {
		req.Header.Set(HeaderAccount, c.accountID)
	}
	if c.deviceID != "" {
		req.Header.Set(HeaderDevice, c.deviceID)
	}
	if err := SignRequest(req, payload, c.key, time.Now()); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if len(data) > maxNodeResponse {
		return fmt.Errorf("node response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeRemoteError(resp.StatusCode, data)
	}
	if out != nil {
		if len(data) == 0 {
			return fmt.Errorf("node returned an empty response (status %d)", resp.StatusCode)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode node response: %w", err)
		}
	}
	return nil
}

func decodeRemoteError(status int, data []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	message := envelope.Error.Message
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return &RemoteError{Status: status, Code: envelope.Error.Code, Message: message}
}

// UploadAttachment posts sealed attachment ciphertext to a remote room's home
// node. Metadata travels in the signed query string; the body stays opaque.
func (c *Client) UploadAttachment(ctx context.Context, roomID string, a room.WireAttachment, sealed []byte) (*room.WireAttachment, error) {
	values := url.Values{}
	values.Set("room_id", roomID)
	values.Set("id", a.ID)
	values.Set("name", a.Name)
	values.Set("mime", a.MIME)
	values.Set("size", strconv.FormatInt(a.Size, 10))
	values.Set("sha256", a.SHA256)
	values.Set("epoch", strconv.Itoa(a.Epoch))
	path := "/toget/v1/rooms/attachments?" + values.Encode()
	var out room.WireAttachment
	if err := c.doRaw(ctx, http.MethodPost, path, sealed, "application/octet-stream", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchAttachment downloads sealed attachment ciphertext from a home node.
func (c *Client) FetchAttachment(ctx context.Context, roomID, attachmentID string) ([]byte, error) {
	values := url.Values{}
	values.Set("room_id", roomID)
	path := "/toget/v1/attachments/" + url.PathEscape(attachmentID) + "?" + values.Encode()
	return c.getRaw(ctx, path)
}

func (c *Client) doRaw(ctx context.Context, method, path string, payload []byte, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build node request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.accountID != "" {
		req.Header.Set(HeaderAccount, c.accountID)
	}
	if c.deviceID != "" {
		req.Header.Set(HeaderDevice, c.deviceID)
	}
	if err := SignRequest(req, payload, c.key, time.Now()); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if len(data) > maxNodeResponse {
		return fmt.Errorf("node response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeRemoteError(resp.StatusCode, data)
	}
	if out != nil {
		if len(data) == 0 {
			return fmt.Errorf("node returned an empty response (status %d)", resp.StatusCode)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode node response: %w", err)
		}
	}
	return nil
}

func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build node request: %w", err)
	}
	if c.accountID != "" {
		req.Header.Set(HeaderAccount, c.accountID)
	}
	if c.deviceID != "" {
		req.Header.Set(HeaderDevice, c.deviceID)
	}
	if err := SignRequest(req, nil, c.key, time.Now()); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if len(data) > maxNodeResponse {
		return nil, fmt.Errorf("node response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeRemoteError(resp.StatusCode, data)
	}
	return data, nil
}

// LeaveRoom submits an already prepared and signed self-leave event.
func (c *Client) LeaveRoom(ctx context.Context, event *room.Event) error {
	return c.do(ctx, http.MethodPost, "/toget/v1/rooms/leave", eventRequest{Event: event.ToWire()}, nil)
}
