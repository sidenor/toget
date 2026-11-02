import { useEffect, useState } from "react";
import { roomSummaryAvatar } from "./avatars";
import { FormError, Modal } from "./dialogs";
import { copyText, expiryLabel, listTime, roomPreview } from "./helpers";
import type {
  FriendInviteResult,
  FriendRequestSent,
  FriendRequestView,
  InviteResult,
  ResolvedAccount,
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

export interface FriendRequestActions {
  requests: FriendRequestView[];
  busyID: string | null;
  onAccept: (requestID: string) => void;
  onDecline: (requestID: string) => void;
}

function FriendRequestSection({ actions }: { actions: FriendRequestActions }) {
  const { requests, busyID, onAccept, onDecline } = actions;
  if (requests.length === 0) {
    return null;
  }
  return (
    <div className="friend-request-section">
      <div className="friend-request-head">
        <span>好友申请</span>
        <span className="friend-request-count">{requests.length}</span>
      </div>
      {requests.map((request) => {
        const busy = busyID === request.id;
        return (
          <div className="friend-request-row" key={request.id}>
            <span className="friend-lookup-avatar friend-request-avatar">
              <Icon name="person" size={19} />
            </span>
            <span className="friend-request-copy">
              <strong>{request.from_name?.trim() || "一位新朋友"}</strong>
              <small>
                想加你为好友
                {request.room_name ? ` · ${request.room_name}` : ""}
              </small>
            </span>
            <span className="friend-request-actions">
              <button
                type="button"
                className="text-button friend-request-decline"
                onClick={() => onDecline(request.id)}
                disabled={busy}
              >
                拒绝
              </button>
              <button
                type="button"
                className="primary-button small"
                onClick={() => onAccept(request.id)}
                disabled={busy}
              >
                {busy ? "处理中" : "接受"}
              </button>
            </span>
          </div>
        );
      })}
    </div>
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
  requests,
  requestBusyID,
  onAcceptRequest,
  onDeclineRequest,
  onOpen,
  onAdd,
  onJoin,
}: {
  entries: FriendEntry[];
  requests: FriendRequestView[];
  requestBusyID: string | null;
  onAcceptRequest: (requestID: string) => void;
  onDeclineRequest: (requestID: string) => void;
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
      <FriendRequestSection
        actions={{
          requests,
          busyID: requestBusyID,
          onAccept: onAcceptRequest,
          onDecline: onDeclineRequest,
        }}
      />
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
  requests,
  requestBusyID,
  onAcceptRequest,
  onDeclineRequest,
  onOpen,
  onAdd,
  onJoin,
}: {
  entries: FriendEntry[];
  requests: FriendRequestView[];
  requestBusyID: string | null;
  onAcceptRequest: (requestID: string) => void;
  onDeclineRequest: (requestID: string) => void;
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
      <FriendRequestSection
        actions={{
          requests,
          busyID: requestBusyID,
          onAccept: onAcceptRequest,
          onDecline: onDeclineRequest,
        }}
      />
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
  requests,
  requestBusyID,
  onAcceptRequest,
  onDeclineRequest,
  onOpen,
  onAdd,
}: {
  entries: FriendEntry[];
  requests: FriendRequestView[];
  requestBusyID: string | null;
  onAcceptRequest: (requestID: string) => void;
  onDeclineRequest: (requestID: string) => void;
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
          <FriendRequestSection
            actions={{
              requests,
              busyID: requestBusyID,
              onAccept: onAcceptRequest,
              onDecline: onDeclineRequest,
            }}
          />
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
  myHostCode,
  onClose,
  onCreateInvite,
  onResolveAccount,
  onAddFriend,
  onJoinExisting,
}: {
  isMobile: boolean;
  myHostCode: string;
  onClose: () => void;
  onCreateInvite: (name: string) => Promise<FriendInviteResult>;
  onResolveAccount: (accountID: string) => Promise<ResolvedAccount>;
  onAddFriend: (resolved: ResolvedAccount) => Promise<FriendRequestSent>;
  onJoinExisting: () => void;
}) {
  const [mode, setMode] = useState<"id" | "invite">("id");
  const [name, setName] = useState("");
  const [accountID, setAccountID] = useState("");
  const [busy, setBusy] = useState(false);
  const [lookingUp, setLookingUp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [resolved, setResolved] = useState<ResolvedAccount | null>(null);
  const [result, setResult] = useState<FriendInviteResult | null>(null);
  const [sent, setSent] = useState<FriendRequestSent | null>(null);
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

  const normalizedID = accountID.replace(/\D/g, "").slice(0, 14);
  const sameHost =
    resolved?.kind === "account" &&
    Boolean(myHostCode) &&
    resolved.host_code === myHostCode;
  const canCreateRoom = Boolean(resolved && sameHost && !resolved.self);

  const lookup = async () => {
    setLookingUp(true);
    setLookupError(null);
    try {
      setResolved(await onResolveAccount(normalizedID));
    } catch (cause) {
      setLookupError(
        cause instanceof Error ? cause.message : "没有找到这个账号，请检查 ID。",
      );
    } finally {
      setLookingUp(false);
    }
  };

  const create = async (friendName = name) => {
    setBusy(true);
    setError(null);
    try {
      setResult(await onCreateInvite(friendName.trim()));
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "创建邀请失败，请重试。",
      );
    } finally {
      setBusy(false);
    }
  };

  const add = async () => {
    if (!resolved) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      setSent(await onAddFriend(resolved));
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "发送好友申请失败，请重试。",
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

  const switchMode = (next: "id" | "invite") => {
    setMode(next);
    setError(null);
    setLookupError(null);
  };

  const subtitle = sent
    ? "等 TA 同意后，你们就会进入一个只有两个人的端到端加密房间。"
    : result
      ? "把链接发给好友，对方登录或注册后就会进入你们的 1:1 房间。"
      : mode === "id"
        ? "输入对方的 14 位账号 ID，先通过查号台确认 TA 是谁。"
        : "好友会进入一个只有你们两个人的端到端加密房间。";

  return (
    <Modal
      isMobile={isMobile}
      title={sent ? "好友申请已发送" : result ? "邀请好友" : "添加好友"}
      subtitle={subtitle}
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
        sent ? (
          <div className="dialog-actions">
            <button type="button" className="primary-button" onClick={onClose}>
              完成
            </button>
          </div>
        ) : result ? (
          <div className="dialog-actions">
            <button type="button" className="primary-button" onClick={onClose}>
              完成
            </button>
          </div>
        ) : mode === "id" ? (
          resolved ? (
            <div className="dialog-actions">
              <button
                type="button"
                className="secondary-button"
                onClick={() => setResolved(null)}
                disabled={busy}
              >
                重新查找
              </button>
              {canCreateRoom && (
                <button
                  type="button"
                  className="primary-button"
                  onClick={() => void add()}
                  disabled={busy}
                >
                  {busy ? <i className="spinner" /> : null}
                  {busy ? "正在发送" : "发送好友申请"}
                </button>
              )}
            </div>
          ) : (
            <div className="dialog-actions">
              <button
                type="button"
                className="secondary-button"
                onClick={onClose}
                disabled={lookingUp}
              >
                取消
              </button>
              <button
                type="button"
                className="primary-button"
                onClick={() => void lookup()}
                disabled={lookingUp || normalizedID.length !== 14}
              >
                {lookingUp ? <i className="spinner" /> : null}
                {lookingUp ? "正在查找" : "查找好友"}
              </button>
            </div>
          )
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
      {sent ? (
        <div className="friend-invite-result friend-request-sent">
          <div className="friend-invite-head">
            <span className="friend-invite-check">
              <Icon name="check" size={18} />
            </span>
            <div>
              <strong>已经送到 TA 的收件箱</strong>
              <p>{expiryLabel(sent.expires_at)} 前有效。</p>
            </div>
          </div>
          <p className="friend-invite-note">
            TA 打开好友页、点「接受」后，你们就会进入同一个 1:1
            加密房间。期间这个房间会显示「待加入」。
          </p>
        </div>
      ) : result ? (
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
      ) : mode === "id" ? (
        <div className="dialog-form">
          {resolved ? (
            <>
              <div className="friend-lookup-card">
                <span className="friend-lookup-avatar">
                  <Icon name="person" size={22} />
                </span>
                <div className="friend-lookup-copy">
                  <strong>{resolved.name?.trim() || "未命名账号"}</strong>
                  <span className="friend-lookup-id">
                    {resolved.account_id}
                  </span>
                  <span className="friend-lookup-server">
                    {resolved.self
                      ? "这是你自己的账号"
                      : resolved.kind === "node"
                        ? `自建节点 · ${resolved.host_url}`
                        : `${resolved.server_name?.trim() || "Toget 服务器"}${
                            sameHost ? " · 当前服务器" : ` · ${resolved.host_code}`
                          }`}
                  </span>
                </div>
              </div>
              {resolved.self ? (
                <p className="friend-invite-note">
                  把 ID 发给朋友，让 TA 来加你吧。
                </p>
              ) : resolved.kind === "node" ? (
                <p className="friend-invite-note">
                  这是自建节点的主人账号。这个节点还没有开通账号主页，暂时不能直接添加。
                </p>
              ) : sameHost ? (
                <p className="friend-invite-note">
                  找到啦。发送好友申请，等 TA 同意后就会进入你们的 1:1 加密房间。
                </p>
              ) : (
                <p className="friend-invite-note">
                  已经通过查号台找到 TA 所在的服务器。跨服务器好友申请还在开发中，暂时还不能直接添加。
                </p>
              )}
            </>
          ) : (
            <>
              <FormError message={lookupError} />
              <label className="field">
                <span>好友的账号 ID</span>
                <input
                  autoFocus
                  type="text"
                  inputMode="numeric"
                  autoComplete="off"
                  maxLength={14}
                  value={normalizedID}
                  placeholder="14 位数字，例如 74854782000123"
                  onChange={(event) => {
                    setAccountID(event.target.value);
                    setLookupError(null);
                  }}
                  onKeyDown={(event) => {
                    if (
                      event.key === "Enter" &&
                      normalizedID.length === 14 &&
                      !lookingUp
                    ) {
                      void lookup();
                    }
                  }}
                />
              </label>
              <p className="friend-invite-note">
                在「我」页面可以看到自己的 ID。对方的 ID 也会显示在 TA 的个人资料里。
              </p>
            </>
          )}
          <div className="friend-join-line">
            <span>只有邀请码？</span>
            <button
              type="button"
              className="text-button"
              onClick={() => switchMode("invite")}
              disabled={busy || lookingUp}
            >
              用邀请码添加
            </button>
          </div>
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
            <span>知道对方的 ID？</span>
            <button
              type="button"
              className="text-button"
              onClick={() => switchMode("id")}
              disabled={busy}
            >
              按 ID 查找
            </button>
          </div>
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
