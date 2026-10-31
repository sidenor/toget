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

export type AccountSyncResult = {
  peers: number;
  synced: number;
  warnings?: string[];
  revoked?: string[];
};

export type RotateAccountKeyResult = {
  generation: number;
  devices: number;
  warnings: string[];
};

export type RecoveryStatus = {
  enabled: boolean;
  created_at?: string | null;
  device_id: string;
};

export type RecoveryCredential = {
  filename: string;
  content: string;
  created_at: string;
};

export type Account = {
  id: string;
  name: string;
  server_id: string;
  server_code: string;
  key_generation: number;
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
  direct_url?: string;
  relay_url?: string;
  relay_base?: string;
  relay_mode: "off" | "auto" | "pinned";
  relay_setting: string;
  relay_connected: boolean;
  relay_tunnel: boolean;
  directory_url?: string;
  mdns: boolean;
  mdns_peers?: number;
  tls_fingerprint: string;
  protocol: number;
};

export type CardView = {
  server_id: string;
  server_code: string;
  account_id: string;
  label?: string;
  card: string;
  added_at?: string;
  identity_expires_at: string;
  node_expires_at: string;
};

export type DiscoveredPeer = {
  server_id: string;
  server_code: string;
  protocol: number;
  tls_fingerprint: string;
  port: number;
  addresses: string[];
  last_seen: string;
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

export type AgentStream = {
  run_id: string;
  agent_id: string;
  agent_name: string;
  room_id: string;
  trigger_event_id?: string;
  trigger_seq?: number;
  text: string;
  truncated: boolean;
  started_at: string;
  updated_at: string;
};

export type AgentSession = {
  agent_id: string;
  room_id: string;
  room_name?: string;
  last_seen_seq: number;
  last_run_seq: number;
  active_event_id?: string;
  active_seq?: number;
  runtime_session?: string;
  run_count: number;
  status: string;
  last_error?: string;
  updated_at: string;
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
  policy_id?: string;
  created_at: string;
};

export type PolicyRule = {
  id: string;
  effect: "allow" | "deny";
  kind?: string;
  tool?: string;
  target?: string;
  recipient?: string;
  amount_min?: string;
  amount_max?: string;
  currency?: string;
  weekdays?: number[];
  start?: string;
  end?: string;
  timezone?: string;
  note?: string;
};

export type Policy = {
  id: string;
  room_id: string;
  agent_id?: string;
  rules: PolicyRule[];
  created_at: string;
  updated_at: string;
};

export type PolicyDecision = {
  effect: "allow" | "deny" | "approval" | string;
  rule_id?: string;
  agent_id?: string;
  reason: string;
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

export type UpdateCurrent = {
  app: string;
  protocol: number;
  schema: number;
  release?: string;
  manifest_sha256?: string;
};

export type UpdateChannelSummary = {
  channel: string;
  release: string;
  rollout: number;
  published_at?: string;
};

export type UpdateManifestSummary = {
  release: string;
  protocol: number;
  schema: number;
  level: "recommended" | "security" | "forced" | string;
  changelog?: string;
  published_at?: string;
  min_app?: string;
  min_protocol?: number;
};

export type UpdateCheckResult = {
  channel_url: string;
  manifest_url: string;
  channel?: UpdateChannelSummary;
  manifest?: UpdateManifestSummary;
  manifest_sha256: string;
  current: UpdateCurrent;
  available: boolean;
  reason: string;
  rollout_percent?: number;
  rollout_bucket?: number;
  downgrade?: boolean;
  warning?: string;
};

export type UpdateApplyResult = {
  release: string;
  previous?: string;
  executable?: string;
  up_to_date?: boolean;
  restarted?: boolean;
  needs_restart?: boolean;
  warning?: string;
};

export type UpdatePending = {
  release: string;
  started_at: string;
};

export type UpdateManagerState = {
  current?: string;
  previous?: string;
  pending?: UpdatePending | null;
  rollout_id?: string;
  [key: string]: unknown;
};

export type UpdateManagerStatus = {
  root: string;
  initialized: boolean;
  state: UpdateManagerState;
  current_link?: string;
  previous_link?: string;
  versions?: { release: string; path: string; executable: string }[];
  issues?: string[];
};

export type UpdateStatusView = {
  enabled: boolean;
  mode: "notify" | "auto" | "off" | string;
  channel: string;
  check_interval: string;
  last_check?: string;
  last_error?: string;
  current: UpdateCurrent;
  result?: UpdateCheckResult;
  dismissed?: boolean;
  dismissed_until?: string;
  staged?: UpdateApplyResult;
  status?: UpdateManagerStatus;
  restart_configured: boolean;
  restart_supported: boolean;
  config_error?: string;
};

export type UpdateCheckView = {
  result: UpdateCheckResult;
  update: UpdateStatusView;
};

export type UpdateApplyView = {
  result: UpdateApplyResult;
  update: UpdateStatusView;
};

export type BackupManifest = {
  format: string;
  version: number;
  created_at: string;
  app_version: string;
  schema_version: number;
  files: { path: string; size: number; sha256: string }[];
};

export type BackupArchive = {
  name: string;
  size: number;
  manifest: BackupManifest;
};

export type RestoreUpload = {
  id: string;
  size: number;
  manifest: BackupManifest;
};

export type RestorePending = {
  manifest: BackupManifest;
  size: number;
  staged_at: string;
};

export type RestoreStatus = {
  staged: RestorePending | null;
  restart_configured: boolean;
  restart_supported: boolean;
};

export type NotificationSubscription = {
  id: string;
  endpoint_host: string;
  label: string;
  language: string;
  user_agent: string;
  enabled: boolean;
  message_events: boolean;
  approval_events: boolean;
  muted_until?: string | null;
  created_at: string;
  last_success_at?: string | null;
  last_failure_at?: string | null;
  failure_count: number;
};

export type NotificationStatus = {
  public_key: string;
  subscriptions: NotificationSubscription[];
  muted_until?: string | null;
};
