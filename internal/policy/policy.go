// Package policy implements the smallest useful Agent permission language:
// rules over action kind, tool, target, recipient, amount, currency, weekday,
// and local time window. Deny wins; an explicit allow is pre-authorization;
// unmatched risky actions still require a human approval.
package policy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

var (
	// ErrNotFound means no policy is stored for the room and Agent.
	ErrNotFound = errors.New("policy not found")
	// ErrInvalid means a rule is malformed.
	ErrInvalid = errors.New("invalid policy")
	// ErrForbidden means the caller may not manage this policy.
	ErrForbidden = errors.New("not allowed to manage this policy")
	// ErrAgentNotInRoom means the Agent is not an active member of the room.
	ErrAgentNotInRoom = errors.New("agent is not in the room")
)

const (
	EffectAllow  = "allow"
	EffectDeny   = "deny"
	EffectReview = "approval"

	MaxRules        = 64
	MaxPattern      = 256
	MaxNote         = 200
	MaxRuleID       = 64
	MaxAmount       = 64
	MaxTimezone     = 64
	MaxRulesJSON    = 64 * 1024
	DefaultTimezone = "Local"
)

// Action is the subset of an approval action that policy rules can match.
// Approval keeps its own canonical wire type; callers map the fields across.
type Action struct {
	Kind      string
	Tool      string
	Target    string
	Recipient string
	Amount    string
	Currency  string
}

// Rule is one policy rule. Empty selector fields match anything. Amount
// bounds are decimal strings compared exactly. Weekdays use 0=Sunday through
// 6=Saturday. Start and End are "HH:MM" in the rule's timezone; a window with
// End earlier than Start crosses midnight.
type Rule struct {
	ID        string `json:"id"`
	Effect    string `json:"effect"`
	Kind      string `json:"kind,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Target    string `json:"target,omitempty"`
	Recipient string `json:"recipient,omitempty"`
	AmountMin string `json:"amount_min,omitempty"`
	AmountMax string `json:"amount_max,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Weekdays  []int  `json:"weekdays,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Policy is the rule set for one room, optionally narrowed to one Agent.
type Policy struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	Rules     []Rule    `json:"rules"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Decision is the result of evaluating one action.
type Decision struct {
	Effect  string `json:"effect"`
	RuleID  string `json:"rule_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	Reason  string `json:"reason"`
}

// Store persists policy documents.
type Store interface {
	GetPolicy(ctx context.Context, roomID, agentID string) (*Policy, error)
	ListPolicies(ctx context.Context, roomID string) ([]Policy, error)
	UpsertPolicy(ctx context.Context, p *Policy) error
	DeletePolicy(ctx context.Context, roomID, agentID string) error
}

// Service owns policy validation and evaluation.
type Service struct {
	store  Store
	rooms  *room.Service
	agents *agent.Service
	now    func() time.Time
}

// New creates a policy service.
func New(store Store, rooms *room.Service, agents *agent.Service) *Service {
	return &Service{store: store, rooms: rooms, agents: agents, now: time.Now}
}

