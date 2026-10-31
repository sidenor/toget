package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	nodenet "github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/relaytunnel"
)

const (
	maxPollWire    = 4*relayproto.MaxEnvelopeWire + (1 << 20)
	maxParentReply = relayproto.MaxResultWire
	parentTimeout  = 75 * time.Second
	announceEvery  = 20 * time.Second
)

// parentLink is the relay's client connection to a parent relay. The child
// registers its self-certifying relay ID, long-polls for frames addressed to
// any route below it, and publishes its route table upward.
type parentLink struct {
	svc         *Service
	base        string
	fingerprint string
	url         string
	client      *http.Client
	wsClient    *http.Client
}

func newParentLink(_ context.Context, svc *Service, raw string) (*parentLink, error) {
	if raw == "" {
		return nil, nil
	}
	base, fingerprint, err := nodenet.ParsePinnedURL(raw)
	if err != nil {
		return nil, err
	}
	config, err := nodenet.PinnedTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       config,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: parentTimeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	tunnelTransport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       config,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
	}
	return &parentLink{
		svc: svc, base: base, fingerprint: fingerprint, url: raw,
		client:   &http.Client{Transport: transport, Timeout: parentTimeout},
		wsClient: &http.Client{Transport: tunnelTransport},
	}, nil
}

func (p *parentLink) register(ctx context.Context) (string, time.Time, error) {
	pub := p.svc.key.Public().(ed25519.PublicKey)
	req := relayproto.RegisterRequest{
		Kind:      "relay",
		RelayID:   p.svc.relayID,
		PublicKey: base64.RawStdEncoding.EncodeToString(pub),
	}
	if err := relayproto.SignRegistration(&req, p.svc.key, time.Now()); err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", time.Time{}, err
	}
	resp, data, err := p.request(ctx, http.MethodPost, "/relay/v1/register", payload, "")
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("parent registration failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out relayproto.RegisterResponse
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return "", time.Time{}, errors.New("parent registration returned no token")
	}
	return out.Token, out.ExpiresAt, nil
}

func (p *parentLink) poll(ctx context.Context, token string) ([]relayproto.Envelope, error) {
	path := "/relay/v1/poll?id=" + url.QueryEscape(p.svc.relayID)
	resp, data, err := p.request(ctx, http.MethodPost, path, []byte("{}"), token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parent poll failed (%d)", resp.StatusCode)
	}
	var out relayproto.PollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New("parent poll returned an invalid response")
	}
	return out.Frames, nil
}

