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

	"github.com/sidenor/toget/internal/relayproto"
)

const (
	relayPollWire       = 4*relayproto.MaxEnvelopeWire + (1 << 20)
	relaySessionRefresh = 2 * time.Minute
)

var errRelayConfigChanged = errors.New("relay configuration changed")

type relayConn struct {
	svc         *Service
	base        string
	fingerprint string
	client      *http.Client
	token       string
	expires     time.Time
	relayID     string
}

func newRelayConn(s *Service, base, fingerprint string) (*relayConn, error) {
	config, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	return &relayConn{
		svc: s, base: base, fingerprint: fingerprint,
		client: pinnedHTTPClient(config, relayTimeout, relayTimeout),
	}, nil
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
	result.Protocol = relayproto.Protocol
	result.RequestID = requestID
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(payload) > relayproto.MaxResultWire {
		payload, _ = json.Marshal(relayproto.Result{
			Protocol: relayproto.Protocol, RequestID: requestID,
			Status: http.StatusBadGateway, Code: "response_too_large",
			Message: "the node response is too large to relay",
		})
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
	resp, err := c.client.Do(req)
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
		base, fingerprint := s.relayConfig()
		if base == "" {
			if s.relaySetting == "auto" {
				if !sleepContext(ctx, 5*time.Second) {
					return
				}
				continue
			}
			return
		}
		started := time.Now()
		err := s.runRelaySession(ctx, base, fingerprint)
		s.relayConnected.Store(false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRelayConfigChanged) {
			backoff = time.Second
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		if err != nil {
			s.logger.Warn("relay session ended", "relay", base, "error", err)
		}
		if !sleepContext(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
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
	if err := conn.register(ctx); err != nil {
		return err
	}
	s.relayConnected.Store(true)
	defer s.relayConnected.Store(false)
	s.logger.Info("node connected to relay", "relay", base, "relay_id", conn.relayID)

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
