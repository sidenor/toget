import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { memberAvatar, roomAvatar } from "./avatars";
import { copyText, downloadFile, expiryLabel } from "./helpers";
import type { InviteResult, Profile, RecoveryFile, RoomView } from "./types";
import { Avatar, Icon } from "./ui";

export function Modal({
  isMobile,
  title,
  subtitle,
  onClose,
  children,
  footer,
  wide = false,
  dismissible = true,
  className = "",
  label,
}: {
  isMobile: boolean;
  title?: string;
  subtitle?: string;
  onClose?: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  dismissible?: boolean;
  className?: string;
  label?: string;
}) {
  useEffect(() => {
    if (!dismissible || !onClose) {
      return;
    }
    const close = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [dismissible, onClose]);

  return (
    <div
      className={`sheet-backdrop ${isMobile ? "sheet-mobile-backdrop" : ""}`}
      onClick={(event) => {
        if (
          dismissible &&
          onClose &&
          event.target === event.currentTarget
        ) {
          onClose();
        }
      }}
    >
      <div
        className={`sheet ${isMobile ? "sheet-mobile" : ""} ${
          wide ? "sheet-wide" : ""
        } ${className}`}
        role="dialog"
        aria-modal="true"
        aria-label={label || title || "对话框"}
      >
        {isMobile && <span className="sheet-grabber" />}
        {title && (
          <div className="sheet-head">
            <div className="sheet-head-copy">
              <h2>{title}</h2>
              {subtitle && <p>{subtitle}</p>}
            </div>
            {dismissible && onClose && (
              <button
                type="button"
                className="sheet-close"
                aria-label="关闭"
                onClick={onClose}
              >
                <Icon name="close" size={18} />
              </button>
            )}
          </div>
        )}
        <div className="sheet-body">{children}</div>
        {footer && <div className="sheet-footer">{footer}</div>}
      </div>
    </div>
  );
}

export function FormError({ message }: { message: string | null }) {
  if (!message) {
    return null;
  }
  return (
    <div className="form-alert" role="alert">
      <Icon name="info" size={16} />
      <span>{message}</span>
    </div>
  );
}

export function CreateRoomDialog({
  isMobile,
  onClose,
  onCreate,
}: {
  isMobile: boolean;
  onClose: () => void;
  onCreate: (name: string, topic: string) => Promise<boolean>;
}) {
  const [name, setName] = useState("");
  const [topic, setTopic] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    if (!name.trim()) {
      setError("请填写房间名称。");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (await onCreate(name.trim(), topic.trim())) {
        return;
      }
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "创建房间失败，请重试。",
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title="新房间"
      subtitle="房间名称和消息都会在设备上加密。"
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
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
            onClick={() => void submit()}
            disabled={busy || !name.trim()}
          >
            {busy ? <i className="spinner" /> : null}
            {busy ? "正在创建" : "创建"}
          </button>
        </div>
      }
    >
      <form
        className="dialog-form"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <FormError message={error} />
        <label className="field">
          <span>名称</span>
          <input
            autoFocus
            type="text"
            maxLength={80}
            value={name}
            placeholder="例如：周末计划"
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <label className="field">
          <span>说明 <em>可选</em></span>
          <textarea
            rows={3}
            maxLength={280}
            value={topic}
            placeholder="这个房间用来做什么？"
            onChange={(event) => setTopic(event.target.value)}
          />
          <small className="field-count">{topic.length}/280</small>
        </label>
      </form>
    </Modal>
  );
}

