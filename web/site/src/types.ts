export type Tone = "jade" | "sky" | "peach" | "lilac" | "sand" | "rose" | "slate";

export interface AvatarSpec {
  label: string;
  tone: Tone;
  agent?: boolean;
}

export interface MessageFile {
  name: string;
  size: string;
  kind?: string;
}

export interface Message {
  id: string;
  from: string;
  text?: string;
  kind?: "text" | "file" | "event";
  file?: MessageFile;
  at: string;
  read?: boolean;
  reactions?: Record<string, number>;
}

export interface Chat {
  id: string;
  name: string;
  avatar: AvatarSpec;
  kind: "group" | "dm" | "agent";
  members: string[];
  pinned?: boolean;
  unread: number;
  messages: Message[];
  createdAt?: string;
}

export interface Person {
  id: string;
  name: string;
  handle: string;
  avatar: AvatarSpec;
  status: string;
  agent?: boolean;
}

export interface Profile {
  id: string;
  name: string;
  handle: string;
  avatar: AvatarSpec;
  status: string;
}

export type Tab = "chats" | "contacts" | "me";

export type Theme = "light" | "dark" | "system";
