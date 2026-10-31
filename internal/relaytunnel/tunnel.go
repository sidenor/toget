// Package relaytunnel implements the client side of the relay WebSocket
// tunnel. Nodes and child relays register over HTTPS, then dial /relay/v1/ws
// to receive envelopes and post results without repeated long-polls. The
// HTTPS long-poll path remains available when the tunnel cannot be opened.
package relaytunnel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sidenor/toget/internal/relayproto"
)

const (
	// Path is the tunnel endpoint on a relay.
	Path = "/relay/v1/ws"
	// maxRead bounds one inbound message. The relay only sends envelopes, but
	// leaving room for large frames keeps this transport independent of the
	// long-poll batch size.
	maxRead   = 4*relayproto.MaxEnvelopeWire + (1 << 20)
	writeWait = 30 * time.Second
)

// Client is one authenticated WebSocket tunnel connection.
type Client struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// Dial opens a tunnel for a registered session. base is the pinned relay
// HTTPS origin and token is the bearer token returned by registration.
func Dial(ctx context.Context, base, token, id string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("relay tunnel requires a session token")
	}
	endpoint, err := tunnelURL(base, id)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient:      httpClient,
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("relay tunnel: %w", err)
	}
	conn.SetReadLimit(maxRead)
	client := &Client{conn: conn}
	helloCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var hello relayproto.TunnelMessage
	if err := wsjson.Read(helloCtx, conn, &hello); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("relay tunnel hello: %w", err)
	}
	if hello.Protocol != relayproto.Protocol || hello.Type != relayproto.TunnelHello {
		conn.CloseNow()
		return nil, errors.New("relay tunnel returned an unsupported hello")
	}
	return client, nil
}

// Read waits for one tunnel message.
func (c *Client) Read(ctx context.Context) (relayproto.TunnelMessage, error) {
	var msg relayproto.TunnelMessage
	if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
		return relayproto.TunnelMessage{}, err
	}
	if msg.Protocol != 0 && msg.Protocol != relayproto.Protocol {
		return relayproto.TunnelMessage{}, errors.New("relay tunnel uses an unsupported protocol")
	}
	return msg, nil
}

// Write sends one tunnel message. Writes are serialized because coder
// websocket permits only one concurrent writer.
func (c *Client) Write(ctx context.Context, msg relayproto.TunnelMessage) error {
	msg.Protocol = relayproto.Protocol
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	return wsjson.Write(writeCtx, c.conn, msg)
}

// Close closes the connection cleanly.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close(websocket.StatusNormalClosure, "bye")
}

// CloseNow abandons the connection without a close handshake.
func (c *Client) CloseNow() {
	if c != nil && c.conn != nil {
		c.conn.CloseNow()
	}
}

func tunnelURL(base, id string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" {
		return "", errors.New("relay tunnel base is invalid")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("relay tunnel base must be HTTPS")
	}
	parsed.Path = Path
	parsed.RawQuery = url.Values{"id": []string{id}}.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}