function extractInvite(input: string): string {
  const value = input.trim();
  if (value.startsWith("toget-invite:")) {
    return value;
  }
  try {
    const url = new URL(value);
    const encoded =
      url.hash.replace(/^#/, "").startsWith("invite=")
        ? url.hash.replace(/^#/, "").slice("invite=".length)
        : url.searchParams.get("invite");
    if (encoded) {
      return decodeURIComponent(encoded).trim();
    }
  } catch {
    // It may still be a bare invite code with surrounding whitespace.
  }
  const marker = value.indexOf("toget-invite:");
  if (marker >= 0) {
    return value.slice(marker).split(/\s+/)[0];
  }
  return value;
}

export function JoinRoomDialog({
  isMobile,
  initialCode,
  onClose,
  onJoin,
}: {
  isMobile: boolean;
  initialCode: string;
  onClose: () => void;
  onJoin: (code: string) => Promise<boolean>;
}) {
  const [code, setCode] = useState(initialCode);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    const invite = extractInvite(code);
    if (!invite) {
      setError("请粘贴邀请码或邀请链接。");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (await onJoin(invite)) {
        return;
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "加入失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title="加入房间"
      subtitle="邀请码只能使用一次，加入后就能看到约定的历史消息。"
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
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
            onClick={() => void submit()}
            disabled={busy || !code.trim()}
          >
            {busy ? <i className="spinner" /> : null}
            {busy ? "正在加入" : "加入"}
          </button>
        </div>
      }
    >
      <form
        className="dialog-form"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <FormError message={error} />
        <label className="field">
          <span>邀请码</span>
          <textarea
            autoFocus
            rows={5}
            value={code}
            placeholder="粘贴 toget-invite:v1:… 或邀请链接"
            spellCheck={false}
            onChange={(event) => setCode(event.target.value)}
          />
        </label>
      </form>
    </Modal>
  );
}

export function RoomInfoDialog({
  isMobile,
  room,
  profile,
  onClose,
  onRename,
  onLeave,
  onCreateInvite,
}: {
  isMobile: boolean;
  room: RoomView;
  profile: Profile;
  onClose: () => void;
  onRename: (name: string, topic: string) => Promise<boolean>;
  onLeave: () => Promise<boolean>;
  onCreateInvite: (
    history: "recent" | "all" | "none",
    ttlHours: number,
  ) => Promise<InviteResult>;
}) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(room.summary.name);
  const [topic, setTopic] = useState(room.summary.topic ?? "");
  const [renameBusy, setRenameBusy] = useState(false);
  const [renameError, setRenameError] = useState<string | null>(null);
  const [history, setHistory] = useState<"recent" | "all" | "none">("recent");
  const [ttl, setTtl] = useState(168);
  const [inviteBusy, setInviteBusy] = useState(false);
  const [inviteError, setInviteError] = useState<string | null>(null);
  const [invite, setInvite] = useState<InviteResult | null>(null);
  const [copied, setCopied] = useState<"code" | "link" | null>(null);
  const [leaveArmed, setLeaveArmed] = useState(false);
  const [leaveBusy, setLeaveBusy] = useState(false);
  const [leaveError, setLeaveError] = useState<string | null>(null);

  useEffect(() => {
    if (!editing) {
      setName(room.summary.name);
      setTopic(room.summary.topic ?? "");
    }
  }, [editing, room.summary.name, room.summary.topic]);

  useEffect(() => {
    setInvite(null);
    setInviteError(null);
    setLeaveArmed(false);
    setLeaveError(null);
    setEditing(false);
  }, [room.summary.id]);

  useEffect(() => {
    if (!copied) {
      return;
    }
    const timer = window.setTimeout(() => setCopied(null), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const avatar = roomAvatar(room.summary.id, room.summary.name);
  const ownerName =
    room.members.find(
      (member) => member.account_id === room.summary.owner_account_id,
    )?.display_name ?? "创建者";

  const submitRename = async () => {
    if (!name.trim()) {
      setRenameError("房间名称不能为空。");
      return;
    }
    setRenameBusy(true);
    setRenameError(null);
    try {
      if (await onRename(name.trim(), topic.trim())) {
        setEditing(false);
      }
    } catch (cause) {
      setRenameError(
        cause instanceof Error ? cause.message : "保存失败，请重试。",
      );
    } finally {
      setRenameBusy(false);
    }
  };

  const createInvite = async () => {
    setInviteBusy(true);
    setInviteError(null);
    try {
      const result = await onCreateInvite(history, ttl);
      setInvite(result);
    } catch (cause) {
      setInviteError(
        cause instanceof Error ? cause.message : "创建邀请失败，请重试。",
      );
    } finally {
      setInviteBusy(false);
    }
  };

  const copyInvite = async (kind: "code" | "link") => {
    if (!invite) {
      return;
    }
    const text =
      kind === "code"
        ? invite.code
        : `${window.location.origin}/#invite=${encodeURIComponent(invite.code)}`;
    if (await copyText(text)) {
      setCopied(kind);
    } else {
      setInviteError("无法访问剪贴板，请长按邀请码手动复制。");
    }
  };

  const leave = async () => {
    setLeaveBusy(true);
    setLeaveError(null);
    try {
      if (await onLeave()) {
        return;
      }
    } catch (cause) {
      setLeaveError(
        cause instanceof Error ? cause.message : "离开失败，请重试。",
      );
    } finally {
      setLeaveBusy(false);
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title="房间信息"
      onClose={leaveBusy ? undefined : onClose}
      dismissible={!leaveBusy}
      wide
      className="room-info-dialog"
    >
      <div className="room-info-hero">
        <Avatar spec={avatar} size={62} />
        {editing ? (
          <div className="room-edit-form">
            <label className="field compact-field">
              <span>名称</span>
              <input
                autoFocus
                type="text"
                maxLength={80}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <label className="field compact-field">
              <span>说明</span>
              <textarea
                rows={2}
                maxLength={280}
                value={topic}
                placeholder="可选"
                onChange={(event) => setTopic(event.target.value)}
              />
            </label>
            <FormError message={renameError} />
            <div className="inline-actions">
              <button
                type="button"
                className="text-button"
                onClick={() => {
                  setEditing(false);
                  setRenameError(null);
                }}
                disabled={renameBusy}
              >
                取消
              </button>
              <button
                type="button"
                className="primary-button small"
                onClick={() => void submitRename()}
                disabled={renameBusy}
              >
                {renameBusy ? <i className="spinner" /> : null}
                保存
              </button>
            </div>
          </div>
        ) : (
          <div className="room-info-copy">
            <h3>{room.summary.name}</h3>
            <p>
              {room.summary.topic?.trim() ||
                `${room.members.length} 位成员 · 端到端加密`}
            </p>
            {room.can_rename && (
              <button
                type="button"
                className="text-button"
                onClick={() => setEditing(true)}
              >
                编辑名称和说明
              </button>
            )}
          </div>
        )}
      </div>

      {!editing && (
        <>
          <div className="info-caption">
            成员 · {room.members.length}
          </div>
          <div className="member-list card">
            {room.members.map((member) => (
              <div className="member-row" key={member.account_id}>
                <Avatar
                  spec={memberAvatar(member.account_id, member.display_name)}
                  size={36}
                />
                <div className="member-copy">
                  <strong>
                    {member.display_name}
                    {member.account_id === profile.account_id && (
                      <span className="member-you">我</span>
                    )}
                  </strong>
                  <small>
                    {member.role === "owner" ? "房间所有者" : "成员"}
                  </small>
                </div>
                {member.account_id === room.summary.owner_account_id && (
                  <span className="owner-badge">所有者</span>
                )}
              </div>
            ))}
          </div>

          {room.can_invite && (
            <>
              <div className="info-caption">邀请新成员</div>
              <div className="invite-card card">
                <p className="invite-hint">
                  邀请码只有一次有效。对方加入后，会把当前房间的密钥交给 TA。
                </p>
                <div className="invite-options">
                  <label className="field compact-field">
                    <span>对方可以看到</span>
                    <select
                      value={history}
                      onChange={(event) =>
                        setHistory(
                          event.target.value as "recent" | "all" | "none",
                        )
                      }
                    >
                      <option value="recent">最近的消息</option>
                      <option value="none">加入之后的消息</option>
                      <option value="all">全部历史消息</option>
                    </select>
                  </label>
                  <label className="field compact-field">
                    <span>有效期</span>
                    <select
                      value={ttl}
                      onChange={(event) => setTtl(Number(event.target.value))}
                    >
                      <option value={24}>1 天</option>
                      <option value={168}>7 天</option>
                      <option value={720}>30 天</option>
                    </select>
                  </label>
                </div>
                {!invite ? (
                  <>
                    <FormError message={inviteError} />
                    <button
                      type="button"
                      className="primary-button invite-create"
                      onClick={() => void createInvite()}
                      disabled={inviteBusy}
                    >
                      {inviteBusy ? <i className="spinner" /> : null}
                      {inviteBusy ? "正在创建" : "创建邀请码"}
                    </button>
                  </>
                ) : (
                  <div className="invite-result">
                    <div className="invite-result-head">
                      <span>邀请码</span>
                      <span>{expiryLabel(invite.expires_at)} 前有效</span>
                    </div>
                    <div className="invite-code">{invite.code}</div>
                    <FormError message={inviteError} />
                    <div className="invite-actions">
                      <button
                        type="button"
                        className="secondary-button"
                        onClick={() => void copyInvite("code")}
                      >
                        <Icon name="copy" size={15} />
                        {copied === "code" ? "已复制" : "复制邀请码"}
                      </button>
                      <button
                        type="button"
                        className="secondary-button"
                        onClick={() => void copyInvite("link")}
                      >
                        <Icon name="link" size={15} />
                        {copied === "link" ? "已复制" : "复制链接"}
                      </button>
                    </div>
                    <button
                      type="button"
                      className="text-button make-another"
                      onClick={() => {
                        setInvite(null);
                        setCopied(null);
                      }}
                    >
                      再创建一个
                    </button>
                  </div>
                )}
              </div>
            </>
          )}

          <div className="info-caption">房间</div>
          <div className="card about-card">
            <div className="about-row">
              <span>所有者</span>
              <span>{ownerName}</span>
            </div>
            <div className="about-row">
              <span>加密</span>
              <span className="service-state ok">
                <i />
                端到端加密
              </span>
            </div>
            <div className="about-row">
              <span>房间编号</span>
              <span className="mono dim">{room.summary.id}</span>
            </div>
          </div>

          <div className="danger-zone">
            {leaveError && <FormError message={leaveError} />}
            {!leaveArmed ? (
              <button
                type="button"
                className="danger-button"
                onClick={() => setLeaveArmed(true)}
              >
                离开房间
              </button>
            ) : (
              <div className="confirm-card">
                <p>
                  离开后，这个浏览器会删除房间密钥；想回来需要新的邀请码。
                  {room.can_rename &&
                    " 你是房间所有者，离开后房间将没有所有者，其他成员也无法再改名。"}
                </p>
                <div className="dialog-actions">
                  <button
                    type="button"
                    className="secondary-button"
                    onClick={() => setLeaveArmed(false)}
                    disabled={leaveBusy}
                  >
                    再想想
                  </button>
                  <button
                    type="button"
                    className="danger-button solid"
                    onClick={() => void leave()}
                    disabled={leaveBusy}
                  >
                    {leaveBusy ? <i className="spinner" /> : null}
                    {leaveBusy ? "正在离开" : "确认离开"}
                  </button>
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </Modal>
  );
}

export function RecoveryDialog({
  isMobile,
  file,
  rotated,
  onDone,
}: {
  isMobile: boolean;
  file: RecoveryFile;
  rotated: boolean;
  onDone: () => void;
}) {
  const [downloaded, setDownloaded] = useState(false);
  const [confirmed, setConfirmed] = useState(false);

  const save = () => {
    downloadFile(file.filename, file.content);
    setDownloaded(true);
  };

  return (
    <Modal
      isMobile={isMobile}
      title={rotated ? "新的恢复文件" : "保存恢复文件"}
      subtitle={
        rotated
          ? "旧恢复文件已经失效。"
          : "这是恢复账号的唯一文件，服务器没有副本。"
      }
      dismissible={false}
      className="recovery-dialog"
      footer={
        <button
          type="button"
          className="primary-button recovery-done"
          disabled={!downloaded || !confirmed}
          onClick={onDone}
        >
          {rotated ? "完成" : "开始使用 Toget"}
        </button>
      }
    >
      <div className="recovery-illustration">
        <span className="recovery-file-icon">
          <Icon name="file" size={28} />
          <i>
            <Icon name="lock" size={11} />
          </i>
        </span>
        <div>
          <strong>{file.filename}</strong>
          <p>和密码一起，用来在新设备上找回这个账号。</p>
        </div>
      </div>

      <button type="button" className="primary-button download-button" onClick={save}>
        <Icon name="download" size={17} />
        {downloaded ? "重新下载文件" : "下载恢复文件"}
      </button>

      <ul className="recovery-points">
        <li>请放在密码管理器、加密磁盘或离线备份里。</li>
        <li>不要发到聊天、邮箱或云笔记的公开链接里。</li>
        <li>
          文件丢失后，只能依靠密码登录；密码也丢失时，账号将无法恢复。
        </li>
        {rotated && <li>生成新文件后，之前的恢复文件已经不能再用。</li>}
      </ul>

      <label className={`check-row ${!downloaded ? "disabled" : ""}`}>
        <input
          type="checkbox"
          checked={confirmed}
          disabled={!downloaded}
          onChange={(event) => setConfirmed(event.target.checked)}
        />
        <span>我已经把文件保存在安全的地方</span>
      </label>
    </Modal>
  );
}

export function RecoverAccountDialog({
  isMobile,
  initialUser,
  onClose,
  onRequest,
}: {
  isMobile: boolean;
  initialUser: string;
  onClose: () => void;
  onRequest: (user: string) => Promise<void>;
}) {
  const [user, setUser] = useState(initialUser);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);

  const submit = async () => {
    const value = user.trim();
    if (!value) {
      setError("请输入账号或邮箱。");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await onRequest(value);
      setSent(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "发送失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title={sent ? "邮件已发送" : "找回账号"}
      subtitle={
        sent
          ? "请打开邮件里的链接，按提示用恢复文件设置新密码。"
          : "我们会向已验证的邮箱发送重置链接。"
      }
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      className="recover-dialog"
      footer={
        sent ? (
          <button type="button" className="primary-button" onClick={onClose}>
            完成
          </button>
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
              onClick={() => void submit()}
              disabled={busy}
            >
              {busy ? <i className="spinner" /> : null}
              {busy ? "正在发送" : "发送重置邮件"}
            </button>
          </div>
        )
      }
    >
      {sent ? (
        <div className="recover-sent">
          <span className="recover-sent-icon">
            <Icon name="mail" size={22} />
          </span>
          <div>
            <strong>去邮箱看看</strong>
            <p>
              如果这个账号存在、邮箱也已经验证，重置邮件很快就会到。链接
              30 分钟内有效。
            </p>
            <p className="dim">
              没有收到？请确认邮箱已经验证，或稍后再试一次。
            </p>
          </div>
        </div>
      ) : (
        <form
          className="dialog-form"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <label className="field">
            <span>账号或邮箱</span>
            <input
              type="text"
              autoComplete="username"
              value={user}
              placeholder="14 位账号，或注册时验证的邮箱"
              onChange={(event) => setUser(event.target.value)}
              autoFocus
            />
          </label>
          <FormError message={error} />
        </form>
      )}
    </Modal>
  );
}

export function LogoutDialog({
  isMobile,
  busy,
  onClose,
  onLogout,
}: {
  isMobile: boolean;
  busy: boolean;
  onClose: () => void;
  onLogout: () => void;
}) {
  return (
    <Modal
      isMobile={isMobile}
      title="退出登录"
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
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
            className="danger-button solid"
            onClick={onLogout}
            disabled={busy}
          >
            {busy ? <i className="spinner" /> : null}
            {busy ? "正在退出" : "退出"}
          </button>
        </div>
      }
    >
      <div className="logout-copy">
        <p>
          这台浏览器上的房间密钥和登录状态会被清除，其他设备不受影响。
        </p>
        <p>
          下次登录需要账号密码；如果忘记密码，需要恢复文件。
        </p>
      </div>
    </Modal>
  );
}

export function ConfirmDialog({
  isMobile,
  title,
  message,
  confirmLabel,
  danger = false,
  busy = false,
  error = null,
  onClose,
  onConfirm,
}: {
  isMobile: boolean;
  title: string;
  message: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  error?: string | null;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal
      isMobile={isMobile}
      title={title}
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
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
            className={danger ? "danger-button solid" : "primary-button"}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? <i className="spinner" /> : null}
            {confirmLabel}
          </button>
        </div>
      }
    >
      <div className="confirm-message">
        {message}
        <FormError message={error} />
      </div>
    </Modal>
  );
}

