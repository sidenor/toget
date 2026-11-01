import type { AvatarSpec, Tone } from "./types";

const TONES: Tone[] = ["jade", "sky", "peach", "lilac", "sand", "rose", "slate"];

function hash(seed: string): number {
  let value = 0x811c9dc5;
  for (let index = 0; index < seed.length; index += 1) {
    value ^= seed.charCodeAt(index);
    value = Math.imul(value, 0x01000193);
  }
  return value >>> 0;
}

export function avatarFor(
  seed: string,
  name: string,
  options: { agent?: boolean } = {},
): AvatarSpec {
  const characters = Array.from((name || "").trim());
  const label =
    characters.length > 0 ? characters[0].toLocaleUpperCase("zh-CN") : "·";
  return {
    label,
    tone: TONES[hash(seed) % TONES.length],
    agent: options.agent,
  };
}

export function roomAvatar(roomId: string, name: string): AvatarSpec {
  return avatarFor(`room:${roomId}`, name || "房");
}

export function memberAvatar(accountId: string, name: string): AvatarSpec {
  return avatarFor(`member:${accountId}`, name || "人");
}

export function profileAvatar(accountId: string, name: string): AvatarSpec {
  return avatarFor(`me:${accountId}`, name || "我");
}
