package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/room"
)

const (
	// maxNodeResponse leaves room for a 25 MiB attachment after base64
	// expansion by the room ciphertext format.
	maxNodeResponse = 48 << 20
	requestTimeout  = 20 * time.Second
	relayTimeout    = 75 * time.Second
	// maxRelayResponse leaves room for a relayed 48 MiB node response after
	// the relay's base64 envelope framing.
	maxRelayResponse = relayproto.MaxResultWire
	probeTimeout     = 2500 * time.Millisecond
	// relayQueueLifetime is how long a relay may keep a request for an
	// offline node. It stays well below the client's 75-second relay
	// timeout, so a request is either answered or discarded before the
	// caller gives up; this avoids applying a write after the client already
	// saw a failure.
	relayQueueLifetime = 55 * time.Second
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

// Client talks to one pinned home node. When the node address includes a
// relay fallback, the client probes the direct LAN path first and relays only
// when it is unreachable.
type Client struct {
	endpoint  string
	accountID string
	deviceID  string
	key       ed25519.PrivateKey
	http      *http.Client

	relayBase   string
	relayTarget string
	relayHTTP   *http.Client
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
	return &Client{
		endpoint:  strings.TrimSuffix(endpoint, "/"),
		accountID: accountID,
		deviceID:  deviceID,
		key:       key,
		http:      pinnedHTTPClient(config, requestTimeout, requestTimeout),
	}, nil
}

func pinnedHTTPClient(config *tls.Config, timeout, headerTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       config,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// NewClientForURL builds a client from a URL containing #fp=<fingerprint>.
// The URL may also carry a relay route in its fragment.
func NewClientForURL(raw, accountID, deviceID string, key ed25519.PrivateKey) (*Client, error) {
	direct, relay := relayproto.SplitAddress(raw)
	endpoint, fingerprint, err := ParsePinnedURL(direct)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(endpoint, fingerprint, accountID, deviceID, key)
	if err != nil {
		return nil, err
	}
	if relay != "" {
		base, relayFP, target, err := relayproto.ParseNodeRoute(relay)
		if err != nil {
			return nil, err
		}
		config, err := PinnedTLSConfig(relayFP)
		if err != nil {
			return nil, err
		}
		client.relayBase, client.relayTarget = base, target
		client.relayHTTP = pinnedHTTPClient(config, relayTimeout, relayTimeout)
	}
	return client, nil
}

// Close releases idle connections.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
	if c != nil && c.relayHTTP != nil {
		c.relayHTTP.CloseIdleConnections()
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
	status, _, data, err := c.exchange(ctx, method, path, payload, contentTypeFor(body))
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return decodeRemoteError(status, data)
	}
	if out != nil {
		if len(data) == 0 {
			return fmt.Errorf("node returned an empty response (status %d)", status)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode node response: %w", err)
		}
	}
	return nil
}

func contentTypeFor(body any) string {
	if body == nil {
		return ""
	}
	return "application/json"
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

func (c *Client) doRaw(ctx context.Context, method, path string, payload []byte, contentType string, out any) error {
	status, _, data, err := c.exchange(ctx, method, path, payload, contentType)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return decodeRemoteError(status, data)
	}
	if out != nil {
		if len(data) == 0 {
			return fmt.Errorf("node returned an empty response (status %d)", status)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode node response: %w", err)
		}
	}
	return nil
}

func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	status, _, data, err := c.exchange(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, decodeRemoteError(status, data)
	}
	return data, nil
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

// exchange sends one signed request directly or through the relay fallback.
func (c *Client) exchange(ctx context.Context, method, path string, payload []byte, contentType string) (int, http.Header, []byte, error) {
	if c.relayBase != "" && !c.useDirect(ctx) {
		return c.relayExchange(ctx, method, path, payload, contentType)
	}
	status, headers, data, err := c.directExchange(ctx, method, path, payload, contentType)
	if err != nil && c.relayBase != "" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// The probe succeeded but the connection failed before a response.
		rememberDirect(c.endpoint, false, 30*time.Second)
	}
	return status, headers, data, err
}

func (c *Client) directExchange(ctx context.Context, method, path string, payload []byte, contentType string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build node request: %w", err)
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
		return 0, nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if len(data) > maxNodeResponse {
		return 0, nil, nil, fmt.Errorf("node response is too large")
	}
	return resp.StatusCode, resp.Header, data, nil
}

