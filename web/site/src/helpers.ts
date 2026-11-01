import type { RoomSummary } from "./types";

export function dayKey(iso: string): string {
  const date = new Date(iso);
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
}

export function clock(iso: string): string {
  const date = new Date(iso);
  return `${String(date.getHours()).padStart(2, "0")}:${String(
    date.getMinutes(),
  ).padStart(2, "0")}`;
}

export function sameDate(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

export function dayLabel(iso: string): string {
  const date = new Date(iso);
  const now = new Date();
  if (sameDate(date, now)) {
    return "今天";
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDate(date, yesterday)) {
    return "昨天";
  }
  if (date.getFullYear() !== now.getFullYear()) {
    return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
  }
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}

export function listTime(iso?: string | null): string {
  if (!iso) {
    return "";
  }
  const date = new Date(iso);
  const now = new Date();
  if (sameDate(date, now)) {
    return clock(iso);
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDate(date, yesterday)) {
    return "昨天";
  }
  const elapsed = now.getTime() - date.getTime();
  if (elapsed >= 0 && elapsed < 6 * 24 * 60 * 60 * 1000) {
    return ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][
      date.getDay()
    ];
  }
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

export function expiryLabel(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const now = new Date();
  const prefix =
    date.getFullYear() === now.getFullYear()
      ? `${date.getMonth() + 1}月${date.getDate()}日`
      : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
  return `${prefix} ${clock(iso)}`;
}

export function roomTitle(room: RoomSummary): string {
  if (room.kind === "direct" && room.peer_name?.trim()) {
    return room.peer_name.trim();
  }
  return room.name;
}

export function roomPreview(room: RoomSummary): string {
  const text = (room.last_text || "").trim();
  if (text) {
    return text;
  }
  if (room.kind === "direct" && !room.peer_name?.trim()) {
    return "等待好友加入";
  }
  const topic = (room.topic || "").trim();
  if (topic) {
    return topic;
  }
  return "还没有消息";
}

export function shortID(value: string, length = 8): string {
  if (value.length <= length) {
    return value;
  }
  return value.slice(0, length);
}

export function downloadFile(filename: string, content: string): void {
  const blob = new Blob([content + "\n"], {
    type: "application/octet-stream",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 4000);
}

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // The clipboard API can be blocked by permissions; fall through.
  }
  try {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    area.style.pointerEvents = "none";
    document.body.appendChild(area);
    area.select();
    const copied = document.execCommand("copy");
    area.remove();
    return copied;
  } catch {
    return false;
  }
}

export function defaultDeviceName(): string {
  const agent = navigator.userAgent;
  if (/iPhone/i.test(agent)) {
    return "iPhone 浏览器";
  }
  if (/iPad/i.test(agent)) {
    return "iPad 浏览器";
  }
  if (/Android/i.test(agent)) {
    return "Android 浏览器";
  }
  if (/Macintosh|Mac OS X/i.test(agent)) {
    return "Mac 浏览器";
  }
  if (/Windows/i.test(agent)) {
    return "Windows 浏览器";
  }
  return "网页版";
}

export function pluralRooms(count: number): string {
  return `${count} 个房间`;
}
