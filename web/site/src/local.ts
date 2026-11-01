// Client for the Toget node running on this computer. The public website is
// served from https://toget.vu, so every call here is cross-origin to
// http://localhost:7718. The local node answers only origins it trusts and
// requires a bearer token issued by a one-time pairing code.

const BASE_URL = "http://localhost:7718";
const TOKEN_KEY = "toget.site.localBridge.v1";
const TIMEOUT_MS = 7000;

export interface BridgeMachine {
  name: string;
  os: string;
  arch: string;
  version: string;
  listen: string;
  local_url: string;
  home?: string;
  locked: boolean;
  account?: { id: string; name: string };
  agents: number;
}

export interface BridgeRoom {
  id: string;
  name: string;
}

export interface BridgeAgent {
  id: string;
  account_id: string;
  name: string;
  runtime: string;
  status: string;
  permissions: Record<string, boolean>;
  created_at: string;
  last_seen_at?: string;
  revoked_at?: string;
  rooms?: BridgeRoom[];
}

export interface BridgeDirectoryEntry {
  name: string;
  path: string;
  modified?: string;
}

export interface BridgeDirectory {
  path: string;
  relative: string;
  parent?: string;
  home: string;
  roots: { name: string; path: string }[];
  entries: BridgeDirectoryEntry[];
  truncated?: boolean;
}

export interface SavedBridge {
  token: string;
  machine: BridgeMachine;
  paired_at: string;
}

export class BridgeError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "BridgeError";
    this.status = status;
    this.code = code;
  }
}

function friendlyNetworkError(): BridgeError {
  return new BridgeError(
    0,
    "unreachable",
    "没有连上本机的 Toget。请确认电脑上已经运行 `toget serve`，再试一次。",
  );
}

async function request<T>(
  path: string,
  token: string | undefined,
  init: RequestInit = {},
): Promise<T> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), TIMEOUT_MS);
  let response: Response;
  try {
    response = await fetch(`${BASE_URL}${path}`, {
      ...init,
      cache: "no-store",
      credentials: "omit",
      signal: controller.signal,
      headers: {
        Accept: "application/json",
        ...(init.body ? { "Content-Type": "application/json" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(init.headers ?? {}),
      },
    });
  } catch {
    throw friendlyNetworkError();
  } finally {
    window.clearTimeout(timer);
  }

  if (response.status === 204) {
    return undefined as T;
  }
  const body = await response.text();
  let parsed: unknown = null;
  if (body) {
    try {
      parsed = JSON.parse(body);
    } catch {
      parsed = null;
    }
  }
  if (!response.ok) {
    const error = (parsed as { error?: { code?: string; message?: string } } | null)
      ?.error;
    throw new BridgeError(
      response.status,
      error?.code ?? "request_failed",
      error?.message ?? `本机返回了错误（${response.status}）。`,
    );
  }
  return parsed as T;
}

export function loadBridge(): SavedBridge | null {
  try {
    const raw = localStorage.getItem(TOKEN_KEY);
    if (!raw) {
      return null;
    }
    const parsed = JSON.parse(raw) as Partial<SavedBridge>;
    if (!parsed.token || !parsed.machine) {
      return null;
    }
    return parsed as SavedBridge;
  } catch {
    return null;
  }
}

export function saveBridge(token: string, machine: BridgeMachine): SavedBridge {
  const saved: SavedBridge = {
    token,
    machine,
    paired_at: new Date().toISOString(),
  };
  try {
    localStorage.setItem(TOKEN_KEY, JSON.stringify(saved));
  } catch {
    // Private browsing may block storage; keep the connection for this tab.
  }
  return saved;
}

export function forgetBridge() {
  try {
    localStorage.removeItem(TOKEN_KEY);
  } catch {
    // Nothing else to clear.
  }
}

export async function probeLocal(signal?: AbortSignal): Promise<boolean> {
  try {
    const response = await fetch(`${BASE_URL}/api/v1/bridge/info`, {
      cache: "no-store",
      credentials: "omit",
      signal,
    });
    if (!response.ok) {
      return false;
    }
    const body = (await response.json()) as { app?: string; bridge?: boolean };
    return body.app === "toget" && body.bridge === true;
  } catch {
    return false;
  }
}

export async function pairLocal(code: string): Promise<SavedBridge> {
  const result = await request<{
    token: string;
    machine: BridgeMachine;
  }>("/api/v1/bridge/pair", undefined, {
    method: "POST",
    body: JSON.stringify({ code }),
  });
  return saveBridge(result.token, result.machine);
}

export function session(token: string) {
  return request<{ machine: BridgeMachine }>("/api/v1/bridge/session", token);
}

export function machine(token: string) {
  return request<BridgeMachine>("/api/v1/bridge/machine", token);
}

export function directory(token: string, path?: string) {
  const query = path ? `?path=${encodeURIComponent(path)}` : "";
  return request<BridgeDirectory>(`/api/v1/bridge/directory${query}`, token);
}

export function agents(token: string) {
  return request<{ agents: BridgeAgent[] }>("/api/v1/bridge/agents", token);
}

export function createAgent(
  token: string,
  name: string,
  runtime: string,
  permissions: Record<string, boolean>,
) {
  return request<BridgeAgent>("/api/v1/bridge/agents", token, {
    method: "POST",
    body: JSON.stringify({ name, runtime, permissions }),
  });
}

export function updateAgent(
  token: string,
  id: string,
  name: string,
  runtime: string,
  permissions: Record<string, boolean>,
) {
  return request<BridgeAgent>(`/api/v1/bridge/agents/${encodeURIComponent(id)}`, token, {
    method: "PATCH",
    body: JSON.stringify({ name, runtime, permissions }),
  });
}

export function revokeAgent(token: string, id: string) {
  return request<void>(
    `/api/v1/bridge/agents/${encodeURIComponent(id)}/revoke`,
    token,
    { method: "POST" },
  );
}

export async function unpairLocal(token: string): Promise<void> {
  try {
    await request<void>("/api/v1/bridge/unpair", token, { method: "POST" });
  } catch {
    // Forgetting the local copy is still useful if the node is offline.
  }
  forgetBridge();
}

export const LOCAL_UI_URL = "http://toget.localhost:7718";
export const INSTALL_COMMAND = "curl -fsSL https://toget.vu/install.sh | sh";
