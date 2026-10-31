package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

var (
	// ErrUnsupportedVersion means the host asked for a connector protocol
	// version this program does not implement.
	ErrUnsupportedVersion = errors.New("unsupported connector protocol version")
	// ErrRequestTooLarge means stdin exceeded MaxRequestBytes.
	ErrRequestTooLarge = errors.New("connector request is too large")
)

// DecodeRequest reads and validates one Request from r. Unknown fields are
// ignored so a newer host can add fields within version 1.
func DecodeRequest(r io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("read connector request: %w", err)
	}
	if len(data) > MaxRequestBytes {
		return Request{}, ErrRequestTooLarge
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, fmt.Errorf("decode connector request: %w", err)
	}
	if req.Version != Version {
		return Request{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, req.Version, Version)
	}
	return req, nil
}

// Emitter writes newline-delimited protocol events. Its methods are safe to
// call concurrently; each event is written as one line.
type Emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newEmitter(w io.Writer) *Emitter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Emitter{enc: enc}
}

// Delta appends text to the live display.
func (e *Emitter) Delta(text string) error {
	return e.write(Event{Type: "delta", Text: text})
}

// Reset clears the text collected for the live display. Use it before
// retrying with a fresh session after a stale-session error.
func (e *Emitter) Reset() error {
	return e.write(Event{Type: "delta", Reset: true})
}

func (e *Emitter) result(result Result) error {
	return e.write(Event{
		Type:            "result",
		Text:            result.Text,
		Format:          result.Format,
		SessionID:       result.SessionID,
		WaitingApproval: result.WaitingApproval,
	})
}

func (e *Emitter) write(event Event) error {
	if e == nil {
		return errors.New("connector: nil emitter")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(event)
}

// Handler implements one connector invocation. Use stream (if desired) for
// partial output and return the final Result. Returning an error exits the
// connector without writing a result, so the host marks the run failed.
type Handler func(ctx context.Context, req Request, stream *Emitter) (Result, error)

// Run reads one Request from in, calls handler, and writes the final result to
// out. The caller controls the process lifetime. A nil handler is an error.
func Run(ctx context.Context, in io.Reader, out io.Writer, handler Handler) error {
	if handler == nil {
		return errors.New("connector: nil handler")
	}
	req, err := DecodeRequest(in)
	if err != nil {
		return err
	}
	stream := newEmitter(out)
	result, err := handler(ctx, req, stream)
	if err != nil {
		return err
	}
	return stream.result(result)
}

// Main runs one connector invocation on stdin/stdout and exits nonzero on
// failure. It is the usual entry point for a connector binary.
func Main(handler Handler) {
	if err := Run(context.Background(), os.Stdin, os.Stdout, handler); err != nil {
		fmt.Fprintln(os.Stderr, "connector:", err)
		os.Exit(1)
	}
}
