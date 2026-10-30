export function formatTime(iso: string, locale?: string): string {
  return new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(new Date(iso));
}

export function formatDay(iso: string, locale?: string): string {
  const date = new Date(iso);
  const today = new Date();
  if (date.toDateString() === today.toDateString()) return locale === "zh-CN" ? "今天" : "Today";
  return new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(date);
}

export function shortID(id: string): string {
  return id.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export function formatAccountID(id: string): string {
  return id.length === 14 ? `${id.slice(0, 8)}-${id.slice(8)}` : id;
}
