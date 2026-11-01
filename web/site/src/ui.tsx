import type { ReactNode } from "react";
import type { AvatarSpec } from "./types";

export type IconName =
  | "search"
  | "compose"
  | "back"
  | "close"
  | "chevronRight"
  | "more"
  | "send"
  | "smile"
  | "check"
  | "sparkle"
  | "moon"
  | "sun"
  | "auto"
  | "file"
  | "chats"
  | "contacts"
  | "person";

const paths: Record<IconName, ReactNode> = {
  search: (
    <>
      <circle cx="10.8" cy="10.8" r="6.4" />
      <path d="m15.6 15.6 4.1 4.1" />
    </>
  ),
  compose: (
    <>
      <path d="M11.8 5H6.6A2.6 2.6 0 0 0 4 7.6v9.8A2.6 2.6 0 0 0 6.6 20h9.8a2.6 2.6 0 0 0 2.6-2.6v-5.2" />
      <path d="m13.6 6.2 4.2 4.2M11.7 16.3l.8-3.5 7-7a1.9 1.9 0 0 1 2.7 2.7l-7 7z" />
    </>
  ),
  back: <path d="m14.7 5.2-7 6.8 7 6.8" />,
  close: <path d="m6.2 6.2 11.6 11.6M17.8 6.2 6.2 17.8" />,
  chevronRight: <path d="m9.4 5.2 6.6 6.8-6.6 6.8" />,
  more: (
    <>
      <circle cx="5.4" cy="12" r="1.45" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12" r="1.45" fill="currentColor" stroke="none" />
      <circle cx="18.6" cy="12" r="1.45" fill="currentColor" stroke="none" />
    </>
  ),
  send: <path d="M12 18.8V5.4M6.7 10.7 12 5.4l5.3 5.3" strokeWidth="2.1" />,
  smile: (
    <>
      <circle cx="12" cy="12" r="8.4" />
      <path d="M8.5 14.1c.8 1.4 2 2.1 3.5 2.1s2.7-.7 3.5-2.1" />
      <circle cx="9.2" cy="9.7" r=".8" fill="currentColor" stroke="none" />
      <circle cx="14.8" cy="9.7" r=".8" fill="currentColor" stroke="none" />
    </>
  ),
  check: <path d="m5.2 12.4 4.2 4.2 9.4-9.2" />,
  sparkle: (
    <path
      d="M12 3.1 13.8 8.3 19 10.1l-5.2 1.8L12 17.1l-1.8-5.2L5 10.1l5.2-1.8z"
      fill="currentColor"
      stroke="none"
    />
  ),
  moon: <path d="M20.2 14.6A8.2 8.2 0 0 1 9.4 3.8 8.2 8.2 0 1 0 20.2 14.6Z" />,
  sun: (
    <>
      <circle cx="12" cy="12" r="4.1" />
      <path d="M12 2.8v2M12 19.2v2M2.8 12h2M19.2 12h2M5.5 5.5l1.4 1.4M17.1 17.1l1.4 1.4M18.5 5.5l-1.4 1.4M6.9 17.1l-1.4 1.4" />
    </>
  ),
  auto: (
    <>
      <rect x="3.4" y="5" width="17.2" height="11.7" rx="2.3" />
      <path d="M9.2 20h5.6M12 16.7V20" />
    </>
  ),
  file: (
    <>
      <path d="M13.2 3.4H7.6A2.2 2.2 0 0 0 5.4 5.6v12.8a2.2 2.2 0 0 0 2.2 2.2h8.8a2.2 2.2 0 0 0 2.2-2.2V9z" />
      <path d="M13.2 3.4V9h5.4" />
    </>
  ),
  chats: (
    <path d="M20.4 11.5c0 3.8-3.8 6.9-8.4 6.9-1 0-1.9-.1-2.8-.4L4.2 20l1.1-3.2a6.4 6.4 0 0 1-1.7-4.4c0-3.8 3.8-6.9 8.4-6.9s8.4 3.1 8.4 6.9Z" />
  ),
  contacts: (
    <>
      <circle cx="9.1" cy="8.2" r="3.1" />
      <path d="M3.7 19.1c.5-3.1 2.7-5 5.4-5s4.9 1.9 5.4 5" />
      <circle cx="17.1" cy="9.2" r="2.3" />
      <path d="M16.2 14.3c2.4.2 4.1 1.8 4.4 4.2" />
    </>
  ),
  person: (
    <>
      <circle cx="12" cy="8" r="3.4" />
      <path d="M5.2 20c.6-3.7 3.1-5.8 6.8-5.8s6.2 2.1 6.8 5.8" />
    </>
  ),
};

export function Icon({
  name,
  size = 20,
  className,
  strokeWidth = 1.8,
}: {
  name: IconName;
  size?: number;
  className?: string;
  strokeWidth?: number;
}) {
  return (
    <svg
      aria-hidden="true"
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {paths[name]}
    </svg>
  );
}

export function BrandMark({ size = 30 }: { size?: number }) {
  return (
    <svg aria-hidden="true" width={size} height={size} viewBox="0 0 32 32">
      <rect width="32" height="32" rx="9" fill="#0f766e" />
      <path
        d="M9.5 11.5h13M16 11.5v10.2"
        fill="none"
        stroke="#fff"
        strokeWidth="2.6"
        strokeLinecap="round"
      />
      <circle cx="22.4" cy="21.7" r="2.3" fill="#7ee0d3" />
    </svg>
  );
}

const AVATAR_SIZES = [24, 28, 32, 36, 38, 40, 44, 46, 50, 68, 76, 84] as const;

function avatarSize(size: number): number {
  return AVATAR_SIZES.reduce((best, candidate) =>
    Math.abs(candidate - size) < Math.abs(best - size) ? candidate : best,
  );
}

export function Avatar({
  spec,
  size = 44,
  className = "",
  presence = false,
}: {
  spec: AvatarSpec;
  size?: number;
  className?: string;
  presence?: boolean;
}) {
  const avatar = (
    <span
      aria-hidden="true"
      className={`avatar avatar-size-${avatarSize(size)} ${
        spec.agent ? "avatar-agent" : `avatar-${spec.tone}`
      } ${className}`}
    >
      {spec.agent ? <Icon name="sparkle" size={Math.round(size * 0.45)} /> : spec.label}
    </span>
  );
  if (!presence) {
    return avatar;
  }
  return (
    <span className="avatar-wrap">
      {avatar}
      <i className="presence" />
    </span>
  );
}

export function AvatarStack({
  avatars,
  max = 3,
}: {
  avatars: AvatarSpec[];
  max?: number;
}) {
  const shown = avatars.slice(0, max);
  const extra = avatars.length - shown.length;
  return (
    <div className="avatar-stack" aria-hidden="true">
      {shown.map((avatar, index) => (
        <Avatar key={index} spec={avatar} size={24} className="stack-avatar" />
      ))}
      {extra > 0 && <span className="stack-more">+{extra}</span>}
    </div>
  );
}