// Get returns the effective stored policy for a room and Agent. An agentID of
// "" addresses the room default policy.
func (s *Service) Get(ctx context.Context, roomID, agentID string, actor room.Actor) (*Policy, error) {
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	p, err := s.store.GetPolicy(ctx, roomID, agentID)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// List returns the room default policy first, then per-Agent policies.
func (s *Service) List(ctx context.Context, roomID string, actor room.Actor) ([]Policy, error) {
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	return s.store.ListPolicies(ctx, roomID)
}

// Save replaces the rule set for a room and Agent. Missing rule IDs are
// generated; supplied IDs must be unique.
func (s *Service) Save(ctx context.Context, roomID, agentID string, rules []Rule, actor room.Actor) (*Policy, *room.Event, error) {
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}
	if err := s.authorize(ctx, roomID, agentID, actor); err != nil {
		return nil, nil, err
	}
	normalized, err := normalizeRules(rules)
	if err != nil {
		return nil, nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	p := &Policy{
		ID:        identity.NewID(),
		RoomID:    roomID,
		AgentID:   agentID,
		Rules:     normalized,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if existing, err := s.store.GetPolicy(ctx, roomID, agentID); err == nil {
		p.ID = existing.ID
		p.CreatedAt = existing.CreatedAt
	} else if !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	if err := s.store.UpsertPolicy(ctx, p); err != nil {
		return nil, nil, err
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventPolicyUpdated, room.PolicyUpdatedPayload{
		AgentID: agentID, RuleCount: len(normalized), PolicyID: p.ID,
	})
	if err != nil {
		return nil, nil, err
	}
	return p, event, nil
}

// Delete removes the policy for a room and Agent.
func (s *Service) Delete(ctx context.Context, roomID, agentID string, actor room.Actor) (*room.Event, error) {
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, roomID, agentID, actor); err != nil {
		return nil, err
	}
	if _, err := s.store.GetPolicy(ctx, roomID, agentID); err != nil {
		return nil, err
	}
	if err := s.store.DeletePolicy(ctx, roomID, agentID); err != nil {
		return nil, err
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventPolicyDeleted, room.PolicyUpdatedPayload{
		AgentID: agentID, Change: "deleted",
	})
	if err != nil {
		return nil, err
	}
	return event, nil
}

// Evaluate applies the room default policy plus the Agent's policy. A matching
// deny always wins; otherwise the first matching allow pre-authorizes the
// action. No match means a human approval is required.
func (s *Service) Evaluate(ctx context.Context, roomID, agentID string, action Action, at time.Time) (Decision, error) {
	policies, err := s.store.ListPolicies(ctx, roomID)
	if err != nil {
		return Decision{}, err
	}
	if at.IsZero() {
		at = s.now()
	}
	var allowed *Rule
	for i := range policies {
		p := policies[i]
		if p.AgentID != "" && p.AgentID != agentID {
			continue
		}
		for j := range p.Rules {
			rule := p.Rules[j]
			match, err := rule.matches(action, at)
			if err != nil {
				return Decision{}, fmt.Errorf("evaluate policy %s: %w", p.ID, err)
			}
			if !match {
				continue
			}
			if rule.Effect == EffectDeny {
				return Decision{
					Effect: EffectDeny, RuleID: rule.ID, AgentID: p.AgentID,
					Reason: "denied by rule " + ruleLabel(rule),
				}, nil
			}
			if rule.Effect == EffectAllow && allowed == nil {
				copy := rule
				allowed = &copy
			}
		}
	}
	if allowed != nil {
		return Decision{
			Effect: EffectAllow, RuleID: allowed.ID, AgentID: agentID,
			Reason: "allowed by rule " + ruleLabel(*allowed),
		}, nil
	}
	return Decision{Effect: EffectReview, AgentID: agentID, Reason: "no matching rule"}, nil
}

func ruleLabel(r Rule) string {
	if r.Note != "" {
		return r.Note
	}
	return r.ID
}

func (s *Service) authorize(ctx context.Context, roomID, agentID string, actor room.Actor) error {
	member, err := s.rooms.GetMember(ctx, roomID, actor.AccountID)
	if err != nil {
		return err
	}
	if member.Status != "active" {
		return ErrForbidden
	}
	if agentID == "" {
		if member.Role != "owner" {
			return ErrForbidden
		}
		return nil
	}
	ag, err := s.agents.Get(ctx, agentID)
	if err != nil {
		return err
	}
	if ag.AccountID != actor.AccountID && member.Role != "owner" {
		return ErrForbidden
	}
	inRoom, err := s.agents.IsInRoom(ctx, agentID, roomID)
	if err != nil {
		return err
	}
	if !inRoom {
		return ErrAgentNotInRoom
	}
	return nil
}

func normalizeRules(rules []Rule) ([]Rule, error) {
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("%w: at most %d rules", ErrInvalid, MaxRules)
	}
	out := make([]Rule, 0, len(rules))
	ids := make(map[string]bool, len(rules))
	for i, rule := range rules {
		normalized, err := normalizeRule(rule)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		if normalized.ID == "" {
			normalized.ID = identity.NewID()
		}
		if ids[normalized.ID] {
			return nil, fmt.Errorf("rule %d: duplicate id %q", i, normalized.ID)
		}
		ids[normalized.ID] = true
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeRule(r Rule) (Rule, error) {
	r.ID = strings.TrimSpace(r.ID)
	r.Effect = strings.ToLower(strings.TrimSpace(r.Effect))
	r.Kind = strings.TrimSpace(r.Kind)
	r.Tool = strings.TrimSpace(r.Tool)
	r.Target = strings.TrimSpace(r.Target)
	r.Recipient = strings.TrimSpace(r.Recipient)
	r.AmountMin = strings.TrimSpace(r.AmountMin)
	r.AmountMax = strings.TrimSpace(r.AmountMax)
	r.Currency = strings.TrimSpace(r.Currency)
	r.Start = strings.TrimSpace(r.Start)
	r.End = strings.TrimSpace(r.End)
	r.Timezone = strings.TrimSpace(r.Timezone)
	r.Note = strings.TrimSpace(r.Note)

	if r.Effect != EffectAllow && r.Effect != EffectDeny {
		return Rule{}, fmt.Errorf("%w: effect must be %q or %q", ErrInvalid, EffectAllow, EffectDeny)
	}
	if len(r.ID) > MaxRuleID {
		return Rule{}, fmt.Errorf("%w: id is too long", ErrInvalid)
	}
	for name, value := range map[string]string{
		"kind": r.Kind, "tool": r.Tool, "target": r.Target, "recipient": r.Recipient,
	} {
		if len(value) > MaxPattern {
			return Rule{}, fmt.Errorf("%w: %s is too long", ErrInvalid, name)
		}
	}
	if len(r.Note) > MaxNote {
		return Rule{}, fmt.Errorf("%w: note is too long", ErrInvalid)
	}
	if len(r.Currency) > 16 {
		return Rule{}, fmt.Errorf("%w: currency is too long", ErrInvalid)
	}
	if r.AmountMin != "" {
		if _, err := parseAmount(r.AmountMin); err != nil {
			return Rule{}, fmt.Errorf("%w: amount_min: %v", ErrInvalid, err)
		}
	}
	if r.AmountMax != "" {
		if _, err := parseAmount(r.AmountMax); err != nil {
			return Rule{}, fmt.Errorf("%w: amount_max: %v", ErrInvalid, err)
		}
	}
	if r.AmountMin != "" && r.AmountMax != "" {
		min, _ := parseAmount(r.AmountMin)
		max, _ := parseAmount(r.AmountMax)
		if min.Cmp(max) > 0 {
			return Rule{}, fmt.Errorf("%w: amount_min exceeds amount_max", ErrInvalid)
		}
	}
	if len(r.Weekdays) > 0 {
		seen := make(map[int]bool, len(r.Weekdays))
		for _, day := range r.Weekdays {
			if day < 0 || day > 6 {
				return Rule{}, fmt.Errorf("%w: weekday must be 0..6", ErrInvalid)
			}
			if seen[day] {
				return Rule{}, fmt.Errorf("%w: duplicate weekday %d", ErrInvalid, day)
			}
			seen[day] = true
		}
		sort.Ints(r.Weekdays)
	}
	if (r.Start == "") != (r.End == "") {
		return Rule{}, fmt.Errorf("%w: start and end must be set together", ErrInvalid)
	}
	if r.Start != "" {
		start, err := parseClock(r.Start)
		if err != nil {
			return Rule{}, fmt.Errorf("%w: start: %v", ErrInvalid, err)
		}
		end, err := parseClock(r.End)
		if err != nil {
			return Rule{}, fmt.Errorf("%w: end: %v", ErrInvalid, err)
		}
		if start == end {
			return Rule{}, fmt.Errorf("%w: empty time window", ErrInvalid)
		}
		if r.Timezone == "" {
			r.Timezone = DefaultTimezone
		}
	}
	if r.Timezone == "" {
		r.Timezone = DefaultTimezone
	}
	if len(r.Timezone) > MaxTimezone {
		return Rule{}, fmt.Errorf("%w: timezone is too long", ErrInvalid)
	}
	if _, err := loadLocation(r.Timezone); err != nil {
		return Rule{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if r.Effect == EffectAllow && noSelectors(r) {
		return Rule{}, fmt.Errorf("%w: allow rule needs at least one selector", ErrInvalid)
	}
	return r, nil
}

func noSelectors(r Rule) bool {
	return r.Kind == "" && r.Tool == "" && r.Target == "" && r.Recipient == "" &&
		r.AmountMin == "" && r.AmountMax == "" && r.Currency == "" &&
		len(r.Weekdays) == 0 && r.Start == ""
}

func (r Rule) matches(action Action, at time.Time) (bool, error) {
	if r.Kind != "" && !matchGlob(r.Kind, action.Kind, false) {
		return false, nil
	}
	if r.Tool != "" {
		if action.Tool == "" || !matchGlob(r.Tool, action.Tool, false) {
			return false, nil
		}
	}
	if r.Target != "" {
		if action.Target == "" || !matchGlob(r.Target, action.Target, false) {
			return false, nil
		}
	}
	if r.Recipient != "" {
		if action.Recipient == "" || !matchGlob(r.Recipient, action.Recipient, true) {
			return false, nil
		}
	}
	if r.Currency != "" {
		if action.Currency == "" || !matchGlob(r.Currency, action.Currency, true) {
			return false, nil
		}
	}
	if r.AmountMin != "" || r.AmountMax != "" {
		if action.Amount == "" {
			return false, nil
		}
		amount, err := parseAmount(action.Amount)
		if err != nil {
			return false, nil
		}
		if r.AmountMin != "" {
			min, _ := parseAmount(r.AmountMin)
			if amount.Cmp(min) < 0 {
				return false, nil
			}
		}
		if r.AmountMax != "" {
			max, _ := parseAmount(r.AmountMax)
			if amount.Cmp(max) > 0 {
				return false, nil
			}
		}
	}
	if len(r.Weekdays) > 0 || r.Start != "" {
		loc, err := loadLocation(r.Timezone)
		if err != nil {
			return false, err
		}
		local := at.In(loc)
		minutes := local.Hour()*60 + local.Minute()
		day := int(local.Weekday())
		if r.Start != "" {
			start, _ := parseClock(r.Start)
			end, _ := parseClock(r.End)
			if start < end {
				if minutes < start || minutes >= end {
					return false, nil
				}
			} else {
				if minutes >= end && minutes < start {
					return false, nil
				}
				// The after-midnight part belongs to the previous weekday.
				if minutes < end {
					day = (day + 6) % 7
				}
			}
		}
		if len(r.Weekdays) > 0 {
			found := false
			for _, want := range r.Weekdays {
				if want == day {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
	}
	return true, nil
}

func loadLocation(name string) (*time.Location, error) {
	if name == "" || name == DefaultTimezone {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	return loc, nil
}

func parseClock(value string) (int, error) {
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("must be HH:MM")
	}
	return t.Hour()*60 + t.Minute(), nil
}

func parseAmount(value string) (*big.Rat, error) {
	if len(value) > MaxAmount {
		return nil, fmt.Errorf("amount is too long")
	}
	if value == "" || value[0] == '-' || value[0] == '+' {
		return nil, fmt.Errorf("must be a non-negative decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, fmt.Errorf("must be a non-negative decimal")
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return nil, fmt.Errorf("must be a non-negative decimal")
			}
		}
	}
	if len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 18) {
		return nil, fmt.Errorf("must have at most 18 decimal places")
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return nil, fmt.Errorf("must not have leading zeros")
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, fmt.Errorf("must be a non-negative decimal")
	}
	return rat, nil
}

// matchGlob matches the whole value. "*" matches any sequence and "?" matches
// one character. It is deliberately small: no path separators or escapes.
func matchGlob(pattern, value string, fold bool) bool {
	if fold {
		pattern = strings.ToLower(pattern)
		value = strings.ToLower(value)
	}
	p := []rune(pattern)
	v := []rune(value)
	pi, vi := 0, 0
	star, mark := -1, 0
	for vi < len(v) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]) {
			pi++
			vi++
			continue
		}
		if pi < len(p) && p[pi] == '*' {
			star = pi
			mark = vi
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			mark++
			vi = mark
			continue
		}
		return false
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
