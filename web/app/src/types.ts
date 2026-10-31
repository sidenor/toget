export type VersionInfo = {
  app: string;
  commit: string;
  build_time: string;
  protocol: number;
  schema: number;
  go_version: string;
  os: string;
  arch: string;
};

export type Device = {
  id: string;
  name: string;
  role: string;
  fingerprint: string;
  current: boolean;
  last_seen_at?: string;
  revoked_at?: string;
  created_at: string;
};

export type Account = {
  id: string;
  name: string;
  server_id: string;
  server_code: string;
  created_at: string;
  device?: Device;
};

export type Status = {
  version: VersionInfo;
  account: Account | null;
  rooms: number;
  agents: number;
  pending_approvals: number;
  listen: string;
  locked: boolean;
};

export type SessionInfo = {
  has_account: boolean;
  locked: boolean;
  expires_at?: string;
  ttl_seconds: number;
};

export type Room = {
  id: string;
  name: string;
  topic?: string;
  home_node: string;
  home_server_id?: string;
  home_address?: string;
  created_by: string;
  epoch: number;
  created_at: string;
  archived_at?: string;
};

export type NodeInfo = {
  server_id: string;
  server_code: string;
  account_id: string;
  device_id: string;
  endpoint: string;
  url: string;
  tls_fingerprint: string;
  protocol: number;
};

export type RoomEvent = {
  id: string;
  room_id: string;
  seq: number;
  epoch: number;
  type: string;
  author_id: string;
  author_device?: string;
  payload: Record<string, any>;
  created_at: string;
  prev_id?: string;
  prev_hash?: string;
  hash: string;
  signature?: string;
};

export type Member = {
  room_id: string;
  account_id: string;
  device_id?: string;
  display_name: string;
  role: string;
  status: string;
  joined_at: string;
  left_at?: string;
};

export type RoomDevice = {
  room_id: string;
  device_id: string;
  account_id: string;
  joined_at: string;
  revoked_at?: string;
};

export type Invite = {
  id: string;
  room_id: string;
  secret?: string;
  code?: string;
  created_by: string;
  history: string;
  expires_at: string;
  used_at?: string;
  created_at: string;
};

export type Agent = {
  id: string;
  account_id: string;
  name: string;
  runtime: string;
  status: "idle" | "running" | "waiting_approval" | "offline" | "revoked" | string;
  permissions: Record<string, any>;
  created_at: string;
  last_seen_at?: string;
  revoked_at?: string;
};

export type ApprovalAction = {
  kind: string;
  tool?: string;
  target?: string;
  recipient?: string;
  amount?: string;
  currency?: string;
  parameters?: Record<string, unknown>;
};

export type Approval = {
  id: string;
  room_id: string;
  agent_id: string;
  requested_by: string;
  action: ApprovalAction;
  action_digest: string;
  description: string;
  status: "pending" | "granted" | "rejected" | "expired" | "consumed" | string;
  expires_at: string;
  decided_by?: string;
  decided_at?: string;
  consumed_at?: string;
  created_at: string;
};

export type ToolReceipt = {
  id: string;
  room_id: string;
  agent_id: string;
  approval_id?: string;
  kind: string;
  tool: string;
  input: unknown;
  output?: string;
  status: string;
  created_at: string;
};

export type Attachment = {
  id: string;
  room_id: string;
  event_id?: string;
  name: string;
  mime: string;
  size: number;
  sha256: string;
  created_at: string;
};

export type SearchHit = {
  room_id: string;
  room_name: string;
  event_id: string;
  seq: number;
  type: string;
  text: string;
  author_id: string;
  created_at: string;
};

export type AuditBundle = {
  room: Room;
  members: Member[];
  devices: RoomDevice[];
  events: RoomEvent[];
  head: string;
  generated_at: string;
};

export type AuditResponse = {
  audit: AuditBundle;
  verified: boolean;
};

export type CreateRoomResponse = {
  room: Room;
  event: RoomEvent;
};

export type PairingView = {
  id: string;
  status: "waiting" | "requested" | "approving" | "approved" | "expired" | "canceled" | string;
  device_id?: string;
  device_name?: string;
  fingerprint?: string;
  expires_at: string;
  created_at: string;
  code?: string;
  warnings?: string[];
};

export type PairJoinView = {
  id: string;
  status: "waiting" | "awaiting_approval" | "importing" | "ready" | "failed" | "canceled" | "expired" | string;
  device_name: string;
  fingerprint: string;
  account_id: string;
  error?: string;
  warnings?: string[];
  started_at: string;
  updated_at: string;
};
