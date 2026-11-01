package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/bridge"
)

const (
	bridgeTestOrigin      = "https://toget.vu"
	bridgeTestAltOrigin   = "https://staging.toget.vu"
	bridgeTestLocalOrigin = "http://toget.localhost:7718"
	bridgeTestPassword    = "bridge-test-password"
)

type bridgeMachineBody struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	Listen  string `json:"listen"`
	Local   string `json:"local_url"`
	Home    string `json:"home"`
	Locked  bool   `json:"locked"`
	Account *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
	Agents int `json:"agents"`
}

type bridgeAgentBody struct {
	agent.Agent
	Rooms []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"rooms,omitempty"`
}

type bridgeDirectoryBody struct {
	Path     string `json:"path"`
	Relative string `json:"relative"`
	Parent   string `json:"parent"`
	Home     string `json:"home"`
	Roots    []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"roots"`
	Entries []struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Modified string `json:"modified"`
	} `json:"entries"`
	Truncated bool `json:"truncated"`
}

func newBridgeTestNode(t *testing.T) *testNode {
	t.Helper()
	return newBridgeTestNodeWith(t, nil)
}

func newBridgeTestNodeWith(t *testing.T, extra func(*api.Options)) *testNode {
	t.Helper()
	return newTestNodeWithOptions(t, nil, func(opts *api.Options) {
		manager, err := bridge.New("")
		if err != nil {
			t.Fatalf("bridge.New: %v", err)
		}
		opts.Bridge = manager
		opts.BridgeOrigins = []string{bridgeTestOrigin, bridgeTestAltOrigin}
		if extra != nil {
			extra(opts)
		}
	})
}

func bridgeAPIErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error %s: %v", body, err)
	}
	return out.Error.Code
}

func createBridgeTestAccount(t *testing.T, node *testNode) string {
	t.Helper()
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", node.token,
		fmt.Sprintf(`{"name":"Bridge Test","password":%q,"device_name":"Test node"}`, bridgeTestPassword),
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account: status = %d body = %s", resp.StatusCode, body)
	}
	var view struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if view.ID == "" {
		t.Fatalf("create account returned no id: %s", body)
	}
	return view.ID
}

func createBridgeTestCode(t *testing.T, node *testNode) (string, time.Time) {
	t.Helper()
	resp, body := node.request(t, http.MethodPost, "/api/v1/bridge/code", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create pairing code: status = %d body = %s", resp.StatusCode, body)
	}
	var out struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode pairing code: %v", err)
	}
	if len(out.Code) != 6 {
		t.Fatalf("pairing code = %q, want six digits", out.Code)
	}
	for _, r := range out.Code {
		if r < '0' || r > '9' {
			t.Fatalf("pairing code = %q, want digits", out.Code)
		}
	}
	if !out.ExpiresAt.After(time.Now()) || time.Until(out.ExpiresAt) > 6*time.Minute {
		t.Fatalf("pairing code expiry = %v", out.ExpiresAt)
	}
	return out.Code, out.ExpiresAt
}

