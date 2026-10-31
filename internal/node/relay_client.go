package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/quicclient"
	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/relaytunnel"
)

const (
	relayPollWire       = 4*relayproto.MaxEnvelopeWire + (1 << 20)
	relaySessionRefresh = 2 * time.Minute
)

var (
	errRelayConfigChanged = errors.New("relay configuration changed")
	errRelayTunnelRefresh = errors.New("relay tunnel token refresh")
)

type relayConn struct {
	svc         *Service
	base        string
	fingerprint string
	client      *http.Client
	transport   *quicclient.Transport
	wsClient    *http.Client
	token       string
	expires     time.Time
	relayID     string
}

func newRelayConn(s *Service, base, fingerprint string) (*relayConn, error) {
	config, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	client := pinnedHTTPClient(config, relayTimeout, relayTimeout)
	return &relayConn{
		svc: s, base: base, fingerprint: fingerprint,
		client:    client,
		transport: quicclient.New(base, "/relay/v1/health", config, client, relayTimeout, probeTimeout),
		wsClient:  pinnedTunnelHTTPClient(config),
	}, nil
}

func (c *relayConn) close() {
	if c == nil {
		return
	}
	if c.transport != nil {
		c.transport.Close()
	}
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
	if c.wsClient != nil {
		c.wsClient.CloseIdleConnections()
	}
}

func (c *relayConn) register(ctx context.Context) error {
	snapshot, err := c.svc.accounts.Get(ctx)
	if err != nil {
		return err
	}
	if snapshot.Server == nil {
		return errors.New("server identity is not ready")
	}
	serverKey, err := c.svc.accounts.UnsealServerKey(snapshot.Server)
	if err != nil {
		return err
	}
	manifestJSON, err := c.svc.manifestJSON(ctx)
	if err != nil {
		return err
	}
	req := relayproto.RegisterRequest{
		Kind: "node", ServerID: snapshot.Server.ID, ServerCode: snapshot.Server.Code,
		IdentityManifest: json.RawMessage(snapshot.Server.Manifest),
		NodeManifest:     json.RawMessage(manifestJSON),
	}
	if err := relayproto.SignRegistration(&req, serverKey, time.Now()); err != nil {
		return err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, data, err := c.request(ctx, http.MethodPost, "/relay/v1/register", payload, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay registration failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out relayproto.RegisterResponse
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return errors.New("relay registration returned no token")
	}
	if out.Protocol != 0 && out.Protocol != relayproto.Protocol {
		return errors.New("relay uses an unsupported protocol")
	}
	c.token = out.Token
	c.expires = out.ExpiresAt
	c.relayID = out.RelayID
	if c.expires.IsZero() || time.Until(c.expires) < time.Minute {
		c.expires = time.Now().Add(10 * time.Minute)
	}
	return nil
}

func (c *relayConn) poll(ctx context.Context, serverID string) ([]relayproto.Envelope, error) {
	path := "/relay/v1/poll?id=" + url.QueryEscape(serverID)
	resp, data, err := c.request(ctx, http.MethodPost, path, []byte("{}"), c.token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay poll failed (%d)", resp.StatusCode)
	}
	var out relayproto.PollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New("relay poll returned an invalid response")
	}
	if out.Protocol != 0 && out.Protocol != relayproto.Protocol {
		return nil, errors.New("relay uses an unsupported protocol")
	}
	return out.Frames, nil
}

func (c *relayConn) postResult(ctx context.Context, requestID string, result relayproto.Result) error {
	result = boundRelayResult(requestID, result)
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	path := "/relay/v1/results/" + url.PathEscape(requestID)
	resp, data, err := c.request(ctx, http.MethodPost, path, payload, c.token)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay result failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (c *relayConn) request(ctx context.Context, method, path string, payload []byte, token string) (*http.Response, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.transport.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("relay unreachable: %w", err)
	}
	defer resp.Body.Close()
	limit := int64(relayproto.MaxResultWire)
	if strings.Contains(path, "/poll") {
		limit = relayPollWire
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, errors.New("relay response is too large")
	}
	return resp, data, nil
}

