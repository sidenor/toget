package connector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidenor/toget/sdk/connector"
)

func testRequest() connector.Request {
	return connector.Request{
		Version: connector.Version,
		Agent: connector.Agent{
			ID: "agent-1", AccountID: "account-1", Name: "Claude",
			Runtime: "my-connector", Status: "idle",
			Permissions: json.RawMessage(`{"shell":false}`),
			CreatedAt:   time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
		},
		Room: connector.Room{ID: "room-1", Name: "Lab", Topic: "demo"},
		Trigger: connector.Trigger{
			EventID: "event-1", Seq: 7, AuthorID: "account-1", AuthorName: "Aiden",
			Text: "@Claude check", Format: "plain",
			CreatedAt: time.Date(2026, 10, 31, 0, 0, 1, 0, time.UTC),
		},
		History: []connector.HistoryEntry{{
			Seq: 7, EventID: "event-1", Type: "message.posted",
			AuthorID: "account-1", AuthorName: "Aiden", Text: "@Claude check",
			CreatedAt: time.Date(2026, 10, 31, 0, 0, 1, 0, time.UTC),
		}},
		SessionID: "session-1",
		Home:      "/tmp/home",
		Workdir:   "/tmp/home/agents/agent-1",
	}
}

func TestDecodeRequestV1(t *testing.T) {
	data, err := json.Marshal(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"future_field":true}`)...)
	req, err := connector.DecodeRequest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if req.Version != connector.Version || req.Agent.ID != "agent-1" ||
		req.Room.Name != "Lab" || req.Trigger.EventID != "event-1" ||
		len(req.History) != 1 || req.SessionID != "session-1" {
		t.Fatalf("request = %#v", req)
	}
}

func TestDecodeRequestErrors(t *testing.T) {
	tests := map[string]struct {
		body string
		want error
	}{
		"unsupported version": {`{"version":2}`, connector.ErrUnsupportedVersion},
		"missing version":     {`{"agent":{}}`, connector.ErrUnsupportedVersion},
		"malformed":           {`{`, nil},
		"empty":               {``, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := connector.DecodeRequest(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("DecodeRequest succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDecodeRequestTooLarge(t *testing.T) {
	body := `{"version":1,"padding":"` + strings.Repeat("x", connector.MaxRequestBytes) + `"}`
	_, err := connector.DecodeRequest(strings.NewReader(body))
	if !errors.Is(err, connector.ErrRequestTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStreamsThenWritesResult(t *testing.T) {
	input, err := json.Marshal(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = connector.Run(context.Background(), bytes.NewReader(input), &out,
		func(ctx context.Context, req connector.Request, stream *connector.Emitter) (connector.Result, error) {
			if req.Agent.Name != "Claude" {
				t.Fatalf("request = %#v", req)
			}
			if err := stream.Delta("Working"); err != nil {
				return connector.Result{}, err
			}
			if err := stream.Reset(); err != nil {
				return connector.Result{}, err
			}
			if err := stream.Delta("Done"); err != nil {
				return connector.Result{}, err
			}
			return connector.Result{Text: "Done", Format: "markdown", SessionID: "session-2"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	lines := decodeEvents(t, out.Bytes())
	if len(lines) != 4 {
		t.Fatalf("events = %#v", lines)
	}
	if lines[0].Type != "delta" || lines[0].Text != "Working" ||
		lines[1].Type != "delta" || !lines[1].Reset ||
		lines[2].Type != "delta" || lines[2].Text != "Done" ||
		lines[3].Type != "result" || lines[3].Text != "Done" ||
		lines[3].Format != "markdown" || lines[3].SessionID != "session-2" {
		t.Fatalf("events = %#v", lines)
	}
}

func TestRunWritesExplicitEmptyResult(t *testing.T) {
	input, err := json.Marshal(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = connector.Run(context.Background(), bytes.NewReader(input), &out,
		func(ctx context.Context, req connector.Request, stream *connector.Emitter) (connector.Result, error) {
			if err := stream.Delta("preview"); err != nil {
				return connector.Result{}, err
			}
			return connector.Result{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	events := decodeEvents(t, out.Bytes())
	if len(events) != 2 || events[0].Type != "delta" || events[0].Text != "preview" ||
		events[1].Type != "result" || events[1].Text != "" {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunHandlerErrorWritesNoResult(t *testing.T) {
	input, err := json.Marshal(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	want := errors.New("boom")
	err = connector.Run(context.Background(), bytes.NewReader(input), &out,
		func(context.Context, connector.Request, *connector.Emitter) (connector.Result, error) {
			return connector.Result{}, want
		})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q", out.String())
	}
}

func TestEmitterIsSafeForConcurrentDeltas(t *testing.T) {
	input, err := json.Marshal(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var wg sync.WaitGroup
	err = connector.Run(context.Background(), bytes.NewReader(input), &out,
		func(ctx context.Context, req connector.Request, stream *connector.Emitter) (connector.Result, error) {
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := stream.Delta("x"); err != nil {
						t.Errorf("Delta: %v", err)
					}
				}()
			}
			wg.Wait()
			return connector.Result{Text: "done"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	events := decodeEvents(t, out.Bytes())
	if len(events) != 51 {
		t.Fatalf("events = %d", len(events))
	}
	for _, event := range events[:len(events)-1] {
		if event.Type != "delta" || event.Text != "x" {
			t.Fatalf("event = %#v", event)
		}
	}
	if events[len(events)-1].Type != "result" || events[len(events)-1].Text != "done" {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
}

func decodeEvents(t *testing.T, data []byte) []connector.Event {
	t.Helper()
	var events []connector.Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event connector.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}