func pairBridgeTestBrowser(t *testing.T, node *testNode, origin string) (string, bridge.Public) {
	t.Helper()
	code, _ := createBridgeTestCode(t, node)
	resp, body := node.request(t, http.MethodPost, "/api/v1/bridge/pair", "",
		fmt.Sprintf(`{"code":%q}`, code), map[string]string{"Origin": origin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair: status = %d body = %s", resp.StatusCode, body)
	}
	var out struct {
		Token   string            `json:"token"`
		Bridge  bridge.Public     `json:"bridge"`
		Machine bridgeMachineBody `json:"machine"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode pair response: %v", err)
	}
	if out.Token == "" {
		t.Fatalf("pair returned no token: %s", body)
	}
	if out.Bridge.Origin != origin {
		t.Fatalf("paired origin = %q, want %q", out.Bridge.Origin, origin)
	}
	return out.Token, out.Bridge
}

func bridgeRequest(t *testing.T, node *testNode, method, path, token, body string) (*http.Response, []byte) {
	t.Helper()
	return node.request(t, method, path, token, body, map[string]string{"Origin": bridgeTestOrigin})
}

func TestBridgeRequiresAnAccount(t *testing.T) {
	node := newBridgeTestNode(t)
	resp, body := node.request(t, http.MethodPost, "/api/v1/bridge/code", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("code without account: status = %d body = %s", resp.StatusCode, body)
	}
	if code := bridgeAPIErrorCode(t, body); code != "no_account" {
		t.Fatalf("code without account: error = %q", code)
	}
}

func TestBridgePairingAndMachine(t *testing.T) {
	node := newBridgeTestNode(t)
	accountID := createBridgeTestAccount(t, node)

	// The local pairing code must be requested from the local UI origin, not
	// from the public website.
	resp, body := node.request(t, http.MethodPost, "/api/v1/bridge/code", node.token, "",
		map[string]string{"Origin": bridgeTestOrigin})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("website origin requesting local code: status = %d body = %s", resp.StatusCode, body)
	}

	code, _ := createBridgeTestCode(t, node)

	resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/pair", "",
		fmt.Sprintf(`{"code":%q}`, code), map[string]string{"Origin": bridgeTestOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair: status = %d body = %s", resp.StatusCode, body)
	}
	var paired struct {
		Token   string            `json:"token"`
		Bridge  bridge.Public     `json:"bridge"`
		Machine bridgeMachineBody `json:"machine"`
	}
	if err := json.Unmarshal(body, &paired); err != nil {
		t.Fatalf("decode pair: %v", err)
	}
	if paired.Token == "" || paired.Bridge.ID == "" {
		t.Fatalf("pair returned an incomplete token: %s", body)
	}
	if paired.Bridge.Origin != bridgeTestOrigin {
		t.Fatalf("paired origin = %q", paired.Bridge.Origin)
	}
	if paired.Bridge.AccountID != accountID {
		t.Fatalf("paired account = %q, want %q", paired.Bridge.AccountID, accountID)
	}
	if paired.Machine.Account == nil || paired.Machine.Account.ID != accountID {
		t.Fatalf("pair machine account = %+v", paired.Machine.Account)
	}

	// A pairing code is single-use.
	resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/pair", "",
		fmt.Sprintf(`{"code":%q}`, code), map[string]string{"Origin": bridgeTestOrigin})
	if resp.StatusCode != http.StatusUnauthorized || bridgeAPIErrorCode(t, body) != "invalid_code" {
		t.Fatalf("reused code: status = %d body = %s", resp.StatusCode, body)
	}

	// Unknown and badly formatted codes are rejected.
	for _, payload := range []string{`{"code":"000000"}`, `{"code":"123"}`} {
		resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/pair", "", payload,
			map[string]string{"Origin": bridgeTestOrigin})
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad code %s: status = %d body = %s", payload, resp.StatusCode, body)
		}
	}

	// Pairing from an origin that is not allowlisted is refused.
	resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/pair", "", `{"code":"000000"}`,
		map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden || bridgeAPIErrorCode(t, body) != "origin_denied" {
		t.Fatalf("disallowed origin: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/pair", "", `{"code":"000000"}`, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing origin: status = %d body = %s", resp.StatusCode, body)
	}

	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/machine", paired.Token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("machine: status = %d body = %s", resp.StatusCode, body)
	}
	var machine bridgeMachineBody
	if err := json.Unmarshal(body, &machine); err != nil {
		t.Fatalf("decode machine: %v", err)
	}
	if machine.Name == "" || machine.OS != runtime.GOOS || machine.Arch != runtime.GOARCH {
		t.Fatalf("machine = %+v", machine)
	}
	if machine.Local != "http://toget.localhost:7718" {
		t.Fatalf("local_url = %q", machine.Local)
	}
	if machine.Locked {
		t.Fatal("new account should leave the node unlocked")
	}
	if machine.Account == nil || machine.Account.ID != accountID || machine.Account.Name != "Bridge Test" {
		t.Fatalf("machine account = %+v", machine.Account)
	}
	if machine.Agents != 0 {
		t.Fatalf("machine agents = %d, want 0", machine.Agents)
	}
	if strings.Contains(string(body), "vault") || strings.Contains(string(body), "token") {
		t.Fatalf("machine response leaks internals: %s", body)
	}

	// The light session endpoint does not need the vault, so it keeps working
	// while locked; it only reveals that the node is locked.
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/session", paired.Token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session: status = %d body = %s", resp.StatusCode, body)
	}

	// A token is bound to the origin it was paired with.
	resp, body = node.request(t, http.MethodGet, "/api/v1/bridge/machine", paired.Token, "",
		map[string]string{"Origin": bridgeTestAltOrigin})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token used from another origin: status = %d body = %s", resp.StatusCode, body)
	}

	// An unpaired token is rejected.
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/machine", "not-a-token", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired token: status = %d body = %s", resp.StatusCode, body)
	}

	// The local API token is not a website token.
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/machine", node.token, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("local token on bridge route: status = %d body = %s", resp.StatusCode, body)
	}

	// Browser tokens are listed and revoked from the local UI only.
	resp, body = node.request(t, http.MethodGet, "/api/v1/bridge/browsers", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list browsers: status = %d body = %s", resp.StatusCode, body)
	}
	var browsers struct {
		Browsers []bridge.Public `json:"browsers"`
	}
	if err := json.Unmarshal(body, &browsers); err != nil {
		t.Fatalf("decode browsers: %v", err)
	}
	if len(browsers.Browsers) != 1 || browsers.Browsers[0].ID != paired.Bridge.ID {
		t.Fatalf("browsers = %+v", browsers.Browsers)
	}
	if strings.Contains(string(body), "hash") {
		t.Fatalf("browser list leaks token hashes: %s", body)
	}
	resp, body = node.request(t, http.MethodDelete, "/api/v1/bridge/browsers/"+paired.Bridge.ID,
		node.token, "", map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke browser: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/machine", paired.Token, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodDelete, "/api/v1/bridge/browsers/missing",
		node.token, "", map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke missing browser: status = %d body = %s", resp.StatusCode, body)
	}
}

func TestBridgeCORSPreflight(t *testing.T) {
	node := newBridgeTestNode(t)
	createBridgeTestAccount(t, node)

	req, err := http.NewRequest(http.MethodOptions, node.url+"/api/v1/bridge/machine", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", bridgeTestOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "Authorization")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != bridgeTestOrigin {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("Allow-Private-Network = %q", got)
	}

	req, err = http.NewRequest(http.MethodOptions, node.url+"/api/v1/bridge/machine", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied preflight: status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("denied preflight Allow-Origin = %q", got)
	}
}

func TestBridgeAgentLifecycle(t *testing.T) {
	node := newBridgeTestNode(t)
	createBridgeTestAccount(t, node)
	token, _ := pairBridgeTestBrowser(t, node, bridgeTestOrigin)

	resp, body := bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/agents", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list agents: status = %d body = %s", resp.StatusCode, body)
	}
	var list struct {
		Agents []bridgeAgentBody `json:"agents"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if list.Agents == nil || len(list.Agents) != 0 {
		t.Fatalf("agents = %#v, want an empty list", list.Agents)
	}

	resp, body = bridgeRequest(t, node, http.MethodPost, "/api/v1/bridge/agents", token,
		`{"name":"Builder","runtime":"claude","permissions":{"tools":["read"]}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent: status = %d body = %s", resp.StatusCode, body)
	}
	var created bridgeAgentBody
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created agent: %v", err)
	}
	if created.ID == "" || created.Name != "Builder" || created.Runtime != "claude" {
		t.Fatalf("created agent = %+v", created.Agent)
	}
	if string(created.Permissions) != `{"tools":["read"]}` {
		t.Fatalf("created permissions = %s", created.Permissions)
	}

	resp, body = bridgeRequest(t, node, http.MethodPatch, "/api/v1/bridge/agents/"+created.ID, token,
		`{"name":"Builder Two","runtime":"codex","permissions":{"tools":["read","write"]}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update agent: status = %d body = %s", resp.StatusCode, body)
	}
	var updated bridgeAgentBody
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatalf("decode updated agent: %v", err)
	}
	if updated.Name != "Builder Two" || updated.Runtime != "codex" {
		t.Fatalf("updated agent = %+v", updated.Agent)
	}
	if string(updated.Permissions) != `{"tools":["read","write"]}` {
		t.Fatalf("updated permissions = %s", updated.Permissions)
	}

	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/agents", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list agents: status = %d body = %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if len(list.Agents) != 1 || list.Agents[0].ID != created.ID || list.Agents[0].Name != "Builder Two" {
		t.Fatalf("agents = %+v", list.Agents)
	}

	// Machine stats reflect the Agent count.
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/machine", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("machine: status = %d body = %s", resp.StatusCode, body)
	}
	var machine bridgeMachineBody
	if err := json.Unmarshal(body, &machine); err != nil {
		t.Fatalf("decode machine: %v", err)
	}
	if machine.Agents != 1 {
		t.Fatalf("machine agents = %d, want 1", machine.Agents)
	}

	// The same settings are editable from the local UI.
	resp, body = node.request(t, http.MethodPost, "/api/v1/agents/"+created.ID+"/settings",
		node.token, `{"name":"Local Rename","runtime":"claude","permissions":{}}`,
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local settings: status = %d body = %s", resp.StatusCode, body)
	}
	var localUpdate struct {
		Agent agent.Agent `json:"agent"`
	}
	if err := json.Unmarshal(body, &localUpdate); err != nil {
		t.Fatalf("decode local settings: %v", err)
	}
	if localUpdate.Agent.Name != "Local Rename" || localUpdate.Agent.Runtime != "claude" {
		t.Fatalf("local updated agent = %+v", localUpdate.Agent)
	}

	// Locking the node disables everything that needs account authority.
	resp, body = node.request(t, http.MethodDelete, "/api/v1/session", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock: status = %d body = %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/api/v1/bridge/machine", "/api/v1/bridge/agents", "/api/v1/bridge/directory"} {
		resp, body = bridgeRequest(t, node, http.MethodGet, path, token, "")
		if resp.StatusCode != http.StatusLocked {
			t.Fatalf("%s while locked: status = %d body = %s", path, resp.StatusCode, body)
		}
	}
	resp, body = bridgeRequest(t, node, http.MethodPost, "/api/v1/bridge/agents", token,
		`{"name":"Locked","runtime":"claude"}`)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("create agent while locked: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/session", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session while locked: status = %d body = %s", resp.StatusCode, body)
	}
	var session struct {
		Machine bridgeMachineBody `json:"machine"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !session.Machine.Locked {
		t.Fatal("session should report a locked machine")
	}

	// Unlock and finish the lifecycle.
	resp, body = node.request(t, http.MethodPost, "/api/v1/session", node.token,
		fmt.Sprintf(`{"password":%q}`, bridgeTestPassword),
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = bridgeRequest(t, node, http.MethodPost, "/api/v1/bridge/agents/"+created.ID+"/revoke", token, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke agent: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/agents", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list agents after revoke: status = %d body = %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	// Revoked Agents stay in the list for history, marked as revoked.
	if len(list.Agents) != 1 || list.Agents[0].ID != created.ID || list.Agents[0].RevokedAt == nil {
		t.Fatalf("agents after revoke = %+v", list.Agents)
	}
	resp, body = bridgeRequest(t, node, http.MethodPatch, "/api/v1/bridge/agents/"+created.ID, token,
		`{"name":"Gone","runtime":"claude"}`)
	if resp.StatusCode != http.StatusConflict || bridgeAPIErrorCode(t, body) != "agent_revoked" {
		t.Fatalf("update revoked agent: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = bridgeRequest(t, node, http.MethodPost, "/api/v1/bridge/agents/missing/revoke", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke missing agent: status = %d body = %s", resp.StatusCode, body)
	}
}

func TestBridgeDirectoryStaysInsideHome(t *testing.T) {
	node := newBridgeTestNode(t)
	createBridgeTestAccount(t, node)
	token, _ := pairBridgeTestBrowser(t, node, bridgeTestOrigin)

	resp, body := bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/directory", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory: status = %d body = %s", resp.StatusCode, body)
	}
	var directory bridgeDirectoryBody
	if err := json.Unmarshal(body, &directory); err != nil {
		t.Fatalf("decode directory: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("home directory unavailable: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if directory.Home != home || directory.Path != home || directory.Relative != "" {
		t.Fatalf("directory root = %+v, want home %q", directory, home)
	}
	if directory.Parent != "" {
		t.Fatalf("home parent = %q, want empty", directory.Parent)
	}
	for _, entry := range directory.Entries {
		if strings.HasPrefix(entry.Name, ".") {
			t.Fatalf("directory listed hidden entry %q", entry.Name)
		}
		if !strings.HasPrefix(entry.Path, home+string(filepath.Separator)) {
			t.Fatalf("entry %q is outside home", entry.Path)
		}
		if entry.Modified == "" {
			t.Fatalf("entry %q has no modified time", entry.Name)
		}
	}
	for _, root := range directory.Roots {
		if !strings.HasPrefix(root.Path, home) {
			t.Fatalf("shortcut %q is outside home", root.Path)
		}
	}

	for _, target := range []string{"/etc", "..", "%2e%2e", "Documents/../.."} {
		resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/directory?path="+target, token, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("directory %q: status = %d body = %s", target, resp.StatusCode, body)
		}
		if code := bridgeAPIErrorCode(t, body); code != "directory_denied" {
			t.Fatalf("directory %q: error = %q", target, code)
		}
	}
	resp, body = bridgeRequest(t, node, http.MethodGet, "/api/v1/bridge/directory?path=definitely-not-a-folder", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing directory: status = %d body = %s", resp.StatusCode, body)
	}

	// The local UI has the same read-only browser, behind the local token.
	resp, body = node.request(t, http.MethodGet, "/api/v1/machine/directory", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local directory: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodGet, "/api/v1/machine", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local machine: status = %d body = %s", resp.StatusCode, body)
	}
}

func TestBridgeDisabled(t *testing.T) {
	node := newTestNode(t, nil)
	createBridgeTestAccount(t, node)
	resp, body := node.request(t, http.MethodGet, "/api/v1/bridge/info", "", "",
		map[string]string{"Origin": bridgeTestOrigin})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("bridge info without bridge: status = %d body = %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/bridge/code", node.token, "",
		map[string]string{"Origin": bridgeTestLocalOrigin})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("bridge code without bridge: status = %d body = %s", resp.StatusCode, body)
	}
}