// relayLoop keeps a node registered with its relay and executes requests
// delivered over the reverse tunnel. Reconnects are exponential up to 30
// seconds; a changed relay setting takes effect on the next poll.
func (s *Service) relayLoop(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		mode := s.relayMode()
		base, fingerprint := s.relayConfig()
		if mode == "" {
			if !s.sleepRelay(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if base == "" {
			// Auto mode resolves through the directory, which retries on its
			// own schedule. Wait for a resolved route or a settings change.
			if !s.sleepRelay(ctx, 5*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := s.runRelaySession(ctx, base, fingerprint)
		s.relayConnected.Store(false)
		s.relayTunnel.Store(false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRelayConfigChanged) || errors.Is(err, errRelayTunnelRefresh) {
			backoff = time.Second
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		if err != nil {
			s.logger.Warn("relay session ended", "relay", base, "error", err)
		}
		if !s.sleepRelay(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Service) sleepRelay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-s.relayWake:
		return true
	}
}

func (s *Service) runRelaySession(ctx context.Context, base, fingerprint string) error {
	serverID, err := s.ServerID(ctx)
	if err != nil {
		return err
	}
	conn, err := newRelayConn(s, base, fingerprint)
	if err != nil {
		return err
	}
	defer conn.close()
	if err := conn.register(ctx); err != nil {
		return err
	}
	s.relayConnected.Store(true)
	defer s.relayConnected.Store(false)
	s.logger.Info("node connected to relay", "relay", base, "relay_id", conn.relayID)

	// Prefer the bidirectional tunnel. A relay that predates it, disables it,
	// or cannot be reached over WebSocket still works over HTTPS long-poll.
	tunnel, dialErr := conn.dialTunnel(ctx, serverID)
	if dialErr == nil {
		s.logger.Debug("node relay tunnel connected", "relay", base)
		s.relayTunnel.Store(true)
		err = s.serveRelayTunnel(ctx, conn, tunnel, serverID, base, fingerprint)
		s.relayTunnel.Store(false)
		tunnel.CloseNow()
		s.relayConnected.Store(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errRelayTunnelRefresh) || errors.Is(err, errRelayConfigChanged) {
			return err
		}
		if err != nil {
			s.logger.Warn("relay tunnel ended; using HTTPS long-poll", "relay", base, "error", err)
			// The registration token is still valid, so the fallback can
			// continue immediately without another registration round trip.
			s.relayConnected.Store(true)
		}
	} else if ctx.Err() == nil {
		s.logger.Debug("relay WebSocket tunnel unavailable; using HTTPS long-poll", "relay", base, "error", dialErr)
	}
	return s.pollRelaySession(ctx, conn, serverID, base, fingerprint)
}

func (s *Service) pollRelaySession(ctx context.Context, conn *relayConn, serverID, base, fingerprint string) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, 4)
	for {
		currentBase, currentFP := s.relayConfig()
		if currentBase != base || currentFP != fingerprint {
			return errRelayConfigChanged
		}
		if time.Until(conn.expires) < relaySessionRefresh {
			if err := conn.register(ctx); err != nil {
				return err
			}
			s.logger.Debug("node relay registration refreshed", "relay", base)
		}
		frames, err := conn.poll(ctx, serverID)
		if err != nil {
			return err
		}
		for i := range frames {
			frame := frames[i]
			if frame.Target != serverID || frame.RequestID == "" {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				result := s.executeRelayFrame(ctx, frame)
				if err := conn.postResult(ctx, frame.RequestID, result); err != nil && ctx.Err() == nil {
					s.logger.Warn("could not post relay result", "request", frame.RequestID, "error", err)
				}
			}()
		}
	}
}

func (c *relayConn) dialTunnel(ctx context.Context, serverID string) (*relaytunnel.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return relaytunnel.Dial(dialCtx, c.base, c.token, serverID, c.wsClient)
}

// serveRelayTunnel runs an established WebSocket session. It returns when the
// connection fails, the relay rotates the session token, or the node's relay
// configuration changes. The caller falls back to long-poll on a transport
// error, which keeps old relays and restrictive networks working.
func (s *Service) serveRelayTunnel(ctx context.Context, conn *relayConn, tunnel *relaytunnel.Client, serverID, base, fingerprint string) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer tunnel.CloseNow()

	refresh := time.Until(conn.expires) - relaySessionRefresh
	if refresh < 10*time.Second {
		refresh = 10 * time.Second
	}
	refreshTimer := time.NewTimer(refresh)
	defer refreshTimer.Stop()
	configTicker := time.NewTicker(5 * time.Second)
	defer configTicker.Stop()

	type readResult struct {
		msg relayproto.TunnelMessage
		err error
	}
	inbound := make(chan readResult)
	go func() {
		for {
			msg, err := tunnel.Read(runCtx)
			select {
			case inbound <- readResult{msg: msg, err: err}:
			case <-runCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, 4)
	for {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-refreshTimer.C:
			return errRelayTunnelRefresh
		case <-configTicker.C:
			currentBase, currentFP := s.relayConfig()
			if currentBase != base || currentFP != fingerprint {
				return errRelayConfigChanged
			}
		case result := <-inbound:
			if result.err != nil {
				return result.err
			}
			msg := result.msg
			switch msg.Type {
			case relayproto.TunnelFrame:
				if msg.Envelope == nil || msg.Envelope.Target != serverID || msg.Envelope.RequestID == "" {
					continue
				}
				frame := *msg.Envelope
				select {
				case sem <- struct{}{}:
				case <-runCtx.Done():
					return runCtx.Err()
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					reply := s.executeRelayFrame(runCtx, frame)
					reply = boundRelayResult(frame.RequestID, reply)
					if err := tunnel.Write(runCtx, relayproto.TunnelMessage{
						Protocol: relayproto.Protocol, Type: relayproto.TunnelResult, Result: &reply,
					}); err != nil && runCtx.Err() == nil {
						s.logger.Warn("could not post relay tunnel result", "request", frame.RequestID, "error", err)
					}
				}()
			case relayproto.TunnelPing:
				_ = tunnel.Write(runCtx, relayproto.TunnelMessage{
					Protocol: relayproto.Protocol, Type: relayproto.TunnelPong, ServerTime: time.Now().UTC(),
				})
			case relayproto.TunnelError:
				return fmt.Errorf("relay tunnel error: %s", msg.Message)
			}
		}
	}
}

// boundRelayResult replaces an oversized inner response with a small, valid
// error result so one attachment cannot break the tunnel.
func boundRelayResult(requestID string, result relayproto.Result) relayproto.Result {
	result.Protocol = relayproto.Protocol
	result.RequestID = requestID
	payload, err := json.Marshal(result)
	if err == nil && len(payload) <= relayproto.MaxResultWire {
		return result
	}
	return relayproto.Result{
		Protocol: relayproto.Protocol, RequestID: requestID,
		Status: http.StatusBadGateway, Code: "response_too_large",
		Message: "the node response is too large to relay",
	}
}

func (s *Service) executeRelayFrame(ctx context.Context, frame relayproto.Envelope) (result relayproto.Result) {
	result = relayproto.Result{Protocol: relayproto.Protocol, RequestID: frame.RequestID}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = relayproto.Result{
				Protocol: relayproto.Protocol, RequestID: frame.RequestID,
				Status: http.StatusInternalServerError, Code: "internal",
				Message: "the node could not handle the relayed request",
			}
		}
	}()
	if err := relayproto.ValidateEnvelope(&frame); err != nil {
		result.Status = http.StatusBadRequest
		result.Code = "invalid_request"
		result.Message = err.Error()
		return result
	}
	body, err := relayproto.DecodeBody(frame.Body)
	if err != nil {
		result.Status = http.StatusBadRequest
		result.Code = "invalid_request"
		result.Message = "the relayed request body is invalid"
		return result
	}
	req, err := http.NewRequestWithContext(ctx, frame.Method, "https://node.internal"+frame.Path, bytes.NewReader(body))
	if err != nil {
		result.Status = http.StatusBadRequest
		result.Code = "invalid_request"
		result.Message = "the relayed request is invalid"
		return result
	}
	for name, value := range frame.Headers {
		req.Header.Set(name, value)
	}
	recorder := newRecorder()
	s.Handler().ServeHTTP(recorder, req)
	response := recorder.Result()
	_ = response.Body.Close()
	if recorder.body.Len() > maxNodeResponse {
		return relayproto.Result{
			Protocol: relayproto.Protocol, RequestID: frame.RequestID,
			Status: http.StatusBadGateway, Code: "response_too_large",
			Message: "the node response is too large to relay",
		}
	}
	result.Status = response.StatusCode
	result.Headers = map[string]string{}
	for _, name := range []string{"Content-Type", "Cache-Control"} {
		if value := response.Header.Get(name); value != "" {
			result.Headers[name] = value
		}
	}
	result.Body = relayproto.EncodeBody(recorder.body.Bytes())
	return result
}

// recorder is a minimal ResponseWriter that keeps the response in memory and
// does not impose the server's HTTP timeouts on in-process relayed requests.
type recorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newRecorder() *recorder {
	return &recorder{header: make(http.Header)}
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

func (r *recorder) Result() *http.Response {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: r.header.Clone(), Body: io.NopCloser(bytes.NewReader(r.body.Bytes()))}
}

// manifestJSON returns the signed node manifest as wire JSON without changing
// the per-call signature.
func (s *Service) manifestJSON(ctx context.Context) ([]byte, error) {
	manifest, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(manifest)
}
