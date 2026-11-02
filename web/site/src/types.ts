export type Tone = "jade" | "sky" | "peach" | "lilac" | "sand" | "rose" | "slate";

export interface AvatarSpec {
  label: string;
  tone: Tone;
  agent?: boolean;
}

export type Theme = "light" | "dark" | "system";

export type Tab = "chats" | "friends" | "members" | "agents" | "me";

export type ViewMode = "chats" | "member";

export interface Profile {
  account_id: string;
  name: string;
  host_id: string;
  host_code: string;
  device_id: string;
  device_name: string;
  email_verified: boolean;
  created_at?: string;
  room_count: number;
  vault_pending: boolean;
}

export interface AccountDevice {
  device_id: string;
  name: string;
  current: boolean;
  revoked_at?: string | null;
  created_at?: string;
}

export interface RoomSummary {
  id: string;
  name: string;
  topic?: string;
  kind?: string;
  peer_account_id?: string;
  peer_name?: string;
  owner_account_id: string;
  epoch: number;
  member_count: number;
  unread: number;
  history?: string;
  joined_at?: string;
  read_seq?: number;
  last_seq?: number;
  last_text?: string;
  last_author?: string;
  last_at?: string | null;
}

export interface MessageState {
  id: string;
  seq: number;
  event_type: string;
  author_id: string;
  author_device?: string;
  author_name?: string;
  text: string;
  kind: "text" | "system" | "locked";
  at: string;
  epoch?: number;
}

export interface MemberState {
  account_id: string;
  display_name: string;
  role: string;
  status: string;
  encryption_public_key?: string;
  joined_at?: string;
  left_at?: string | null;
}

export interface DeviceInfo {
  device_id: string;
  account_id: string;
  signing_public_key?: string;
  encryption_public_key?: string;
  joined_at?: string;
  revoked_at?: string | null;
}

export interface RoomView {
  summary: RoomSummary;
  messages: MessageState[];
  members: MemberState[];
  devices: DeviceInfo[];
  epoch: number;
  my_role?: string;
  can_rename: boolean;
  can_invite: boolean;
  rotation_required: boolean;
  locked_count: number;
  vault_pending: boolean;
}

export interface RecoveryFile {
  filename: string;
  content: string;
}

export interface AuthResult {
  profile: Profile;
  email_sent: boolean;
  recovery?: RecoveryFile;
  vault_loaded: boolean;
  vault_error?: string;
}

export interface RefreshResult {
  rooms: RoomSummary[];
  warnings?: string[];
}

export interface InviteResult {
  code: string;
  history: string;
  expires_at: string;
}

export interface FriendInviteResult {
  room: RoomView;
  invite: InviteResult;
}

export interface ResolvedDevice {
  id: string;
  encryption_public_key: string;
}

export interface ResolvedAccount {
  protocol: number;
  account_id: string;
  name?: string;
  host_id: string;
  host_code: string;
  host_url: string;
  server_name?: string;
  kind: "account" | "node";
  self: boolean;
  devices?: ResolvedDevice[];
}

export interface FriendRequestSent {
  id: string;
  expires_at: string;
}

export interface SentFriendRequestView {
  id: string;
  to_account_id: string;
  to_name?: string;
  room_id: string;
  created_at: string;
  expires_at: string;
}

export interface FriendRequestView {
  id: string;
  from_account_id: string;
  from_name?: string;
  room_id: string;
  room_name?: string;
  room_topic?: string;
  code: string;
  created_at: string;
  expires_at: string;
}

export interface ApiError {
  code: string;
  message: string;
}

export interface StoredMember {
  accountID: string;
  name: string;
  rooms: { id: string; name: string; role: string }[];
}