func (p *parentLink) announce(ctx context.Context, token string, routes []relayproto.RouteAnnouncement) error {
	payload, err := json.Marshal(relayproto.RoutesRequest{
		Protocol: relayproto.Protocol, Routes: routes, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	resp, data, err := p.request(ctx, http.MethodPost, "/relay/v1/routes", payload, token)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("parent route announcement failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (p *parentLink) postResult(ctx context.Context, token, requestID string, result relayproto.Result) error {
	result.Protocol = relayproto.Protocol
	result.RequestID = requestID
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	path := "/relay/v1/results/" + url.PathEscape(requestID)
	resp, data, err := p.request(ctx, http.MethodPost, path, payload, token)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("parent result failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (p *parentLink) forward(ctx context.Context, env *relayproto.Envelope) (*http.Response, []byte, error) {
	signed := *env
	if err := relayproto.SignForward(&signed, p.svc.key, time.Now()); err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(&signed)
	if err != nil {
		return nil, nil, err
	}
	path := relayproto.RelayForwardPrefix + url.PathEscape(env.Target)
	resp, data, err := p.request(ctx, http.MethodPost, path, payload, "")
	if err != nil {
		return nil, nil, err
	}
	// Older parents know only the unsigned endpoint. The signed request was
	// rejected without reaching a node, so retrying is safe and keeps mixed
	// relay trees working during a rolling upgrade.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		unsigned := *env
		unsigned.ForwarderID, unsigned.ForwarderKey = "", ""
		unsigned.ForwardedAt, unsigned.ForwardNonce, unsigned.ForwardSignature = 0, "", ""
		payload, err = json.Marshal(&unsigned)
		if err != nil {
			return nil, nil, err
		}
		return p.request(ctx, http.MethodPost, relayproto.ForwardPrefix+url.PathEscape(env.Target), payload, "")
	}
	return resp, data, nil
}

func (p *parentLink) pollTicket(ctx context.Context, queued relayproto.Queued) relayproto.Result {
	deadline := time.Now().Add(2 * time.Minute)
	path := "/relay/v1/responses/" + url.PathEscape(queued.Ticket)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errorResult(http.StatusGatewayTimeout, "parent_queue_timeout", "the parent relay did not complete the queued request")
		}
		resp, data, err := p.request(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			return errorResult(http.StatusBadGateway, "parent_unreachable", "the parent relay is unreachable")
		}
		if resp.StatusCode == http.StatusAccepted && resp.Header.Get(relayproto.HeaderRelayQueued) == "true" {
			timer := time.NewTimer(750 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errorResult(http.StatusGatewayTimeout, "request_canceled", "the relayed request was canceled")
			case <-timer.C:
				continue
			}
		}
		return resultFromResponse(resp, data)
	}
}

func (p *parentLink) request(ctx context.Context, method, path string, payload []byte, token string) (*http.Response, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return nil, nil, fmt.Errorf("build parent request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("parent relay unreachable: %w", err)
	}
	defer resp.Body.Close()
	limit := int64(maxParentReply)
	if strings.HasSuffix(path, "/poll") || strings.Contains(path, "/poll?") {
		limit = maxPollWire
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, errors.New("parent response is too large")
	}
	return resp, data, nil
}

// runParent maintains the parent registration and poll loop.
func (s *Service) runParent(ctx context.Context) {
	parent := s.parent
	if parent == nil {
		return
	}
	backoff := time.Second
	for {
		token, expires, err := parent.register(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("relay parent registration failed", "url", parent.url, "error", err)
			if !sleepContext(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		started := time.Now()
		s.logger.Info("relay parent connected", "url", parent.url)
		runCtx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.announceLoop(runCtx, parent, token)
		}()
		err = s.pollParent(runCtx, parent, token, expires)
		cancel()
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		s.logger.Warn("relay parent disconnected", "url", parent.url, "error", err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		if !sleepContext(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Service) pollParent(ctx context.Context, parent *parentLink, token string, expires time.Time) error {
	// Prefer a WebSocket tunnel. If the parent does not support it, keep the
	// existing long-poll path; both use the same registration token.
	tunnel, err := parent.dialTunnel(ctx, s.relayID, token)
	if err == nil {
		s.logger.Debug("relay parent tunnel connected", "url", parent.url)
		err = s.serveParentTunnel(ctx, parent, tunnel, expires)
		tunnel.CloseNow()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.logger.Warn("relay parent tunnel ended; using HTTPS long-poll", "url", parent.url, "error", err)
			return s.pollParentHTTP(ctx, parent, token)
		}
		return nil
	}
	s.logger.Debug("relay parent WebSocket unavailable; using HTTPS long-poll", "url", parent.url, "error", err)
	return s.pollParentHTTP(ctx, parent, token)
}

func (s *Service) pollParentHTTP(ctx context.Context, parent *parentLink, token string) error {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		frames, err := parent.poll(ctx, token)
		if err != nil {
			return err
		}
		for i := range frames {
			frame := frames[i]
			if frame.Target == "" || frame.RequestID == "" {
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
				result := s.forwardLocal(ctx, &frame, false)
				result = boundParentResult(frame.RequestID, result)
				if err := parent.postResult(ctx, token, frame.RequestID, result); err != nil {
					s.logger.Debug("relay parent result failed", "request", frame.RequestID, "error", err)
				}
			}()
		}
	}
}

func (p *parentLink) dialTunnel(ctx context.Context, relayID, token string) (*relaytunnel.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return relaytunnel.Dial(dialCtx, p.base, token, relayID, p.wsClient)
}

func (s *Service) serveParentTunnel(ctx context.Context, parent *parentLink, tunnel *relaytunnel.Client, expires time.Time) error {
	refresh := time.Until(expires) - 2*time.Minute
	if refresh < 10*time.Second {
		refresh = 10 * time.Second
	}
	refreshTimer := time.NewTimer(refresh)
	defer refreshTimer.Stop()
	type readResult struct {
		msg relayproto.TunnelMessage
		err error
	}
	inbound := make(chan readResult)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			msg, err := tunnel.Read(readCtx)
			select {
			case inbound <- readResult{msg: msg, err: err}:
			case <-readCtx.Done():
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
		case <-ctx.Done():
			return ctx.Err()
		case <-refreshTimer.C:
			return nil
		case result := <-inbound:
			if result.err != nil {
				return result.err
			}
			msg := result.msg
			switch msg.Type {
			case relayproto.TunnelFrame:
				if msg.Envelope == nil || msg.Envelope.Target == "" || msg.Envelope.RequestID == "" {
					continue
				}
				frame := *msg.Envelope
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return ctx.Err()
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					reply := s.forwardLocal(ctx, &frame, false)
					reply = boundParentResult(frame.RequestID, reply)
					if err := tunnel.Write(ctx, relayproto.TunnelMessage{
						Protocol: relayproto.Protocol, Type: relayproto.TunnelResult, Result: &reply,
					}); err != nil && ctx.Err() == nil {
						s.logger.Debug("relay parent tunnel result failed", "request", frame.RequestID, "error", err)
					}
				}()
			case relayproto.TunnelPing:
				_ = tunnel.Write(ctx, relayproto.TunnelMessage{
					Protocol: relayproto.Protocol, Type: relayproto.TunnelPong, ServerTime: time.Now().UTC(),
				})
			case relayproto.TunnelError:
				return fmt.Errorf("parent relay tunnel error: %s", msg.Message)
			}
		}
	}
}

func boundParentResult(requestID string, result relayproto.Result) relayproto.Result {
	result.Protocol = relayproto.Protocol
	result.RequestID = requestID
	payload, err := json.Marshal(result)
	if err == nil && len(payload) <= relayproto.MaxResultWire {
		return result
	}
	out := errorResult(http.StatusBadGateway, "response_too_large", "the node response is too large to relay")
	out.RequestID = requestID
	return out
}

func (s *Service) announceLoop(ctx context.Context, parent *parentLink, token string) {
	s.announceOnce(ctx, parent, token)
	ticker := time.NewTicker(announceEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.announceOnce(ctx, parent, token)
		case <-s.announceWake:
			// A node just came online below this relay; publish the new
			// route immediately instead of waiting for the next tick.
			s.announceOnce(ctx, parent, token)
		}
	}
}

