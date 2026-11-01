import { useEffect, useState } from "react";
import { roomSummaryAvatar } from "./avatars";
import { FormError, Modal } from "./dialogs";
import { copyText, expiryLabel, listTime, roomPreview } from "./helpers";
import type {
  FriendInviteResult,
  InviteResult,
  RoomSummary,
} from "./types";
import { Avatar, BrandMark, Icon, SearchBox } from "./ui";

export interface FriendEntry {
  room: RoomSummary;
  title: string;
  pending: boolean;
}

export function friendEntries(rooms: RoomSummary[]): FriendEntry[] {
  return rooms
    .filter((room) => room.kind === "direct")
    .map((room) => {
      const peer = room.peer_name?.trim() || "";
      return {
        room,
        title: peer || room.name || "新的好友",
        pending: !peer,
      };
    });
}

function FriendRow({
  entry,
  compact,
  onOpen,
}: {
  entry: FriendEntry;
  compact: boolean;
  onOpen: () => void;
}) {
  const { room, title, pending } = entry;
  return (
    <button
      type="button"
      className="chat-row friend-row"
      onClick={onOpen}
    >
      <Avatar
        spec={roomSummaryAvatar(room)}
        size={compact ? 36 : 46}
      />
      <span className="chat-row-copy">
        <span className="chat-row-top">
          <strong>
            {title}
            {pending && <span className="friend-pending">待加入</span>}
          </strong>
          <span className="chat-row-time">
            {listTime(room.last_at ?? room.joined_at)}
          </span>
        </span>
        <span className="chat-row-bottom">
          <span className="chat-row-preview">
            {pending ? "等待好友加入" : roomPreview(room)}
          </span>
          {room.unread > 0 && (
            <span className="unread" aria-label={`${room.unread} 条未读`}>
              {room.unread > 99 ? "99+" : room.unread}
            </span>
          )}
        </span>
      </span>
    </button>
  );
}

function EmptyFriends({ compact }: { compact: boolean }) {
  if (compact) {
    return (
      <div className="list-empty">
        <Icon name="contacts" size={20} />
        <span>还没有好友</span>
      </div>
    );
  }
  return (
    <div className="empty-chat friends-empty">
      <Icon name="contacts" size={30} />
      <p>还没有好友。创建一个邀请，把链接发给 TA 就行。</p>
    </div>
  );
}

export function FriendsSidebar({
  entries,
  onOpen,
  onAdd,
  onJoin,
}: {
  entries: FriendEntry[];
  onOpen: (roomID: string) => void;
  onAdd: () => void;
  onJoin: () => void;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? entries.filter((entry) =>
        `${entry.title} ${entry.room.last_text ?? ""}`
          .toLocaleLowerCase("zh-CN")
          .includes(needle),
      )
    : entries;

  return (
    <div className="list-wrap">
      <SearchBox
        value={query}
        onChange={setQuery}
        placeholder="搜索好友"
      />
      <div className="list-tools">
        <button type="button" onClick={onAdd}>
          <Icon name="plus" size={14} />
          加好友
        </button>
        <button type="button" onClick={onJoin}>
          <Icon name="link" size={14} />
          有邀请码
        </button>
      </div>
      <div className="contact-list">
        {visible.map((entry) => (
          <FriendRow
            key={entry.room.id}
            entry={entry}
            compact
            onOpen={() => onOpen(entry.room.id)}
          />
        ))}
        {visible.length === 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>{needle ? "没有找到" : "还没有好友"}</span>
          </div>
        )}
      </div>
    </div>
  );
}

export function FriendsPage({
  entries,
  onOpen,
  onAdd,
  onJoin,
}: {
  entries: FriendEntry[];
  onOpen: (roomID: string) => void;
  onAdd: () => void;
  onJoin: () => void;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? entries.filter((entry) =>
        `${entry.title} ${entry.room.last_text ?? ""}`
          .toLocaleLowerCase("zh-CN")
          .includes(needle),
      )
    : entries;

  return (
    <div className="page mobile-page friends-page">
      <div className="mobile-head">
        <div className="mobile-heading">
          <BrandMark size={25} />
          <h1>好友</h1>
        </div>
        <div className="mobile-head-actions">
          <button
            className="icon-button"
            type="button"
            aria-label="输入好友邀请码"
            onClick={onJoin}
          >
            <Icon name="link" size={18} />
          </button>
          <button
            className="icon-button"
            type="button"
            aria-label="添加好友"
            onClick={onAdd}
          >
            <Icon name="plus" size={19} />
          </button>
        </div>
      </div>
      <div className="mobile-unread-line">
        {entries.length > 0
          ? `${entries.length} 位好友 · 1:1 加密聊天`
          : "添加好友，开始 1:1 加密聊天"}
      </div>
      {entries.length > 0 && (
        <SearchBox
          value={query}
          onChange={setQuery}
          placeholder="搜索好友"
        />
      )}
      <div className="contact-list">
        {visible.map((entry) => (
          <FriendRow
            key={entry.room.id}
            entry={entry}
            compact={false}
            onOpen={() => onOpen(entry.room.id)}
          />
        ))}
        {entries.length === 0 && <EmptyFriends compact={false} />}
        {entries.length > 0 && visible.length === 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>没有找到</span>
          </div>
        )}
      </div>
    </div>
  );
}

