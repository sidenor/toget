package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/sdk/connector"
)

const (
	defaultPollInterval = time.Second
	defaultRunTimeout   = 15 * time.Minute
	defaultHistoryLimit = 200
	defaultHistoryBytes = 256 << 10
	maxHistoryScan      = 5000
	scanPageSize        = 500
	maxSessionError     = 2000
	maxStreamText       = 64 << 10
)

// RunStream is a transient, read-only view of an active Agent run. It exists
// only in the daemon's memory on the Agent owner's node; partial text is never
// appended to the room event log. The final reply is still a message event.
type RunStream struct {
	RunID          string    `json:"run_id"`
	AgentID        string    `json:"agent_id"`
	AgentName      string    `json:"agent_name"`
	RoomID         string    `json:"room_id"`
	TriggerEventID string    `json:"trigger_event_id,omitempty"`
	TriggerSeq     int64     `json:"trigger_seq,omitempty"`
	Text           string    `json:"text"`
	Truncated      bool      `json:"truncated"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RunnerOptions wires the Agent runtime loop to local services. Post and
// SetStatus are callbacks so the runner can also be used without the LAN node.
type RunnerOptions struct {
	Agents *Service
	Rooms  *room.Service
	// Actor returns the local signing identity; the room change is attributed
	// to the human account that owns the Agent.
	Actor func(context.Context) (room.Actor, error)
	// Post sends a message, local or remote. Defaults to Rooms.PostMessage.
	Post func(context.Context, string, room.Actor, room.MessagePostedPayload) (*room.Event, error)
	// SetStatus updates presence and, for local rooms, posts a status card.
	// Defaults to Agents.SetStatus.
	SetStatus func(context.Context, string, string, string, string) error
	// PendingApprovals reports whether the Agent has an unexpired approval in
	// the room. The runner uses it to show waiting_approval while a runtime is
	// blocked.
	PendingApprovals func(context.Context, string, string) (bool, error)
	// Home is the Toget data directory passed to runtimes through TOGET_HOME,
	// so the toget CLI in an Agent subprocess talks to this daemon.
	Home string
	// Workdir returns the per-Agent workspace. The runner creates it with
	// mode 0700 before a run.
	Workdir      func(agentID string) string
	Runtimes     map[string]Runtime
	PollInterval time.Duration
	RunTimeout   time.Duration
	HistoryLimit int
	HistoryBytes int
	Logger       *slog.Logger
}

// Runner watches room events for mentions and executes at most one run per
// Agent at a time. Runs for different Agents may proceed in parallel.
type Runner struct {
	opts RunnerOptions

	mu      sync.Mutex
	running map[string]bool
	cancels map[string]context.CancelFunc
	stopped map[string]bool
	streams map[string]*RunStream
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	wg      sync.WaitGroup
}

// NewRunner creates a runtime loop. Call Start to run it.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.RunTimeout <= 0 {
		opts.RunTimeout = defaultRunTimeout
	}
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = defaultHistoryLimit
	}
	if opts.HistoryBytes <= 0 {
		opts.HistoryBytes = defaultHistoryBytes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Runtimes == nil {
		opts.Runtimes = map[string]Runtime{}
	}
	return &Runner{
		opts:    opts,
		running: map[string]bool{},
		cancels: map[string]context.CancelFunc{},
		stopped: map[string]bool{},
		streams: map[string]*RunStream{},
	}
}

// Start begins scanning rooms.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("agent runner is already started")
	}
	r.started = true
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	r.mu.Unlock()

	r.recoverSessions()
	go r.loop()
	return nil
}

// Close stops the loop and waits for active runtime commands to stop.
func (r *Runner) Close() {
	r.mu.Lock()
	if !r.started || r.cancel == nil {
		r.mu.Unlock()
		return
	}
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	cancel()
	<-done
}

func (r *Runner) loop() {
	defer close(r.done)
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()
	r.scan()
	for {
		select {
		case <-r.ctx.Done():
			r.wg.Wait()
			return
		case <-ticker.C:
			r.scan()
		}
	}
}

func (r *Runner) scan() {
	ctx := r.ctx
	actor, err := r.opts.Actor(ctx)
	if err != nil {
		r.opts.Logger.Debug("agent runner has no local account yet", "error", err)
		return
	}
	agents, err := r.opts.Agents.List(ctx, actor.AccountID)
	if err != nil {
		r.opts.Logger.Warn("agent runner could not list agents", "error", err)
		return
	}
	for i := range agents {
		agent := agents[i]
		if agent.RevokedAt != nil || r.isRunning(agent.ID) {
			continue
		}
		r.scanAgent(ctx, actor, agent)
	}
}

func (r *Runner) scanAgent(ctx context.Context, actor room.Actor, agent Agent) {
	refs, err := r.opts.Agents.Rooms(ctx, agent.ID)
	if err != nil {
		r.opts.Logger.Warn("agent runner could not list agent rooms", "agent_id", agent.ID, "error", err)
		return
	}
	for _, ref := range refs {
		if r.isRunning(agent.ID) {
			return
		}
		record, err := r.opts.Rooms.GetRoom(ctx, ref.RoomID)
		if err != nil {
			continue
		}
		if record.ArchivedAt != nil {
			continue
		}
		inRoom, err := r.opts.Agents.IsInRoom(ctx, agent.ID, record.ID)
		if err != nil || !inRoom {
			continue
		}
		session, err := r.ensureSession(ctx, agent, record)
		if err != nil {
			r.opts.Logger.Warn("agent runner could not ensure session", "agent_id", agent.ID, "room_id", record.ID, "error", err)
			continue
		}
		r.reconcileWaiting(ctx, agent, session)
		roomAgents, err := r.opts.Agents.ListInRoom(ctx, record.ID)
		if err != nil {
			roomAgents = []Agent{agent}
		}
		events, err := r.opts.Rooms.ListEvents(ctx, record.ID, session.LastSeenSeq, scanPageSize)
		if err != nil {
			continue
		}
		if len(events) == 0 {
			continue
		}
		var trigger *room.Event
		var triggerBody *room.MessagePostedPayload
		lastSeq := session.LastSeenSeq
		for i := range events {
			event := &events[i]
			lastSeq = event.Seq
			body, ok := mentionInEvent(agent.ID, roomAgents, event)
			if ok {
				trigger = event
				triggerBody = body
				break
			}
		}
		if trigger == nil {
			if lastSeq > session.LastSeenSeq {
				session.LastSeenSeq = lastSeq
				r.persist(session)
			}
			continue
		}
		runCtx, runCancel, ok := r.claim(ctx, agent.ID)
		if !ok {
			continue
		}
		session.LastSeenSeq = trigger.Seq
		session.ActiveEventID = trigger.ID
		session.ActiveSeq = trigger.Seq
		if err := r.persist(session); err != nil {
			r.release(agent.ID)
			r.opts.Logger.Warn("agent runner could not save cursor", "agent_id", agent.ID, "room_id", record.ID, "error", err)
			continue
		}
		sessionCopy := *session
		recordCopy := *record
		triggerCopy := *trigger
		bodyCopy := *triggerBody
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.release(agent.ID)
			defer runCancel()
			r.runOne(runCtx, actor, agent, recordCopy, &sessionCopy, triggerCopy, bodyCopy)
		}()
		return
	}
}

func mentionInEvent(agentID string, agents []Agent, event *room.Event) (*room.MessagePostedPayload, bool) {
	if event == nil || event.Type != room.EventMessagePosted {
		return nil, false
	}
	var body room.MessagePostedPayload
	if err := json.Unmarshal(event.Payload, &body); err != nil {
		return nil, false
	}
	// Replies and other Agent-authored messages never trigger another run.
	// This is the primary loop guard; a mention of another Agent in an Agent's
	// message still does not start an unbounded conversation.
	if body.AgentID != "" || strings.TrimSpace(body.Text) == "" {
		return nil, false
	}
	for _, id := range body.Mentions {
		if id == agentID {
			return &body, true
		}
	}
	for _, id := range DetectMentions(body.Text, agents) {
		if id == agentID {
			return &body, true
		}
	}
	return nil, false
}

func (r *Runner) runOne(parent context.Context, actor room.Actor, agent Agent, record room.Room, session *Session, trigger room.Event, body room.MessagePostedPayload) {
	ctx, cancel := context.WithTimeout(parent, r.opts.RunTimeout)
	defer cancel()

	runtime, ok := r.opts.Runtimes[agent.Runtime]
	if !ok {
		session.Status = StatusOffline
		session.LastError = fmt.Sprintf("no runtime adapter is available for %q", agent.Runtime)
		session.ActiveEventID = ""
		session.ActiveSeq = 0
		session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		r.saveIfActive(ctx, session, trigger.ID)
		if r.rootAlive() {
			r.setStatus(ctx, agent.ID, StatusOffline, "runtime unavailable", record.ID)
		}
		r.opts.Logger.Warn("agent runtime is not available", "agent_id", agent.ID, "runtime", agent.Runtime)
		return
	}

	roomAgents, _ := r.opts.Agents.ListInRoom(ctx, record.ID)
	members, _ := r.opts.Rooms.ListMembers(ctx, record.ID)
	attachments, _ := r.opts.Rooms.ListAttachments(ctx, record.ID, actor)
	names := historyNames(members, roomAgents)
	attachmentNames := make(map[string]string, len(attachments))
	for _, attachment := range attachments {
		attachmentNames[attachment.ID] = attachment.Name
	}
	history, truncated := r.collectHistory(ctx, record, actor, session.LastRunSeq, trigger.Seq, names, attachmentNames)

	workdir := ""
	if r.opts.Workdir != nil {
		workdir = strings.TrimSpace(r.opts.Workdir(agent.ID))
	}
	if workdir != "" {
		if err := os.MkdirAll(workdir, 0o700); err != nil {
			err = fmt.Errorf("create Agent workspace: %w", err)
			session.Status = StatusIdle
			session.LastError = truncate(err.Error(), maxSessionError)
			session.ActiveEventID = ""
			session.ActiveSeq = 0
			session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
			r.saveIfActive(ctx, session, trigger.ID)
			if r.rootAlive() {
				r.setStatus(ctx, agent.ID, StatusIdle, "workspace unavailable", record.ID)
			}
			r.opts.Logger.Warn("agent workspace unavailable", "agent_id", agent.ID, "error", err)
			return
		}
	}

	req := RunRequest{
		Version: connector.Version,
		Agent:   agent,
		Room:    RunRoom{ID: record.ID, Name: record.Name, Topic: record.Topic},
		Trigger: Trigger{
			EventID: trigger.ID, Seq: trigger.Seq, AuthorID: trigger.AuthorID,
			AuthorName: names[trigger.AuthorID], Text: body.Text, Format: body.Format,
			CreatedAt: trigger.CreatedAt,
		},
		History:   history,
		Truncated: truncated,
		SessionID: session.RuntimeSession,
		Home:      r.opts.Home,
		Workdir:   workdir,
	}

	session.Status = StatusRunning
	session.ActiveEventID = trigger.ID
	session.ActiveSeq = trigger.Seq
	session.LastError = ""
	session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	if err := r.persist(session); err != nil {
		r.opts.Logger.Warn("agent runner could not save running state", "agent_id", agent.ID, "room_id", record.ID, "error", err)
	}
	r.setStatus(ctx, agent.ID, StatusRunning, "working", record.ID)

	streamID := r.beginStream(agent, record, trigger)
	defer r.endStream(agent.ID, streamID)

	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		r.monitorApprovals(ctx, agent.ID, record.ID, stopMonitor)
	}()

	result, runErr := runRuntime(ctx, runtime, req, func(delta StreamDelta) {
		r.emitStream(agent.ID, streamID, delta)
	})
	// The live card is only for the runtime turn; stop advertising it before
	// posting the durable reply so the two never appear as separate answers.
	r.endStream(agent.ID, streamID)

	close(stopMonitor)
	<-monitorDone
	stopRequested := r.takeStopped(agent.ID)

	finalCtx, finalCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer finalCancel()
	pending := false
	if r.opts.PendingApprovals != nil {
		pending, _ = r.opts.PendingApprovals(finalCtx, record.ID, agent.ID)
	}

	postErr := error(nil)
	if runErr == nil && strings.TrimSpace(result.Text) != "" && parent.Err() == nil {
		if canReply, reason := r.canReply(finalCtx, agent.ID, record.ID); !canReply {
			postErr = reason
		} else {
			format := result.Format
			if format != "plain" && format != "markdown" {
				format = "markdown"
			}
			reply := room.MessagePostedPayload{
				Text:    truncate(strings.TrimSpace(result.Text), room.MaxMessageLength),
				Format:  format,
				AgentID: agent.ID,
				ReplyTo: trigger.ID,
			}
			if r.opts.Post != nil {
				_, postErr = r.opts.Post(finalCtx, record.ID, actor, reply)
			} else {
				_, postErr = r.opts.Rooms.PostMessage(finalCtx, record.ID, actor, reply)
			}
		}
	}
	if postErr != nil && runErr == nil {
		runErr = fmt.Errorf("post reply: %w", postErr)
	}

	status := StatusIdle
	detail := ""
	switch {
	case stopRequested && runErr != nil:
		detail = "run stopped"
	case errors.Is(runErr, ErrRuntimeUnavailable):
		status = StatusOffline
		detail = "runtime unavailable"
	case pending || result.WaitingApproval:
		status = StatusWaiting
		detail = "waiting for approval"
	case errors.Is(runErr, ErrRuntimeTimeout):
		detail = "run timed out"
	case errors.Is(runErr, context.Canceled) && parent.Err() != nil:
		detail = "run interrupted"
	case runErr != nil:
		detail = "run failed"
	}

	session.RuntimeSession = firstNonEmpty(result.SessionID, session.RuntimeSession)
	if runErr == nil && postErr == nil {
		session.LastRunSeq = trigger.Seq
	}
	session.RunCount++
	session.ActiveEventID = ""
	session.ActiveSeq = 0
	session.Status = status
	if runErr != nil && !stopRequested {
		session.LastError = truncate(runErr.Error(), maxSessionError)
	} else {
		session.LastError = ""
	}
	session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	r.saveIfActive(finalCtx, session, trigger.ID)

	if r.rootAlive() {
		r.setStatus(finalCtx, agent.ID, status, detail, record.ID)
	}
	if runErr != nil {
		r.opts.Logger.Warn("agent run failed", "agent_id", agent.ID, "room_id", record.ID, "runtime", agent.Runtime, "error", runErr)
	} else {
		r.opts.Logger.Info("agent run completed", "agent_id", agent.ID, "room_id", record.ID, "runtime", agent.Runtime, "reply", strings.TrimSpace(result.Text) != "")
	}
}

func runRuntime(ctx context.Context, runtime Runtime, req RunRequest, emit func(StreamDelta)) (result RunResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runtime panic: %v", recovered)
		}
	}()
	if streaming, ok := runtime.(StreamingRuntime); ok {
		result, err = streaming.RunStream(ctx, req, emit)
	} else {
		result, err = runtime.Run(ctx, req)
	}
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return result, fmt.Errorf("%w: %v", ErrRuntimeTimeout, err)
	}
	return result, err
}

func (r *Runner) monitorApprovals(ctx context.Context, agentID, roomID string, stop <-chan struct{}) {
	if r.opts.PendingApprovals == nil {
		return
	}
	waiting := false
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	check := func() {
		pending, err := r.opts.PendingApprovals(ctx, roomID, agentID)
		if err != nil || pending == waiting {
			return
		}
		waiting = pending
		status, detail := StatusRunning, "working"
		if waiting {
			status, detail = StatusWaiting, "waiting for approval"
		}
		r.setStatus(ctx, agentID, status, detail, roomID)
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			check()
		}
	}
}

func (r *Runner) canReply(ctx context.Context, agentID, roomID string) (bool, error) {
	agent, err := r.opts.Agents.Get(ctx, agentID)
	if err != nil {
		return false, err
	}
	if agent.RevokedAt != nil {
		return false, ErrRevoked
	}
	inRoom, err := r.opts.Agents.IsInRoom(ctx, agentID, roomID)
	if err != nil {
		return false, err
	}
	if !inRoom {
		return false, ErrNotInRoom
	}
	record, err := r.opts.Rooms.GetRoom(ctx, roomID)
	if err != nil {
		return false, err
	}
	if record.ArchivedAt != nil {
		return false, errors.New("room is archived")
	}
	return true, nil
}

func (r *Runner) setStatus(ctx context.Context, agentID, status, detail, roomID string) {
	var err error
	if r.opts.SetStatus != nil {
		err = r.opts.SetStatus(ctx, agentID, status, detail, roomID)
	} else {
		actor, actorErr := r.opts.Actor(ctx)
		if actorErr != nil {
			return
		}
		_, _, err = r.opts.Agents.SetStatus(ctx, agentID, status, detail, roomID, actor)
	}
	if err != nil {
		r.opts.Logger.Debug("agent status update failed", "agent_id", agentID, "room_id", roomID, "error", err)
	}
}

func (r *Runner) persist(session *Session) error {
	if r.opts.Agents == nil {
		return errors.New("agent runner has no agent service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.opts.Agents.SaveSession(ctx, session)
}

// saveIfActive persists a finished run only when the stored session still
// belongs to it. A reset, a leave, or a newer run must not be overwritten by a
// goroutine that was already finishing.
func (r *Runner) saveIfActive(ctx context.Context, session *Session, triggerID string) {
	if r.opts.Agents == nil {
		return
	}
	if _, err := r.opts.Agents.SaveSessionIfActive(ctx, session, triggerID); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return
		}
		r.opts.Logger.Warn("agent runner could not save session state",
			"agent_id", session.AgentID, "room_id", session.RoomID, "error", err)
	}
}

func (r *Runner) rootAlive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctx == nil || r.ctx.Err() == nil
}

func (r *Runner) claim(parent context.Context, agentID string) (context.Context, context.CancelFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[agentID] {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	r.running[agentID] = true
	r.cancels[agentID] = cancel
	return ctx, cancel, true
}

// Stop cancels the Agent's active run. It reports false when no run is active.
// The mention is not marked complete, so it stays in the next catch-up.
func (r *Runner) Stop(agentID string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[agentID]
	if ok {
		r.stopped[agentID] = true
	}
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (r *Runner) release(agentID string) {
	r.mu.Lock()
	cancel := r.cancels[agentID]
	delete(r.running, agentID)
	delete(r.cancels, agentID)
	delete(r.stopped, agentID)
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) takeStopped(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped[agentID] {
		return false
	}
	delete(r.stopped, agentID)
	return true
}

func (r *Runner) isRunning(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[agentID]
}

// Streams returns snapshots of active runs in a room. Streams older than the
// caller's patience are not stored anywhere else; a restart clears them.
func (r *Runner) Streams(roomID string) []RunStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RunStream, 0, len(r.streams))
	for _, stream := range r.streams {
		if stream.RoomID == roomID {
			out = append(out, *stream)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

func (r *Runner) beginStream(agent Agent, record room.Room, trigger room.Event) string {
	runID := identity.NewID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	stream := &RunStream{
		RunID: runID, AgentID: agent.ID, AgentName: agent.Name, RoomID: record.ID,
		TriggerEventID: trigger.ID, TriggerSeq: trigger.Seq, StartedAt: now, UpdatedAt: now,
	}
	r.mu.Lock()
	r.streams[agent.ID] = stream
	r.mu.Unlock()
	return runID
}

func (r *Runner) emitStream(agentID, runID string, delta StreamDelta) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.streams[agentID]
	if stream == nil || stream.RunID != runID {
		return
	}
	if delta.Reset {
		stream.Text = ""
		stream.Truncated = false
	}
	if delta.Text == "" || stream.Truncated {
		stream.UpdatedAt = now
		return
	}
	remaining := maxStreamText - len(stream.Text)
	if remaining <= 0 {
		stream.Truncated = true
		stream.UpdatedAt = now
		return
	}
	if len(delta.Text) > remaining {
		delta.Text = truncate(delta.Text, remaining)
		stream.Truncated = true
	}
	stream.Text += delta.Text
	stream.UpdatedAt = now
}

func (r *Runner) endStream(agentID, runID string) {
	r.mu.Lock()
	if stream := r.streams[agentID]; stream != nil && stream.RunID == runID {
		delete(r.streams, agentID)
	}
	r.mu.Unlock()
}

func (r *Runner) ensureSession(ctx context.Context, agent Agent, record *room.Room) (*Session, error) {
	session, err := r.opts.Agents.Session(ctx, agent.ID, record.ID)
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, ErrSessionNotFound) {
		return nil, err
	}
	head, err := r.opts.Rooms.LastSeq(ctx, record.ID)
	if err != nil {
		return nil, err
	}
	floor := head
	if joinSeq, ok := r.findJoinSeq(ctx, record.ID, agent.ID, head); ok {
		floor = joinSeq
	}
	return r.opts.Agents.EnsureSession(ctx, agent.ID, record.ID, floor)
}

func (r *Runner) findJoinSeq(ctx context.Context, roomID, agentID string, head int64) (int64, bool) {
	start := int64(0)
	if head > maxHistoryScan {
		start = head - maxHistoryScan
	}
	cursor := start
	for cursor < head {
		events, err := r.opts.Rooms.ListEvents(ctx, roomID, cursor, scanPageSize)
		if err != nil || len(events) == 0 {
			return 0, false
		}
		for i := range events {
			event := &events[i]
			if event.Type != room.EventAgentJoined {
				continue
			}
			var payload room.AgentJoinedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				continue
			}
			if payload.AgentID == agentID {
				return event.Seq, true
			}
		}
		cursor = events[len(events)-1].Seq
		if len(events) < scanPageSize {
			break
		}
	}
	return 0, false
}

func (r *Runner) recoverSessions() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	actor, err := r.opts.Actor(ctx)
	if err != nil {
		return
	}
	agents, err := r.opts.Agents.List(ctx, actor.AccountID)
	if err != nil {
		return
	}
	for i := range agents {
		agent := agents[i]
		sessions, err := r.opts.Agents.Sessions(ctx, agent.ID)
		if err != nil {
			continue
		}
		for j := range sessions {
			session := sessions[j]
			inRoom, err := r.opts.Agents.IsInRoom(ctx, agent.ID, session.RoomID)
			if err != nil || !inRoom {
				_ = r.opts.Agents.DeleteSession(ctx, agent.ID, session.RoomID)
				continue
			}
			changed := false
			if session.Status == StatusRunning {
				session.Status = StatusIdle
				session.ActiveEventID = ""
				session.ActiveSeq = 0
				session.LastError = "the previous run was interrupted by a restart"
				changed = true
			}
			if session.Status == StatusWaiting && r.opts.PendingApprovals != nil {
				pending, err := r.opts.PendingApprovals(ctx, session.RoomID, agent.ID)
				if err == nil && !pending {
					session.Status = StatusIdle
					changed = true
				}
			}
			if changed {
				session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
				if err := r.opts.Agents.SaveSession(ctx, &session); err != nil {
					r.opts.Logger.Warn("agent runner could not recover session", "agent_id", agent.ID, "room_id", session.RoomID, "error", err)
				}
			}
		}
	}
}

func (r *Runner) reconcileWaiting(ctx context.Context, agent Agent, session *Session) {
	if session.Status != StatusWaiting || r.opts.PendingApprovals == nil {
		return
	}
	pending, err := r.opts.PendingApprovals(ctx, session.RoomID, agent.ID)
	if err != nil || pending {
		return
	}
	session.Status = StatusIdle
	session.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	r.persist(session)
	r.setStatus(ctx, agent.ID, StatusIdle, "", session.RoomID)
}

func (r *Runner) collectHistory(ctx context.Context, record room.Room, actor room.Actor, after, through int64, names map[string]string, attachmentNames map[string]string) ([]HistoryEntry, bool) {
	if after < 0 {
		after = 0
	}
	if through <= after {
		return nil, false
	}
	start := after
	if through-start > maxHistoryScan {
		start = through - maxHistoryScan
	}
	var entries []HistoryEntry
	index := map[string]int{}
	cursor := start
	for cursor < through {
		events, err := r.opts.Rooms.ListEvents(ctx, record.ID, cursor, scanPageSize)
		if err != nil || len(events) == 0 {
			break
		}
		for i := range events {
			event := &events[i]
			if event.Seq > through {
				cursor = through
				break
			}
			cursor = event.Seq
			entry, ok := historyEntry(event, names, attachmentNames)
			if !ok {
				continue
			}
			if event.Type == room.EventMessageEdited {
				var edit room.MessageEditedPayload
				if err := json.Unmarshal(event.Payload, &edit); err == nil {
					if at, ok := index[edit.TargetID]; ok {
						entries[at].Text = historyText(edit.Text, nil, nil)
					}
				}
				continue
			}
			index[event.ID] = len(entries)
			entries = append(entries, entry)
		}
		if cursor >= through || len(events) < scanPageSize {
			break
		}
	}
	truncated := start > after
	bytes := 0
	for i := range entries {
		bytes += len(entries[i].Text) + len(entries[i].AuthorName) + 64
	}
	for len(entries) > r.opts.HistoryLimit || bytes > r.opts.HistoryBytes {
		if len(entries) <= 1 {
			break
		}
		bytes -= len(entries[0].Text) + len(entries[0].AuthorName) + 64
		entries = entries[1:]
		truncated = true
	}
	return entries, truncated
}

func historyEntry(event *room.Event, names map[string]string, attachmentNames map[string]string) (HistoryEntry, bool) {
	entry := HistoryEntry{
		Seq: event.Seq, EventID: event.ID, Type: event.Type,
		AuthorID: event.AuthorID, AuthorName: names[event.AuthorID], CreatedAt: event.CreatedAt,
	}
	switch event.Type {
	case room.EventMessagePosted:
		var body room.MessagePostedPayload
		if err := json.Unmarshal(event.Payload, &body); err != nil {
			return HistoryEntry{}, false
		}
		entry.AgentID = body.AgentID
		if body.AgentID != "" {
			entry.AgentName = names[body.AgentID]
		}
		entry.Text = historyText(body.Text, body.Attachments, attachmentNames)
		if entry.Text == "" {
			return HistoryEntry{}, false
		}
		return entry, true
	case room.EventMessageEdited:
		return entry, true
	case room.EventApprovalRequested:
		var payload room.ApprovalRequestedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return HistoryEntry{}, false
		}
		entry.Text = fmt.Sprintf("Approval requested by %s: %s", names[payload.AgentID], payload.Description)
		return entry, true
	case room.EventApprovalGranted, room.EventApprovalRejected, room.EventApprovalExpired:
		var payload room.ApprovalDecidedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return HistoryEntry{}, false
		}
		entry.Text = fmt.Sprintf("Approval %s: %s", payload.Decision, payload.ApprovalID)
		return entry, true
	case room.EventApprovalConsumed:
		var payload room.ApprovalConsumedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return HistoryEntry{}, false
		}
		entry.Text = fmt.Sprintf("Approval consumed: %s", payload.ApprovalID)
		return entry, true
	case room.EventToolRequested, room.EventToolResult, room.EventToolFailed:
		var payload room.ToolEventPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return HistoryEntry{}, false
		}
		entry.Text = fmt.Sprintf("Tool %s (%s)", payload.Tool, payload.Status)
		if len(payload.Input) > 0 && string(payload.Input) != "{}" {
			entry.Text += " input: " + truncate(string(payload.Input), 2000)
		}
		if payload.Output != "" {
			entry.Text += " output: " + truncate(payload.Output, 4000)
		}
		return entry, true
	case room.EventMemberJoined, room.EventMemberLeft:
		var payload room.MemberJoinedPayload
		if event.Type == room.EventMemberLeft {
			var left room.MemberLeftPayload
			if json.Unmarshal(event.Payload, &left) != nil {
				return HistoryEntry{}, false
			}
			entry.Text = fmt.Sprintf("%s left the room", displayName(names[left.AccountID], left.AccountID))
		} else {
			if json.Unmarshal(event.Payload, &payload) != nil {
				return HistoryEntry{}, false
			}
			entry.Text = fmt.Sprintf("%s joined the room", displayName(names[payload.AccountID], payload.AccountID))
		}
		return entry, true
	case room.EventAgentJoined, room.EventAgentLeft:
		var payload room.AgentJoinedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return HistoryEntry{}, false
		}
		action := "joined"
		if event.Type == room.EventAgentLeft {
			action = "left"
		}
		name := firstNonEmpty(payload.Name, names[payload.AgentID], payload.AgentID)
		entry.Text = fmt.Sprintf("Agent %s %s the room", name, action)
		return entry, true
	default:
		return HistoryEntry{}, false
	}
}

func historyText(text string, attachments []string, attachmentNames map[string]string) string {
	text = strings.TrimSpace(text)
	var names []string
	for _, id := range attachments {
		if name := attachmentNames[id]; name != "" {
			names = append(names, name)
		} else {
			names = append(names, id)
		}
	}
	if len(names) > 0 {
		text += " [attached: " + strings.Join(names, ", ") + "]"
	}
	return truncate(strings.TrimSpace(text), 16*1024)
}

func historyNames(members []room.Member, agents []Agent) map[string]string {
	names := make(map[string]string, len(members)+len(agents))
	for _, member := range members {
		names[member.AccountID] = displayName(member.DisplayName, member.AccountID)
	}
	for _, agent := range agents {
		names[agent.ID] = agent.Name
	}
	return names
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// IsInRoom reports Agent membership.
func (s *Service) IsInRoom(ctx context.Context, agentID, roomID string) (bool, error) {
	return s.store.IsAgentInRoom(ctx, agentID, roomID)
}
