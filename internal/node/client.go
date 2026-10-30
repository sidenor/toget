package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/room"
)

const (
	maxNodeResponse = 32 << 20
	requestTimeout  = 20 * time.Second
)

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
		return fmt.Errorf("reach home node: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse+1))
	if err != nil {
		return fmt.Errorf("read node response: %w", err)
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
