// Package relayproto defines the wire format shared by Toget nodes and
// relays. A relay forwards opaque, signed HTTP requests to a node behind a
// NAT. The relay never receives room keys or plaintext: the inner request is
// sealed and signed by the sending device, exactly as it would be on a direct
// LAN connection.
package relayproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Protocol is the relay wire protocol version.
	Protocol = 1
	// RoutePrefix is the address shape a client stores for a relayed node.
	RoutePrefix = "/relay/v1/node/"
	// ForwardPrefix receives one opaque envelope.
	ForwardPrefix = "/relay/v1/forward/"
	// RelayForwardPrefix receives an envelope signed by the immediate child
	// relay. The parent verifies the signature and route before forwarding.
	RelayForwardPrefix = "/relay/v1/relay-forward/"
	// RelayIDPrefix marks a self-certifying relay identity.
	RelayIDPrefix = "togr1:"

	// MaxHops bounds a relay chain.
	MaxHops = 8
	// MaxPath bounds an inner request target.
	MaxPath = 8 << 10
	// MaxTarget bounds a server or relay identifier.
	MaxTarget = 256
	// MaxHeaders bounds forwarded header count.
	MaxHeaders = 32
	// MaxHeaderBytes bounds forwarded header material.
	MaxHeaderBytes = 16 << 10
	// MaxEnvelopeWire bounds one envelope on the wire after base64.
	MaxEnvelopeWire = 72 << 20
	// MaxResultWire bounds one result on the wire after base64.
	MaxResultWire = 72 << 20
	// MaxInnerBody is the largest decoded inner request body a relay accepts.
	// It leaves room for a sealed 25 MiB attachment plus JSON and base64 cost.
	MaxInnerBody = 52 << 20

	// DefaultQueueTTL is how long an offline target may keep a queued request.
	DefaultQueueTTL = 30 * time.Minute
	// DefaultResponseTTL is how long a final response stays available for a
	// client that received a queued ticket.
	DefaultResponseTTL = 10 * time.Minute
	// DefaultSessionTTL is how long a node may go without polling.
	DefaultSessionTTL = 90 * time.Second
	// DefaultWaitTimeout bounds one online relayed request.
	DefaultWaitTimeout = 40 * time.Second
	// DefaultPollWait is the long-poll window used by nodes and relays.
	DefaultPollWait = 25 * time.Second
)

// Header names used by the relay transport itself. Inner signed headers are
// copied verbatim so the home node can verify the original device signature.
const (
	HeaderRelayQueued   = "X-Toget-Relay-Queued"
	HeaderRelayedBy     = "X-Toget-Relayed-By"
	HeaderRelayHops     = "X-Toget-Relay-Hops"
	HeaderRelayTicket   = "X-Toget-Relay-Ticket"
	HeaderRelayFinal    = "X-Toget-Relay-Final"
	HeaderAuthorization = "Authorization"
)

// InnerHeaders is the allowlist copied through the relay. Everything the
// home node's request verifier needs is included; nothing else is trusted.
var innerHeaders = map[string]bool{
	"Accept":            true,
	"Content-Type":      true,
	"X-Toget-Account":   true,
	"X-Toget-Device":    true,
	"X-Toget-Timestamp": true,
	"X-Toget-Nonce":     true,
	"X-Toget-Signature": true,
}

// Envelope is one signed HTTP request in transit. Body is base64 so the
// envelope stays JSON. Hops records relay IDs that have handled the request.
type Envelope struct {
	Protocol  int               `json:"protocol"`
	RequestID string            `json:"request_id"`
	Target    string            `json:"target"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	AccountID string            `json:"account_id,omitempty"`
	DeviceID  string            `json:"device_id,omitempty"`
	Hops      []string          `json:"hops,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
	// ForwarderID, ForwarderKey, ForwardedAt, ForwardNonce, and
	// ForwardSignature authenticate one relay-to-relay hop. A parent verifies
	// the immediate child's self-certifying relay ID and signature before
	// accepting a request on the relay-forward endpoint. End devices leave
	// these fields empty; their request is authenticated by the inner node
	// signature instead.
	ForwarderID      string `json:"forwarder_id,omitempty"`
	ForwarderKey     string `json:"forwarder_key,omitempty"`
	ForwardedAt      int64  `json:"forwarded_at,omitempty"`
	ForwardNonce     string `json:"forward_nonce,omitempty"`
	ForwardSignature string `json:"forward_signature,omitempty"`
}

