package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/storage"
)

type httpClient struct {
	client *http.Client
}

func newHTTPClient() *httpClient {
	return &httpClient{client: &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// A push endpoint must not redirect; following one could send the
			// VAPID Authorization header to an unintended host.
			return http.ErrUseLastResponse
		},
	}}
}

func (c *httpClient) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

func newPushRequest(ctx context.Context, sub storage.PushSubscription, kind string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("notify: build push request: %w", err)
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	if kind == KindApproval {
		req.Header.Set("Urgency", "high")
		req.Header.Set("Topic", "toget-approval")
	} else {
		req.Header.Set("Urgency", "normal")
		req.Header.Set("Topic", "toget-message")
	}
	return req, nil
}
