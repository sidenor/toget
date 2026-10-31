package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/tool"
)

func TestPolicyAPI(t *testing.T) {
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"General"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	created := decode[struct {
		Room room.Room `json:"room"`
	}](t, body)
	roomID := created.Room.ID

	resp, body = node.request(t, http.MethodPost, "/api/v1/agents", testToken,
		`{"name":"Claude","runtime":"claude-code","permissions":{}}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent = %d %s", resp.StatusCode, body)
	}
	agent := decode[struct {
		ID string `json:"id"`
	}](t, body)
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/agents", testToken,
		`{"agent_id":"`+agent.ID+`"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("join agent = %d %s", resp.StatusCode, body)
	}

	rules := `{"agent_id":"` + agent.ID + `","rules":[
		{"effect":"allow","kind":"tool.call","tool":"mail.send","amount_max":"50","currency":"USD","note":"small mail"},
		{"effect":"deny","kind":"tool.call","tool":"shell.*","note":"no shell"}]}`
	resp, body = node.request(t, http.MethodPut, "/api/v1/rooms/"+roomID+"/policies", testToken, rules, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put policy = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodGet, "/api/v1/rooms/"+roomID+"/policies", testToken, "", nil)
	if resp.StatusCode != http.StatusOK || !json.Valid(body) {
		t.Fatalf("list policy = %d %s", resp.StatusCode, body)
	}
	list := decode[struct {
		Policies []struct {
			AgentID string `json:"agent_id"`
			Rules   []struct {
				ID string `json:"id"`
			} `json:"rules"`
		} `json:"policies"`
	}](t, body)
	if len(list.Policies) != 1 || len(list.Policies[0].Rules) != 2 || list.Policies[0].Rules[0].ID == "" {
		t.Fatalf("policy list = %s", body)
	}

	evaluate := func(action string) (int, string) {
		resp, body := node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/policies/evaluate", testToken,
			`{"agent_id":"`+agent.ID+`","action":`+action+`}`, nil)
		result := decode[struct {
			Effect string `json:"effect"`
		}](t, body)
		return resp.StatusCode, result.Effect
	}
	if status, effect := evaluate(`{"kind":"tool.call","tool":"mail.send","amount":"50","currency":"usd"}`); status != http.StatusOK || effect != "allow" {
		t.Fatalf("evaluate allow = %d %q", status, effect)
	}
	if status, effect := evaluate(`{"kind":"tool.call","tool":"shell.exec"}`); status != http.StatusOK || effect != "deny" {
		t.Fatalf("evaluate deny = %d %q", status, effect)
	}
	if status, effect := evaluate(`{"kind":"tool.call","tool":"mail.send","amount":"51","currency":"USD"}`); status != http.StatusOK || effect != "approval" {
		t.Fatalf("evaluate review = %d %q", status, effect)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/tools", testToken,
		`{"agent_id":"`+agent.ID+`","kind":"external","tool":"mail.send","input":{"amount":"20","currency":"USD"}}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("allowed tool = %d %s", resp.StatusCode, body)
	}
	receipt := decode[struct {
		Receipt tool.Receipt `json:"receipt"`
	}](t, body)
	if receipt.Receipt.PolicyID == "" {
		t.Fatalf("policy receipt = %s", body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/tools", testToken,
		`{"agent_id":"`+agent.ID+`","kind":"shell","tool":"shell.exec","input":{}}`, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied tool = %d %s", resp.StatusCode, body)
	}
	if got := decode[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, body); got.Error.Code != "policy_denied" {
		t.Fatalf("denied error = %s", body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/tools", testToken,
		`{"agent_id":"`+agent.ID+`","kind":"external","tool":"calendar.create","input":{}}`, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unmatched tool = %d %s", resp.StatusCode, body)
	}
	if got := decode[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, body); got.Error.Code != "approval_required" {
		t.Fatalf("unmatched error = %s", body)
	}

	resp, _ = node.request(t, http.MethodPut, "/api/v1/rooms/"+roomID+"/policies", testToken,
		`{"agent_id":"`+agent.ID+`","rules":[{"effect":"allow"}]}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy = %d, want 400", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodDelete, "/api/v1/rooms/"+roomID+"/policies?agent_id="+agent.ID, testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete policy = %d, want 200", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/api/v1/rooms/"+roomID+"/policies", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list after delete = %d", resp.StatusCode)
	}
}