// Result is the home node's or downstream relay's answer. Status, Headers,
// and Body mirror the inner HTTP response. Code and Message are set only when
// the relay itself generated an error.
type Result struct {
	Protocol  int               `json:"protocol"`
	RequestID string            `json:"request_id"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Code      string            `json:"code,omitempty"`
	Message   string            `json:"message,omitempty"`
	RelayedBy string            `json:"relayed_by,omitempty"`
}

// Queued is returned to a forwarding client when the target is offline and
// the relay stored the envelope.
type Queued struct {
	Queued    bool      `json:"queued"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WebSocket tunnel message types. A node or relay registers over HTTPS, then
// opens /relay/v1/ws with its bearer token. The relay pushes envelopes as
// "frame" messages and the client posts results as "result" messages. The
// HTTPS long-poll endpoints remain available as a fallback.
const (
	TunnelHello  = "hello"
	TunnelFrame  = "frame"
	TunnelResult = "result"
	TunnelPing   = "ping"
	TunnelPong   = "pong"
	TunnelError  = "error"
)

// TunnelMessage is one JSON WebSocket tunnel message. Exactly one of
// Envelope or Result is set for frame/result messages.
type TunnelMessage struct {
	Protocol   int       `json:"protocol"`
	Type       string    `json:"type"`
	Envelope   *Envelope `json:"envelope,omitempty"`
	Result     *Result   `json:"result,omitempty"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message,omitempty"`
	ServerTime time.Time `json:"server_time,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

// RegisterRequest is signed by the node's server key (kind node) or by the
// relay's own Ed25519 key (kind relay). Manifests are embedded verbatim so
// the receiver can verify them independently of the sender's claim.
type RegisterRequest struct {
	Protocol         int             `json:"protocol"`
	Kind             string          `json:"kind"`
	ServerID         string          `json:"server_id,omitempty"`
	ServerCode       string          `json:"server_code,omitempty"`
	IdentityManifest json.RawMessage `json:"identity_manifest,omitempty"`
	NodeManifest     json.RawMessage `json:"node_manifest,omitempty"`
	RelayID          string          `json:"relay_id,omitempty"`
	PublicKey        string          `json:"public_key,omitempty"`
	Timestamp        int64           `json:"timestamp"`
	Nonce            string          `json:"nonce"`
	Signature        string          `json:"signature,omitempty"`
}

// RegisterResponse admits a node or relay session.
type RegisterResponse struct {
	Protocol  int       `json:"protocol"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	RelayID   string    `json:"relay_id,omitempty"`
	RelayURL  string    `json:"relay_url,omitempty"`
	Parent    bool      `json:"parent,omitempty"`
}

// PollResponse carries zero or more envelopes to a polling node or relay.
type PollResponse struct {
	Protocol   int        `json:"protocol"`
	Frames     []Envelope `json:"frames"`
	ServerTime time.Time  `json:"server_time"`
}

// RouteAnnouncement tells a parent relay that a target is reachable through
// the announcing child. Via starts with the child's relay ID and lists every
// relay on the path, excluding the target node.
type RouteAnnouncement struct {
	Target string   `json:"target"`
	Kind   string   `json:"kind"`
	Hops   int      `json:"hops"`
	Via    []string `json:"via,omitempty"`
}

// RoutesRequest is authenticated with the child relay's session token.
type RoutesRequest struct {
	Protocol  int                 `json:"protocol"`
	Routes    []RouteAnnouncement `json:"routes"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Info is the public relay description.
type Info struct {
	Protocol       int       `json:"protocol"`
	RelayID        string    `json:"relay_id"`
	URL            string    `json:"url"`
	TLSFingerprint string    `json:"tls_fingerprint"`
	Parent         bool      `json:"parent"`
	Sessions       int       `json:"sessions"`
	Routes         int       `json:"routes"`
	Queued         int       `json:"queued"`
	Tunnels        int       `json:"tunnels"`
	ServerTime     time.Time `json:"server_time"`
}

// TargetInfo answers a route probe for one server ID.
type TargetInfo struct {
	Protocol   int       `json:"protocol"`
	Target     string    `json:"target"`
	Online     bool      `json:"online"`
	RelayID    string    `json:"relay_id"`
	RelayURL   string    `json:"relay_url"`
	ServerTime time.Time `json:"server_time"`
}

// ErrorBody is the relay's JSON error shape, matching the node API.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewRequestID returns a random 128-bit URL-safe request identifier.
func NewRequestID() string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// NewToken returns a random bearer token.
func NewToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return NewRequestID() + NewRequestID()
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// NewNonce returns a random registration nonce.
func NewNonce() string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// RegistrationPayload returns the canonical bytes signed by a registrant.
func RegistrationPayload(req RegisterRequest) ([]byte, error) {
	req.Signature = ""
	return json.Marshal(req)
}

// SignRegistration fills Timestamp, Nonce, and Signature.
func SignRegistration(req *RegisterRequest, key ed25519.PrivateKey, now time.Time) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("registration key is invalid")
	}
	req.Protocol = Protocol
	req.Timestamp = now.UTC().Unix()
	req.Nonce = NewNonce()
	req.Signature = ""
	payload, err := RegistrationPayload(*req)
	if err != nil {
		return err
	}
	req.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

// VerifyRegistration checks the timestamp and signature over the registration.
// The caller still verifies the manifests or the self-certifying relay ID.
func VerifyRegistration(req RegisterRequest, publicKey ed25519.PublicKey, now time.Time, skew time.Duration) error {
	if req.Protocol != Protocol {
		return errors.New("unsupported relay protocol")
	}
	if req.Timestamp == 0 || req.Nonce == "" || req.Signature == "" {
		return errors.New("registration is incomplete")
	}
	signedAt := time.Unix(req.Timestamp, 0).UTC()
	if now.UTC().Sub(signedAt) > skew || signedAt.Sub(now.UTC()) > skew {
		return errors.New("registration timestamp is outside the allowed window")
	}
	signature, err := base64.RawStdEncoding.DecodeString(req.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("registration signature is malformed")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("registration key is invalid")
	}
	payload, err := RegistrationPayload(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("registration signature is invalid")
	}
	return nil
}

// ForwardPayload returns the canonical bytes signed by the immediate relay.
func ForwardPayload(env Envelope) ([]byte, error) {
	env.ForwardSignature = ""
	return json.Marshal(env)
}

// SignForward authenticates an envelope for one relay-to-relay hop. The
// caller must already have appended its own relay ID to Hops.
func SignForward(env *Envelope, key ed25519.PrivateKey, now time.Time) error {
	if env == nil {
		return errors.New("envelope is required")
	}
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("relay forwarding key is invalid")
	}
	public := key.Public().(ed25519.PublicKey)
	env.ForwarderID = RelayID(public)
	env.ForwarderKey = base64.RawStdEncoding.EncodeToString(public)
	env.ForwardedAt = now.UTC().Unix()
	env.ForwardNonce = NewNonce()
	env.ForwardSignature = ""
	payload, err := ForwardPayload(*env)
	if err != nil {
		return err
	}
	env.ForwardSignature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

// VerifyForward checks a relay-to-relay signature and returns the verified
// public key. It does not authorize the relay to reach a particular target;
// the receiver must also match ForwarderID against an authenticated route.
func VerifyForward(env *Envelope, now time.Time, skew time.Duration) (ed25519.PublicKey, error) {
	if env == nil || env.ForwarderID == "" || env.ForwarderKey == "" ||
		env.ForwardedAt == 0 || env.ForwardNonce == "" || env.ForwardSignature == "" {
		return nil, errors.New("relay forwarding signature is missing")
	}
	rawKey, err := base64.RawStdEncoding.DecodeString(env.ForwarderKey)
	if err != nil || len(rawKey) != ed25519.PublicKeySize {
		return nil, errors.New("relay forwarding key is malformed")
	}
	public := ed25519.PublicKey(rawKey)
	if RelayID(public) != env.ForwarderID {
		return nil, errors.New("relay forwarding ID does not match its key")
	}
	if len(env.ForwardNonce) < 16 || len(env.ForwardNonce) > 128 {
		return nil, errors.New("relay forwarding nonce is invalid")
	}
	signedAt := time.Unix(env.ForwardedAt, 0).UTC()
	now = now.UTC()
	if signedAt.Before(now.Add(-skew)) || signedAt.After(now.Add(skew)) {
		return nil, errors.New("relay forwarding timestamp is outside the allowed window")
	}
	signature, err := base64.RawStdEncoding.DecodeString(env.ForwardSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("relay forwarding signature is malformed")
	}
	payload, err := ForwardPayload(*env)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(public, payload, signature) {
		return nil, errors.New("relay forwarding signature is invalid")
	}
	return public, nil
}

// RelayID returns the self-certifying ID for a relay public key.
func RelayID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return RelayIDPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidateEnvelope rejects malformed or dangerous inner requests before a
// relay queues or forwards them.
func ValidateEnvelope(env *Envelope) error {
	if env == nil || env.Target == "" || env.Path == "" {
		return errors.New("envelope is incomplete")
	}
	if len(env.Target) > MaxTarget || len(env.Path) > MaxPath ||
		len(env.AccountID) > MaxTarget || len(env.DeviceID) > MaxTarget ||
		len(env.ForwarderID) > MaxTarget || len(env.ForwarderKey) > 256 ||
		len(env.ForwardNonce) > 128 || len(env.ForwardSignature) > 256 {
		return errors.New("envelope fields are too large")
	}
	switch env.Method {
	case "GET", "POST":
	default:
		return errors.New("envelope method is not supported")
	}
	if !strings.HasPrefix(env.Path, "/toget/v1/") {
		return errors.New("envelope path is outside the node API")
	}
	if strings.ContainsAny(env.Path, "\r\n\x00") {
		return errors.New("envelope path is invalid")
	}
	if len(env.Headers) > MaxHeaders {
		return errors.New("too many envelope headers")
	}
	total := 0
	for name, value := range env.Headers {
		if !innerHeaders[name] || strings.ContainsAny(name+value, "\r\n\x00") {
			return fmt.Errorf("envelope header %q is not allowed", name)
		}
		total += len(name) + len(value)
	}
	if total > MaxHeaderBytes {
		return errors.New("envelope headers are too large")
	}
	if len(env.Body) > base64.StdEncoding.EncodedLen(MaxInnerBody) {
		return errors.New("envelope body is too large")
	}
	if len(env.Hops) > MaxHops {
		return errors.New("relay hop limit exceeded")
	}
	seen := make(map[string]bool, len(env.Hops))
	for _, hop := range env.Hops {
		if hop == "" || seen[hop] {
			return errors.New("envelope relay path is invalid")
		}
		seen[hop] = true
	}
	return nil
}

// HasHop reports whether a relay ID already appears in an envelope path.
func HasHop(env *Envelope, relayID string) bool {
	for _, hop := range env.Hops {
		if hop == relayID {
			return true
		}
	}
	return false
}

// EncodeBody base64-encodes an inner body.
func EncodeBody(body []byte) string {
	return base64.StdEncoding.EncodeToString(body)
}

// DecodeBody decodes an inner body with a hard size limit.
func DecodeBody(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(MaxInnerBody) {
		return nil, errors.New("inner body is too large")
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("inner body is not valid base64")
	}
	if len(body) > MaxInnerBody {
		return nil, errors.New("inner body is too large")
	}
	return body, nil
}

// CopyInnerHeaders builds the allowlisted header map for an envelope.
func CopyInnerHeaders(header map[string][]string) map[string]string {
	out := make(map[string]string)
	for name, values := range header {
		canonical := http.CanonicalHeaderKey(name)
		if !innerHeaders[canonical] || len(values) == 0 {
			continue
		}
		out[canonical] = values[0]
	}
	return out
}

// NodeRouteURL returns the pinned address clients store for a relayed node.
// relayBase is an HTTPS origin such as https://relay.example:7719.
func NodeRouteURL(relayBase, fingerprint, target string) (string, error) {
	base, err := parseRelayBase(relayBase)
	if err != nil {
		return "", err
	}
	fingerprint, err = ParseFingerprint(fingerprint)
	if err != nil {
		return "", err
	}
	if target == "" || len(target) > MaxTarget || strings.ContainsAny(target, "/?#") {
		return "", errors.New("relay target is invalid")
	}
	return base + RoutePrefix + url.PathEscape(target) + "#fp=" + fingerprint, nil
}

// ParseNodeRoute splits a relayed node address into its relay origin, pin,
// and target server ID.
func ParseNodeRoute(raw string) (base, fingerprint, target string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", "", "", errors.New("relay address must be an HTTPS URL")
	}
	values, _ := url.ParseQuery(parsed.Fragment)
	fingerprint = values.Get("fp")
	if fingerprint == "" {
		fingerprint = values.Get("sha256")
	}
	fingerprint, err = ParseFingerprint(fingerprint)
	if err != nil {
		return "", "", "", err
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if !strings.HasPrefix(path, RoutePrefix) {
		return "", "", "", errors.New("relay address has an unknown path")
	}
	target, err = url.PathUnescape(strings.TrimPrefix(path, RoutePrefix))
	if err != nil || target == "" || strings.Contains(target, "/") || len(target) > MaxTarget {
		return "", "", "", errors.New("relay address target is invalid")
	}
	base = parsed.Scheme + "://" + parsed.Host
	return base, fingerprint, target, nil
}

// JoinAddress attaches a relay route to a direct pinned node URL as a
// fragment parameter. A client tries the direct address first and falls back
// to the relay when the LAN path is unreachable.
func JoinAddress(direct, relay string) string {
	if relay == "" {
		return direct
	}
	return direct + "&relay=" + url.QueryEscape(relay)
}

// SplitAddress separates a composite node address into its direct pinned URL
// and optional relay route. The direct URL is returned untouched so existing
// pinned URL parsing remains the authority for the LAN endpoint.
func SplitAddress(raw string) (direct, relay string) {
	direct = strings.TrimSpace(raw)
	parsed, err := url.Parse(direct)
	if err != nil {
		return direct, ""
	}
	values, _ := url.ParseQuery(parsed.Fragment)
	relay = values.Get("relay")
	if relay == "" {
		return direct, ""
	}
	if _, _, _, err := ParseNodeRoute(relay); err != nil {
		return direct, ""
	}
	return direct, relay
}

// ParseFingerprint validates a SHA-256 certificate fingerprint.
func ParseFingerprint(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "sha256:")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("invalid relay TLS certificate fingerprint")
	}
	return hex.EncodeToString(decoded), nil
}

func parseRelayBase(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("relay base must be an HTTPS URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("relay base must not contain a path, query, or fragment")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