export function FriendsOverview({
  entries,
  onOpen,
  onAdd,
}: {
  entries: FriendEntry[];
  onOpen: (roomID: string) => void;
  onAdd: () => void;
}) {
  return (
    <div className="page friends-overview-page">
      <div className="desktop-page-head friend-page-head">
        <div>
          <h1>好友</h1>
          <p className="desktop-page-sub">
            {entries.length > 0
              ? `${entries.length} 位好友 · 1:1 端到端加密`
              : "添加好友，开始 1:1 加密聊天"}
          </p>
        </div>
        <button
          type="button"
          className="primary-button small friend-add-button"
          onClick={onAdd}
        >
          <Icon name="plus" size={15} />
          添加好友
        </button>
      </div>
      <div className="page-scroll">
        <div className="page-inner friends-inner">
          {entries.length === 0 ? (
            <EmptyFriends compact={false} />
          ) : (
            <div className="member-grid friend-grid">
              {entries.map((entry) => (
                <button
                  type="button"
                  className="member-card friend-card"
                  key={entry.room.id}
                  onClick={() => onOpen(entry.room.id)}
                >
                  <Avatar
                    spec={roomSummaryAvatar(entry.room)}
                    size={46}
                  />
                  <span className="member-card-copy">
                    <strong>
                      {entry.title}
                      {entry.pending && (
                        <span className="friend-pending">待加入</span>
                      )}
                    </strong>
                    <small>
                      {entry.pending
                        ? "等待好友加入"
                        : roomPreview(entry.room)}
                      {entry.room.unread > 0
                        ? ` · ${entry.room.unread} 条未读`
                        : ""}
                    </small>
                  </span>
                  <Icon name="chevronRight" size={15} />
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export function AddFriendDialog({
  isMobile,
  onClose,
  onCreateInvite,
  onJoinExisting,
}: {
  isMobile: boolean;
  onClose: () => void;
  onCreateInvite: (name: string) => Promise<FriendInviteResult>;
  onJoinExisting: () => void;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<FriendInviteResult | null>(null);
  const [copied, setCopied] = useState<"code" | "link" | null>(null);

  useEffect(() => {
    if (!copied) {
      return;
    }
    const timer = window.setTimeout(() => setCopied(null), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const inviteLink = (invite: InviteResult) =>
    `${window.location.origin}/#invite=${encodeURIComponent(invite.code)}`;

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      setResult(await onCreateInvite(name.trim()));
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "创建邀请失败，请重试。",
      );
    } finally {
      setBusy(false);
    }
  };

  const copy = async (kind: "code" | "link") => {
    if (!result) {
      return;
    }
    const text =
      kind === "code"
        ? result.invite.code
        : inviteLink(result.invite);
    if (await copyText(text)) {
      setCopied(kind);
    } else {
      setError("无法访问剪贴板，请长按邀请码手动复制。");
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title={result ? "邀请好友" : "添加好友"}
      subtitle={
        result
          ? "把链接发给好友，对方登录或注册后就会进入你们的 1:1 房间。"
          : "好友会进入一个只有你们两个人的端到端加密房间。"
      }
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
        result ? (
          <div className="dialog-actions">
            <button type="button" className="primary-button" onClick={onClose}>
              完成
            </button>
          </div>
        ) : (
          <div className="dialog-actions">
            <button
              type="button"
              className="secondary-button"
              onClick={onClose}
              disabled={busy}
            >
              取消
            </button>
            <button
              type="button"
              className="primary-button"
              onClick={() => void create()}
              disabled={busy}
            >
              {busy ? <i className="spinner" /> : null}
              {busy ? "正在创建" : "生成邀请"}
            </button>
          </div>
        )
      }
    >
      {result ? (
        <div className="friend-invite-result">
          <div className="friend-invite-head">
            <span className="friend-invite-check">
              <Icon name="check" size={18} />
            </span>
            <div>
              <strong>邀请已经创建</strong>
              <p>
                {expiryLabel(result.invite.expires_at)} 前有效，仅可使用一次。
              </p>
            </div>
          </div>
          <div className="invite-result">
            <div className="invite-result-head">
              <span>邀请码</span>
              <span>发给好友</span>
            </div>
            <div className="invite-code">{result.invite.code}</div>
            <FormError message={error} />
            <div className="invite-actions">
              <button
                type="button"
                className="secondary-button"
                onClick={() => void copy("code")}
              >
                <Icon name="copy" size={15} />
                {copied === "code" ? "已复制" : "复制邀请码"}
              </button>
              <button
                type="button"
                className="secondary-button"
                onClick={() => void copy("link")}
              >
                <Icon name="link" size={15} />
                {copied === "link" ? "已复制" : "复制链接"}
              </button>
            </div>
          </div>
          <p className="friend-invite-note">
            好友打开链接后，用 TA 自己的账号登录或注册即可。这个房间只有你们两个人。
          </p>
        </div>
      ) : (
        <div className="dialog-form">
          <FormError message={error} />
          <label className="field">
            <span>
              好友昵称 <em>可选</em>
            </span>
            <input
              autoFocus
              type="text"
              maxLength={80}
              value={name}
              placeholder="例如：小王"
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          <div className="friend-join-line">
            <span>已经有好友的邀请？</span>
            <button
              type="button"
              className="text-button"
              onClick={onJoinExisting}
              disabled={busy}
            >
              输入邀请码
            </button>
          </div>
        </div>
      )}
    </Modal>
  );
}
