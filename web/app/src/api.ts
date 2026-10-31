import type {
  Account,
  AccountSyncResult,
  Agent,
  AgentSession,
  AgentStream,
  Approval,
  ApprovalAction,
  Attachment,
  CardView,
  AuditResponse,
  CreateRoomResponse,
  Device,
  Invite,
  Member,
  DiscoveredPeer,
  NodeInfo,
  PairingView,
  PairJoinView,
  RecoveryCredential,
  RecoveryStatus,
  Room,
  RoomDevice,
  RoomEvent,
  RotateAccountKeyResult,
  SearchHit,
  SessionInfo,
  Status,
  ToolReceipt,
} from "./types";

export class APIError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: {
      ...(init?.body && !(init.body instanceof Blob) ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    const envelope = data as { error?: { code?: string; message?: string } } | null;
    throw new APIError(
      response.status,
      String(envelope?.error?.code ?? ""),
      String(envelope?.error?.message ?? "") || `Request failed (${response.status})`,
    );
  }
  return data as T;
}

function json(body: unknown): RequestInit {
  return { method: "POST", body: JSON.stringify(body) };
}

export const api = {
  status: () => request<Status>("/api/v1/status"),
  session: () => request<SessionInfo>("/api/v1/session"),
  unlock: (password: string) => request<SessionInfo>("/api/v1/session", json({ password })),
  lock: () => request<void>("/api/v1/session", { method: "DELETE" }),

  createAccount: (name: string, password: string, deviceName?: string) =>
    request<Account>("/api/v1/account", json({ name, password, device_name: deviceName })),
  account: () => request<Account>("/api/v1/account"),
  recovery: () => request<RecoveryStatus>("/api/v1/account/recovery"),
  createRecovery: () => request<RecoveryCredential>("/api/v1/account/recovery", json({})),
  removeRecovery: () => request<void>("/api/v1/account/recovery", { method: "DELETE" }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<SessionInfo>("/api/v1/account/change-password", json({ current_password: currentPassword, new_password: newPassword })),
  resetPassword: (credential: string, newPassword: string) =>
    request<SessionInfo>("/api/v1/account/reset-password", json({ credential, new_password: newPassword })),
  resetPasswordLocal: (newPassword: string) =>
    request<SessionInfo>("/api/v1/account/reset-password", json({ local: true, new_password: newPassword })),

  rooms: () => request<{ rooms: Room[] }>("/api/v1/rooms"),
  createRoom: (name: string, topic = "") => request<CreateRoomResponse>("/api/v1/rooms", json({ name, topic })),
  room: (id: string) => request<Room>(`/api/v1/rooms/${encodeURIComponent(id)}`),
  updateRoom: (id: string, patch: { name?: string; topic?: string }) =>
    request<{ room: Room; event: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    }),
  archiveRoom: (id: string) =>
    request<{ room: Room; event?: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(id)}/archive`, { method: "POST" }),

  events: (roomID: string, after = 0, limit = 500, type?: string) => {
    const params = new URLSearchParams({ after: String(after), limit: String(limit) });
    if (type) params.set("type", type);
    return request<{ events: RoomEvent[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/events?${params}`);
  },
  send: (
    roomID: string,
    body: { text: string; format?: string; attachments?: string[]; mentions?: string[]; reply_to?: string },
  ) =>
    request<RoomEvent>(`/api/v1/rooms/${encodeURIComponent(roomID)}/messages`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  editMessage: (roomID: string, eventID: string, text: string) =>
    request<RoomEvent>(`/api/v1/rooms/${encodeURIComponent(roomID)}/messages/${encodeURIComponent(eventID)}`, {
      method: "PATCH",
      body: JSON.stringify({ text }),
    }),

  members: (roomID: string) =>
    request<{ members: Member[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/members`),
  roomDevices: (roomID: string) =>
    request<{ devices: RoomDevice[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/devices`),
  createInvite: (roomID: string, history: string) =>
    request<{ invite: Invite; event: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/invites`, json({ history })),
  redeemInvite: (secret: string) =>
    request<{ room: Room; member: Member; event?: RoomEvent | null }>("/api/v1/invites/redeem", json({ secret })),
  node: () => request<{ enabled: boolean; info?: NodeInfo }>("/api/v1/node"),
  nodeCard: (label = "") =>
    request<{ card: CardView }>(`/api/v1/card${label ? `?label=${encodeURIComponent(label)}` : ""}`),
  nodeCards: () => request<{ cards: CardView[] }>("/api/v1/cards"),
  addNodeCard: (card: string, label = "") =>
    request<{ card: CardView }>("/api/v1/cards", json({ card, label })),
  removeNodeCard: (id: string) =>
    request<void>(`/api/v1/cards/${encodeURIComponent(id)}`, { method: "DELETE" }),
  nodeDiscover: () =>
    request<{ enabled: boolean; peers: DiscoveredPeer[] }>("/api/v1/node/discover"),
  syncAllRooms: () => request<{ ok: boolean }>("/api/v1/node/sync", { method: "POST" }),
  syncRoom: (roomID: string) =>
    request<{ imported: number }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/sync`, { method: "POST" }),
  leaveRoom: (roomID: string) =>
    request<RoomEvent>(`/api/v1/rooms/${encodeURIComponent(roomID)}/leave`, { method: "POST" }),
  unread: (roomID: string) =>
    request<{ unread: number }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/unread`),
  markRead: (roomID: string, seq: number) =>
    request<{ seq: number }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/read`, json({ seq })),

  agents: () => request<{ agents: Agent[] }>("/api/v1/agents"),
  createAgent: (name: string, runtime: string, permissions: Record<string, unknown>) =>
    request<Agent>("/api/v1/agents", json({ name, runtime, permissions })),
  agent: (id: string) => request<Agent>(`/api/v1/agents/${encodeURIComponent(id)}`),
  setAgentStatus: (id: string, status: string, roomID = "", detail = "") =>
    request<{ agent: Agent; event?: RoomEvent }>(`/api/v1/agents/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify({ status, room_id: roomID, detail }),
    }),
  revokeAgent: (id: string) =>
    request<void>(`/api/v1/agents/${encodeURIComponent(id)}/revoke`, { method: "POST" }),
  agentRooms: (id: string) =>
    request<{ rooms: { room_id: string; joined_at: string }[] }>(`/api/v1/agents/${encodeURIComponent(id)}/rooms`),
  agentSessions: (id: string) =>
    request<{ sessions: AgentSession[] }>(`/api/v1/agents/${encodeURIComponent(id)}/sessions`),
  stopAgent: (id: string) =>
    request<{ stopped: boolean }>(`/api/v1/agents/${encodeURIComponent(id)}/stop`, { method: "POST" }),
  resetAgentSession: (id: string, roomID: string) =>
    request<AgentSession>(
      `/api/v1/agents/${encodeURIComponent(id)}/sessions/${encodeURIComponent(roomID)}`,
      { method: "DELETE" },
    ),
  roomAgents: (roomID: string) =>
    request<{ agents: Agent[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/agents`),
  agentStreams: (roomID: string) =>
    request<{ streams: AgentStream[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/agent-streams`),
  joinAgent: (roomID: string, agentID: string) =>
    request<{ agent: Agent; event: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/agents`, json({ agent_id: agentID })),
  leaveAgent: (roomID: string, agentID: string) =>
    request<RoomEvent>(`/api/v1/rooms/${encodeURIComponent(roomID)}/agents/${encodeURIComponent(agentID)}`, {
      method: "DELETE",
    }),

  approvals: (roomID: string, status = "") =>
    request<{ approvals: Approval[] }>(
      `/api/v1/rooms/${encodeURIComponent(roomID)}/approvals${status ? `?status=${encodeURIComponent(status)}` : ""}`,
    ),
  approval: (id: string) => request<Approval>(`/api/v1/approvals/${encodeURIComponent(id)}`),
  requestApproval: (roomID: string, agentID: string, action: ApprovalAction, ttlSeconds = 900) =>
    request<{ approval: Approval; event: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/approvals`, {
      method: "POST",
      body: JSON.stringify({ agent_id: agentID, action, ttl_seconds: ttlSeconds }),
    }),
  decideApproval: (id: string, decision: "grant" | "reject") =>
    request<{ approval: Approval; event: RoomEvent }>(`/api/v1/approvals/${encodeURIComponent(id)}/decide`, json({ decision })),
  consumeApproval: (id: string, digest: string) =>
    request<{ approval: Approval; event: RoomEvent }>(`/api/v1/approvals/${encodeURIComponent(id)}/consume`, json({ digest })),

  tools: (roomID: string, limit = 100) =>
    request<{ receipts: ToolReceipt[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/tools?limit=${limit}`),
  requestTool: (roomID: string, body: { agent_id: string; kind: string; tool: string; input?: unknown; approval_id?: string }) =>
    request<{ receipt: ToolReceipt; event: RoomEvent }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/tools`, json(body)),
  completeTool: (id: string, output: string, success: boolean) =>
    request<{ receipt: ToolReceipt; event: RoomEvent }>(`/api/v1/tools/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify({ output, success }),
    }),

  devices: () => request<{ devices: Device[] }>("/api/v1/devices"),
  syncAccount: () => request<AccountSyncResult>("/api/v1/account/sync", json({})),
  rotateAccountKey: () => request<RotateAccountKeyResult>("/api/v1/account/rotate-key", json({})),
  revokeDevice: (id: string) =>
    request<{ ok?: boolean; warnings?: string[] } | null>(`/api/v1/devices/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
  pairings: () => request<{ pairings: PairingView[] }>("/api/v1/pairings"),
  createPairing: () => request<PairingView>("/api/v1/pairings", json({})),
  approvePairing: (id: string) =>
    request<PairingView>(`/api/v1/pairings/${encodeURIComponent(id)}/approve`, json({})),
  cancelPairing: (id: string) =>
    request<void>(`/api/v1/pairings/${encodeURIComponent(id)}`, { method: "DELETE" }),
  startPairJoin: (code: string, deviceName: string, password: string) =>
    request<PairJoinView>("/api/v1/pairing/join", json({ code, device_name: deviceName, password })),
  pairJoinStatus: (id: string) =>
    request<PairJoinView>(`/api/v1/pairing/join/${encodeURIComponent(id)}`),
  cancelPairJoin: (id: string) =>
    request<void>(`/api/v1/pairing/join/${encodeURIComponent(id)}`, { method: "DELETE" }),

  attachments: (roomID: string) =>
    request<{ attachments: Attachment[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/attachments`),
  uploadAttachment: (roomID: string, file: File) => {
    const params = new URLSearchParams({ name: file.name, mime: file.type || "application/octet-stream" });
    return request<Attachment>(`/api/v1/rooms/${encodeURIComponent(roomID)}/attachments?${params}`, {
      method: "POST",
      body: file,
      headers: { "Content-Type": file.type || "application/octet-stream" },
    });
  },
  attachmentURL: (id: string) => `/api/v1/attachments/${encodeURIComponent(id)}/content`,

  search: (q: string, roomID?: string) => {
    const params = new URLSearchParams({ q, limit: "100" });
    const path = roomID ? `/api/v1/rooms/${encodeURIComponent(roomID)}/search?${params}` : `/api/v1/search?${params}`;
    return request<{ hits: SearchHit[] }>(path);
  },

  audit: (roomID: string) => request<AuditResponse>(`/api/v1/rooms/${encodeURIComponent(roomID)}/audit`),
  verifyAudit: (bundle: unknown) =>
    request<{ verified: boolean; head: string; events: number }>("/api/v1/audit/verify", json(bundle)),

  async download(path: string, filename: string): Promise<void> {
    const response = await fetch(path, { credentials: "same-origin" });
    if (!response.ok) throw new APIError(response.status, "", `Download failed (${response.status})`);
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
  },
};
