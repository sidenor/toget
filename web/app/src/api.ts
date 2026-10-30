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

export type Account = {
  id: string;
  name: string;
  server_id: string;
  server_code: string;
  created_at: string;
};

export type Room = {
  id: string;
  name: string;
  home_node: string;
  created_by: string;
  epoch: number;
  created_at: string;
};

export type RoomEvent = {
  id: string;
  room_id: string;
  seq: number;
  type: string;
  author_id: string;
  payload: Record<string, unknown>;
  created_at: string;
  prev_id?: string;
  hash: string;
};

export type Status = {
  version: VersionInfo;
  account: Account | null;
  rooms: number;
  listen: string;
};

export type CreateRoomResponse = {
  room: Room;
  event: RoomEvent;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    const message =
      data && typeof data === "object" && "error" in data
        ? String((data as { error?: { message?: string } }).error?.message ?? "")
        : "";
    throw new Error(message || `Request failed (${response.status})`);
  }
  return data as T;
}

export const api = {
  status: () => request<Status>("/api/v1/status"),
  createAccount: (name: string, password: string) =>
    request<Account>("/api/v1/account", {
      method: "POST",
      body: JSON.stringify({ name, password }),
    }),
  rooms: () => request<{ rooms: Room[] }>("/api/v1/rooms"),
  createRoom: (name: string) =>
    request<CreateRoomResponse>("/api/v1/rooms", {
      method: "POST",
      body: JSON.stringify({ name }),
    }),
  events: (roomID: string, after = 0) =>
    request<{ events: RoomEvent[] }>(`/api/v1/rooms/${encodeURIComponent(roomID)}/events?after=${after}&limit=500`),
  send: (roomID: string, text: string) =>
    request<RoomEvent>(`/api/v1/rooms/${encodeURIComponent(roomID)}/messages`, {
      method: "POST",
      body: JSON.stringify({ text }),
    }),
};
