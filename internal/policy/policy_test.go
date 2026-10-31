package policy

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memStore struct{ policies []Policy }

func (m *memStore) GetPolicy(_ context.Context, roomID, agentID string) (*Policy, error) {
	for i := range m.policies {
		if m.policies[i].RoomID == roomID && m.policies[i].AgentID == agentID {
			return &m.policies[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *memStore) ListPolicies(_ context.Context, roomID string) ([]Policy, error) {
	var out []Policy
	for _, p := range m.policies {
		if p.RoomID == roomID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memStore) UpsertPolicy(context.Context, *Policy) error        { return nil }
func (m *memStore) DeletePolicy(context.Context, string, string) error { return nil }

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, value string
		fold, want     bool
	}{
		{"*", "anything", false, true},
		{"*.example.com", "mail.example.com", false, true},
		{"*.example.com", "example.com", false, false},
		{"alice@*", "Alice@Example.com", true, true},
		{"a?c", "abc", false, true},
		{"a?c", "ac", false, false},
		{"*middle*", "left-middle-right", false, true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.value, c.fold); got != c.want {
			t.Errorf("matchGlob(%q,%q,fold=%v) = %v, want %v", c.pattern, c.value, c.fold, got, c.want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	for _, good := range []string{"0", "0.00", "12", "12.5", "12.50", "999999999999999999.123456789012345678"} {
		if _, err := parseAmount(good); err != nil {
			t.Errorf("parseAmount(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", "-1", "+1", "1e3", "01", ".5", "1.", "1.1234567890123456789", " 1"} {
		if _, err := parseAmount(bad); err == nil {
			t.Errorf("parseAmount(%q) succeeded", bad)
		}
	}
}

func TestRuleMatchesAmountAndRecipient(t *testing.T) {
	rule := Rule{Effect: EffectAllow, Tool: "mail.*", Recipient: "*@example.com",
		AmountMin: "1", AmountMax: "100.50", Currency: "usd"}
	action := Action{Kind: "tool.call", Tool: "mail.send", Recipient: "Alice@Example.COM",
		Amount: "100.50", Currency: "USD"}
	ok, err := rule.matches(action, time.Now())
	if err != nil || !ok {
		t.Fatalf("match = %v, %v", ok, err)
	}
	for _, mutate := range []func(*Action){
		func(a *Action) { a.Amount = "100.51" },
		func(a *Action) { a.Amount = "0.99" },
		func(a *Action) { a.Amount = "1,000" },
		func(a *Action) { a.Currency = "EUR" },
		func(a *Action) { a.Recipient = "bob@other.com" },
		func(a *Action) { a.Tool = "chat.send" },
	} {
		next := action
		mutate(&next)
		if ok, _ := rule.matches(next, time.Now()); ok {
			t.Errorf("rule unexpectedly matched %#v", next)
		}
	}
}

func TestRuleMatchesWeekdayAndWindow(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("timezone database unavailable")
	}
	rule := Rule{Effect: EffectAllow, Start: "09:00", End: "18:00",
		Weekdays: []int{5}, Timezone: "Asia/Shanghai"}
	friday := time.Date(2026, 10, 30, 17, 59, 0, 0, shanghai)
	if ok, err := rule.matches(Action{Kind: "tool.call"}, friday); err != nil || !ok {
		t.Fatalf("friday window = %v, %v", ok, err)
	}
	if ok, _ := rule.matches(Action{Kind: "tool.call"}, friday.Add(time.Minute)); ok {
		t.Fatal("window end is inclusive")
	}
	if ok, _ := rule.matches(Action{Kind: "tool.call"}, friday.Add(24*time.Hour)); ok {
		t.Fatal("saturday matched a friday rule")
	}

	cross := Rule{Effect: EffectAllow, Start: "22:00", End: "02:00",
		Weekdays: []int{5}, Timezone: "Asia/Shanghai"}
	saturdayOne := time.Date(2026, 10, 31, 1, 0, 0, 0, shanghai)
	if ok, err := cross.matches(Action{Kind: "tool.call"}, saturdayOne); err != nil || !ok {
		t.Fatalf("cross-midnight = %v, %v", ok, err)
	}
	saturdayThree := time.Date(2026, 10, 31, 3, 0, 0, 0, shanghai)
	if ok, _ := cross.matches(Action{Kind: "tool.call"}, saturdayThree); ok {
		t.Fatal("cross-midnight window should have ended")
	}
}

func TestNormalizeRulesRejectsAmbiguousAllow(t *testing.T) {
	if _, err := normalizeRules([]Rule{{Effect: EffectAllow}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank allow = %v", err)
	}
	if _, err := normalizeRules([]Rule{{Effect: "maybe"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad effect = %v", err)
	}
	if _, err := normalizeRules([]Rule{{Effect: EffectDeny, Start: "09:00"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial window = %v", err)
	}
	if _, err := normalizeRules([]Rule{{Effect: EffectDeny, Timezone: "Mars/Olympus"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad timezone = %v", err)
	}
	if _, err := normalizeRules([]Rule{{Effect: EffectDeny, AmountMin: "10", AmountMax: "1"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inverted amounts = %v", err)
	}
}

func TestEvaluateDenyWinsAndAgentPolicyMerges(t *testing.T) {
	store := &memStore{policies: []Policy{
		{RoomID: "room", AgentID: "", Rules: []Rule{
			{ID: "default-allow", Effect: EffectAllow, Kind: "tool.call"},
		}},
		{RoomID: "room", AgentID: "agent", Rules: []Rule{
			{ID: "agent-deny", Effect: EffectDeny, Tool: "shell"},
		}},
	}}
	svc := New(store, nil, nil)
	at := time.Now()

	decision, err := svc.Evaluate(context.Background(), "room", "agent", Action{Kind: "tool.call", Tool: "shell"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Effect != EffectDeny || decision.RuleID != "agent-deny" {
		t.Fatalf("deny wins: %#v", decision)
	}
	decision, err = svc.Evaluate(context.Background(), "room", "agent", Action{Kind: "tool.call", Tool: "read"}, at)
	if err != nil || decision.Effect != EffectAllow || decision.RuleID != "default-allow" {
		t.Fatalf("default allow: %#v %v", decision, err)
	}
	decision, err = svc.Evaluate(context.Background(), "room", "other", Action{Kind: "tool.call", Tool: "shell"}, at)
	if err != nil || decision.Effect != EffectAllow {
		t.Fatalf("agent policy leaked: %#v %v", decision, err)
	}
	decision, err = svc.Evaluate(context.Background(), "room", "agent", Action{Kind: "payment"}, at)
	if err != nil || decision.Effect != EffectReview {
		t.Fatalf("default review: %#v %v", decision, err)
	}
}