func (c *Client) relayExchange(ctx context.Context, method, path string, payload []byte, contentType string) (int, http.Header, []byte, error) {
	now := time.Now().UTC()
	inner, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build relayed request: %w", err)
	}
	inner.Header.Set("Accept", "application/json")
	if contentType != "" {
		inner.Header.Set("Content-Type", contentType)
	}
	if c.accountID != "" {
		inner.Header.Set(HeaderAccount, c.accountID)
	}
	if c.deviceID != "" {
		inner.Header.Set(HeaderDevice, c.deviceID)
	}
	if err := SignRequest(inner, payload, c.key, now); err != nil {
		return 0, nil, nil, err
	}
	envelope := relayproto.Envelope{
		Protocol: relayproto.Protocol, Target: c.relayTarget,
		Method: method, Path: inner.URL.RequestURI(),
		Headers:   relayproto.CopyInnerHeaders(inner.Header),
		Body:      relayproto.EncodeBody(payload),
		AccountID: c.accountID, DeviceID: c.deviceID,
		CreatedAt: now, ExpiresAt: now.Add(relayQueueLifetime),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("encode relay envelope: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.relayBase+relayproto.ForwardPrefix+url.PathEscape(c.relayTarget), bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build relay request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.accountID != "" {
		req.Header.Set(HeaderAccount, c.accountID)
	}
	if c.deviceID != "" {
		req.Header.Set(HeaderDevice, c.deviceID)
	}
	if err := SignRequest(req, encoded, c.key, now); err != nil {
		return 0, nil, nil, err
	}
	resp, err := c.relayHTTP.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: relay: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRelayResponse+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: relay: %v", ErrUnreachable, err)
	}
	if len(data) > maxRelayResponse {
		return 0, nil, nil, errors.New("relay response is too large")
	}
	if resp.StatusCode == http.StatusAccepted && resp.Header.Get(relayproto.HeaderRelayQueued) == "true" {
		var queued relayproto.Queued
		if err := json.Unmarshal(data, &queued); err != nil || queued.Ticket == "" {
			return 0, nil, nil, errors.New("relay returned an invalid queue ticket")
		}
		return c.pollRelayTicket(ctx, queued)
	}
	return resp.StatusCode, resp.Header, data, nil
}

func (c *Client) pollRelayTicket(ctx context.Context, queued relayproto.Queued) (int, http.Header, []byte, error) {
	deadline := time.Now().Add(2 * time.Minute)
	if !queued.ExpiresAt.IsZero() && queued.ExpiresAt.Before(deadline) {
		deadline = queued.ExpiresAt
	}
	path := "/relay/v1/responses/" + url.PathEscape(queued.Ticket)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return 0, nil, nil, fmt.Errorf("%w: relayed request is still queued", ErrUnreachable)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.relayBase+path, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		resp, err := c.relayHTTP.Do(req)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("%w: relay: %v", ErrUnreachable, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRelayResponse+1))
		resp.Body.Close()
		if readErr != nil {
			return 0, nil, nil, fmt.Errorf("%w: relay: %v", ErrUnreachable, readErr)
		}
		if len(data) > maxRelayResponse {
			return 0, nil, nil, errors.New("relay response is too large")
		}
		if resp.StatusCode == http.StatusAccepted && resp.Header.Get(relayproto.HeaderRelayQueued) == "true" {
			select {
			case <-ctx.Done():
				return 0, nil, nil, ctx.Err()
			case <-time.After(750 * time.Millisecond):
			}
			continue
		}
		return resp.StatusCode, resp.Header, data, nil
	}
}

type reachEntry struct {
	ok    bool
	until time.Time
}

var directReach sync.Map

func (c *Client) useDirect(ctx context.Context) bool {
	if c.relayBase == "" {
		return true
	}
	if value, ok := directReach.Load(c.endpoint); ok {
		entry := value.(reachEntry)
		if time.Now().Before(entry.until) {
			return entry.ok
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.endpoint+"/toget/v1/health", nil)
	if err != nil {
		rememberDirect(c.endpoint, false, 30*time.Second)
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		rememberDirect(c.endpoint, false, 60*time.Second)
		return false
	}
	resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK
	rememberDirect(c.endpoint, ok, 60*time.Second)
	return ok
}

func rememberDirect(endpoint string, ok bool, ttl time.Duration) {
	directReach.Store(endpoint, reachEntry{ok: ok, until: time.Now().Add(ttl)})
}

// LeaveRoom submits an already prepared and signed self-leave event.
func (c *Client) LeaveRoom(ctx context.Context, event *room.Event) error {
	return c.do(ctx, http.MethodPost, "/toget/v1/rooms/leave", eventRequest{Event: event.ToWire()}, nil)
}

// RequestPairing submits a new device's pairing request to the approving node.
func (c *Client) RequestPairing(ctx context.Context, request PairRequest) error {
	return c.do(ctx, http.MethodPost, "/toget/v1/pair/request", request, nil)
}

// CompletePairing polls a pairing until it is approved or ends. offset
// resumes a recovery bundle that reported HasMore.
func (c *Client) CompletePairing(ctx context.Context, pairingID string, offset int) (*PairingComplete, error) {
	var complete PairingComplete
	payload := map[string]any{"pairing_id": pairingID, "offset": offset}
	if err := c.do(ctx, http.MethodPost, "/toget/v1/pair/complete", payload, &complete); err != nil {
		return nil, err
	}
	return &complete, nil
}

// RegisterDevice asks a room's home node to admit a paired account device and
// share the room's epoch keys with it.
func (c *Client) RegisterDevice(ctx context.Context, roomID string, certificate []byte) (*room.RoomDevice, error) {
	var out struct {
		Device room.RoomDevice `json:"device"`
	}
	err := c.do(ctx, http.MethodPost, "/toget/v1/rooms/devices",
		map[string]any{"room_id": roomID, "certificate": string(certificate)}, &out)
	if err != nil {
		return nil, err
	}
	return &out.Device, nil
}

// RevokeDevice asks a room's home node to mark a device revoked and rotate the
// room epoch so the device cannot read later messages.
func (c *Client) RevokeDevice(ctx context.Context, roomID, deviceID string) (int, error) {
	var out struct {
		Epoch int `json:"epoch"`
	}
	err := c.do(ctx, http.MethodPost, "/toget/v1/rooms/devices/revoke",
		map[string]string{"room_id": roomID, "device_id": deviceID}, &out)
	if err != nil {
		return 0, err
	}
	return out.Epoch, nil
}
