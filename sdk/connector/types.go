// Package connector implements version 1 of the Toget Agent connector
// protocol.
//
// A connector is a local command. For each mention, Toget starts the command,
// writes one Request as JSON to stdin, and reads events from stdout until the
// process exits. A connector may emit text deltas while it works and can end
// with a Result. If it emits only deltas, the host posts the collected text;
// the legacy single-JSON-object and plain-text forms also remain accepted.
//
// See docs/connectors.md for the complete wire contract and compatibility
// policy.
package connector

import (
	"encoding/json"
	"time"
)

// Version is the connector protocol version this package implements. The host
// writes this value in Request.Version and rejects a request it does not
// understand.
const Version = 1

// MaxRequestBytes bounds one encoded Request. A larger input is rejected
// instead of being buffered without limit.
const MaxRequestBytes = 8 << 20

// Request is the complete input for one connector invocation.
type Request struct {
	Version int     `json:"version"`
	Agent   Agent   `json:"agent"`
	Room    Room    `json:"room"`
	Trigger Trigger `json:"trigger"`
	// History is oldest first and includes the trigger message. It begins
	// after the previous completed run, so it is exactly the Agent's catch-up.
	History []HistoryEntry `json:"history"`
	// Truncated means older events were omitted to bound the prompt.
	Truncated bool   `json:"truncated,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Home      string `json:"home,omitempty"`
	// Workdir is the per-Agent workspace. Connectors should use it as their
	// working directory so two Agents never share local files by accident.
	Workdir string `json:"workdir,omitempty"`
}

// Agent describes the Agent being invoked.
type Agent struct {
	ID          string          `json:"id"`
	AccountID   string          `json:"account_id"`
	Name        string          `json:"name"`
	Runtime     string          `json:"runtime"`
	Status      string          `json:"status"`
	Permissions json.RawMessage `json:"permissions"`
	CreatedAt   time.Time       `json:"created_at"`
	LastSeenAt  *time.Time      `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time      `json:"revoked_at,omitempty"`
}

// Room is the room context handed to the connector.
type Room struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Topic string `json:"topic,omitempty"`
}

// Trigger is the message that mentioned the Agent.
type Trigger struct {
	EventID    string    `json:"event_id"`
	Seq        int64     `json:"seq"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name,omitempty"`
	Text       string    `json:"text"`
	Format     string    `json:"format,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// HistoryEntry is one room event in the Agent's catch-up context.
type HistoryEntry struct {
	Seq        int64     `json:"seq"`
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	AuthorID   string    `json:"author_id,omitempty"`
	AuthorName string    `json:"author_name,omitempty"`
	AgentID    string    `json:"agent_id,omitempty"`
	AgentName  string    `json:"agent_name,omitempty"`
	Text       string    `json:"text,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Result is the final answer from one run. Empty Text means the connector
// chooses not to post anything. Format is "plain" or "markdown"; the host
// treats any other non-empty value as markdown. Return the same or a new
// SessionID to resume the connector's own conversation on the next mention.
type Result struct {
	Text            string `json:"text"`
	Format          string `json:"format,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WaitingApproval bool   `json:"waiting_approval,omitempty"`
}

// Event is one newline-delimited JSON event on stdout.
//
//	type=delta  text is appended to the live display; reset clears it first.
//	type=result text/format/session_id/waiting_approval is the final answer.
type Event struct {
	Type            string `json:"type"`
	Text            string `json:"text,omitempty"`
	Reset           bool   `json:"reset,omitempty"`
	Format          string `json:"format,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WaitingApproval bool   `json:"waiting_approval,omitempty"`
}
