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
  | "person"
  | "lock"
  | "info"
  | "copy"
  | "download"
  | "logout"
  | "refresh"
  | "wifiOff"
  | "link"
  | "mail"
  | "eye"
  | "eyeOff"
  | "plus"
  | "device";

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
  lock: (
    <>
      <rect x="4.8" y="10.2" width="14.4" height="10" rx="2.6" />
      <path d="M8.2 10.2V7.6a3.8 3.8 0 0 1 7.6 0v2.6" />
      <path d="M12 14.2v2.1" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="8.6" />
      <path d="M12 11.1v5.2" />
      <circle cx="12" cy="8.1" r=".9" fill="currentColor" stroke="none" />
    </>
  ),
  copy: (
    <>
      <rect x="8.2" y="8.2" width="11.2" height="11.2" rx="2.2" />
      <path d="M15.8 8.2V6.4a1.8 1.8 0 0 0-1.8-1.8H6.4a1.8 1.8 0 0 0-1.8 1.8v7.6a1.8 1.8 0 0 0 1.8 1.8h1.8" />
    </>
  ),
  download: (
    <>
      <path d="M12 4.2v10.4M7.6 10.8 12 15.2l4.4-4.4" />
      <path d="M4.8 18.4v1.2a1.6 1.6 0 0 0 1.6 1.6h11.2a1.6 1.6 0 0 0 1.6-1.6v-1.2" />
    </>
  ),
  logout: (
    <>
      <path d="M10 4.6H6.2a2 2 0 0 0-2 2v10.8a2 2 0 0 0 2 2H10" />
      <path d="M14.4 8.2 18.2 12l-3.8 3.8M18.2 12H9.2" />
    </>
  ),
  refresh: (
    <>
      <path d="M19.6 12a7.6 7.6 0 0 1-12.9 5.4L4.4 15" />
      <path d="M4.4 12a7.6 7.6 0 0 1 12.9-5.4L19.6 9" />
      <path d="M4.4 19.2V15h4.2M19.6 4.8V9h-4.2" />
    </>
  ),
  wifiOff: (
    <>
      <path d="M3.4 3.4 20.6 20.6" />
      <path d="M8.8 15.4a4.6 4.6 0 0 1 6.4 0M5.6 12.1a9.2 9.2 0 0 1 3.1-1.9M18.4 12.1a9.2 9.2 0 0 0-2.6-1.7M2.8 8.7a14 14 0 0 1 3.5-2.3M21.2 8.7a14 14 0 0 0-7.8-3.2" />
      <circle cx="12" cy="18.6" r="1" fill="currentColor" stroke="none" />
    </>
  ),
  link: (
    <>
      <path d="M9.6 14.4 14.4 9.6" />
      <path d="M8.1 17.6 6.2 19.5a3.5 3.5 0 0 1-4.9-4.9l3.4-3.4a3.5 3.5 0 0 1 4.9 0" />
      <path d="m15.9 6.4 1.9-1.9a3.5 3.5 0 0 1 4.9 4.9l-3.4 3.4a3.5 3.5 0 0 1-4.9 0" />
    </>
  ),
  mail: (
    <>
      <rect x="3.4" y="5.4" width="17.2" height="13.2" rx="2.4" />
      <path d="m4.6 7.2 7.4 5.4 7.4-5.4" />
    </>
  ),
  eye: (
    <>
      <path d="M2.6 12s3.4-6 9.4-6 9.4 6 9.4 6-3.4 6-9.4 6-9.4-6-9.4-6Z" />
      <circle cx="12" cy="12" r="2.7" />
    </>
  ),
  eyeOff: (
    <>
      <path d="M3 3l18 18" />
      <path d="M10.6 6.2A10 10 0 0 1 12 6c6 0 9.4 6 9.4 6a16 16 0 0 1-2.3 2.9M6.3 7.9A16 16 0 0 0 2.6 12s3.4 6 9.4 6a9.5 9.5 0 0 0 3.4-.6" />
    </>
  ),
  plus: <path d="M12 5.2v13.6M5.2 12h13.6" strokeWidth="2" />,
  device: (
    <>
      <rect x="3.4" y="4.8" width="17.2" height="11.6" rx="2.2" />
      <path d="M9 19.6h6M12 16.4v3.2" />
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

export function SearchBox({
  value,
  onChange,
  placeholder = "搜索",
  autoFocus = false,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  autoFocus?: boolean;
}) {
  return (
    <label className="search-box">
      <Icon name="search" size={17} />
      <input
        autoFocus={autoFocus}
        type="text"
        inputMode="search"
        enterKeyHint="search"
        value={value}
        placeholder={placeholder}
        aria-label={placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
      {value && (
        <button
          type="button"
          className="search-clear"
          aria-label="清除搜索"
          onClick={() => onChange("")}
        >
          <Icon name="close" size={13} />
        </button>
      )}
    </label>
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
}: {
  spec: AvatarSpec;
  size?: number;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      className={`avatar avatar-size-${avatarSize(size)} ${
        spec.agent ? "avatar-agent" : `avatar-${spec.tone}`
      } ${className}`}
    >
      {spec.agent ? <Icon name="sparkle" size={Math.round(size * 0.45)} /> : spec.label}
    </span>
  );
}