func (s *Service) announceOnce(ctx context.Context, parent *parentLink, token string) {
	if err := parent.announce(ctx, token, s.announcements()); err != nil && ctx.Err() == nil {
		// A failed announcement must not stop future retries. If the parent
		// session itself is gone, the poll loop re-registers.
		s.logger.Debug("relay route announcement failed", "error", err)
	}
}

func (s *Service) announcements() []relayproto.RouteAnnouncement {
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]relayproto.RouteAnnouncement, 0, len(s.sessions)+len(s.routes))
	for id, sess := range s.sessions {
		if sess.kind != "node" || !s.sessionLiveLocked(sess, now) {
			continue
		}
		out = append(out, relayproto.RouteAnnouncement{
			Target: id, Kind: "node", Hops: 1, Via: []string{s.relayID},
		})
		if len(out) >= 10000 {
			return out
		}
	}
	for target, entry := range s.routes {
		if !now.Before(entry.expires) || !s.sessionLiveLocked(entry.session, now) {
			continue
		}
		via := append([]string{s.relayID}, entry.via...)
		if len(via) > relayproto.MaxHops {
			continue
		}
		out = append(out, relayproto.RouteAnnouncement{
			Target: target, Kind: entry.kind, Hops: len(via), Via: via,
		})
		if len(out) >= 10000 {
			return out
		}
	}
	return out
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
