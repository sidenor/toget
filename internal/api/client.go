package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to a local Toget node.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient creates a client for a loopback listen address.
func NewClient(listen, token string) *Client {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "127.0.0.1", "7718"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return &Client{
		BaseURL: "http://" + net.JoinHostPort(host, port),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is a structured error returned by the local API.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("local API returned %d", e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Get performs a GET request.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Post performs a POST request with a JSON body.
func (c *Client) Post(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, http.MethodPost, path, in, out)
}

// Patch performs a PATCH request with a JSON body.
func (c *Client) Patch(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, http.MethodPatch, path, in, out)
}

// Delete performs a DELETE request.
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodDelete, path, nil, out)
}

// Do performs a request with an arbitrary method and JSON body.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, method, path, in, out)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the local node at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error APIError `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
			envelope.Error.Status = resp.StatusCode
			return &envelope.Error
		}
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Upload sends raw bytes to a local API path.
func (c *Client) Upload(ctx context.Context, path string, data []byte, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the local node at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error APIError `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
			envelope.Error.Status = resp.StatusCode
			return nil, &envelope.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	return data, nil
}

// Download fetches raw bytes from a local API path.
func (c *Client) Download(ctx context.Context, path string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach the local node at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error APIError `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
			envelope.Error.Status = resp.StatusCode
			return nil, nil, &envelope.Error
		}
		return nil, nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	return data, resp.Header, nil
}

// IsNotFound reports whether an error is a 404 from the local API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
