import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { APIError, api } from "./api";
import { formatAccountID, formatBytes, formatDay, formatTime, shortID } from "./format";
import { Lang, detectLang, localeOf, makeT } from "./i18n";
import { Markdown } from "./Markdown";
import type {
  Account,
  Agent,
  Approval,
  Attachment,
  Member,
  Room,
  RoomEvent,
  Status,
} from "./types";
import { Badge, CopyButton, Modal, Spinner } from "./ui";
import { AgentsView, ApprovalsView, AuditView, CommonProps, DevicesView, SearchView, SettingsView } from "./views";

type View = "chat" | "approvals" | "agents" | "devices" | "audit" | "search" | "settings";

function mergeEvents(previous: RoomEvent[], incoming: RoomEvent[]): RoomEvent[] {
  const seen = new Set(previous.map((event) => event.id));
  const merged = [...previous];
  for (const event of incoming) {
    if (!seen.has(event.id)) {
      seen.add(event.id);
      merged.push(event);
    }
  }
  return merged.sort((a, b) => a.seq - b.seq);
}

export default function App() {
  const [lang, setLangState] = useState<Lang>(() => detectLang());
  const t = useMemo(() => makeT(lang), [lang]);
  const locale = localeOf(lang);

  const [status, setStatus] = useState<Status | null>(null);
  const [rooms, setRooms] = useState<Room[]>([]);
  const [activeRoomID, setActiveRoomID] = useState<string | null>(null);
  const [view, setView] = useState<View>("chat");
  const [booting, setBooting] = useState(true);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const [navOpen, setNavOpen] = useState(false);

  const setLang = useCallback((next: Lang) => {
    localStorage.setItem("toget.lang", next);
    setLangState(next);
  }, []);

  const handleError = useCallback(
    (err: unknown) => {
      if (err instanceof APIError && err.status === 423) {
        setStatus((current) => (current ? { ...current, locked: true } : current));
        setError(t("lock.subtitle"));
        return;
      }
      setError(err instanceof Error ? err.message : t("common.error"));
    },
    [t],
  );

  const refreshRooms = useCallback(async () => {
    const result = await api.rooms();
    setRooms(result.rooms);
    setActiveRoomID((current) => {
      if (current && result.rooms.some((room) => room.id === current)) return current;
      return result.rooms[0]?.id ?? null;
    });
    return result.rooms;
  }, []);

  const refreshStatus = useCallback(async () => {
    const next = await api.status();
    setStatus(next);
    if (next.account && !next.locked) {
      await refreshRooms();
    }
  }, [refreshRooms]);

  useEffect(() => {
    let cancelled = false;
    async function boot() {
      try {
        const next = await api.status();
        if (cancelled) return;
        setStatus(next);
        if (next.account) {
          const session = await api.session();
          if (cancelled) return;
          if (!session.locked) {
            const result = await api.rooms();
            if (cancelled) return;
            setRooms(result.rooms);
            setActiveRoomID(result.rooms[0]?.id ?? null);
          }
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : t("boot.unreachable"));
      } finally {
        if (!cancelled) setBooting(false);
      }
    }
    void boot();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!status?.account || status.locked) return;
    const timer = window.setInterval(() => {
      void refreshStatus().catch(handleError);
    }, 8000);
    return () => window.clearInterval(timer);
  }, [status?.account?.id, status?.locked, refreshStatus, handleError]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 2600);
    return () => window.clearTimeout(timer);
  }, [toast]);

  const notify = useCallback((message: string) => setToast(message), []);

  const selectRoom = useCallback((roomID: string) => {
    setActiveRoomID(roomID);
    setView("chat");
    setNavOpen(false);
  }, []);

  async function lock() {
    try {
      await api.lock();
      setStatus((current) => (current ? { ...current, locked: true } : current));
      setNavOpen(false);
    } catch (err) {
      handleError(err);
    }
  }

  if (booting) {
    return (
      <div className="boot">
        <div className="brand-mark">T</div>
        <p>{t("boot.starting")}</p>
      </div>
    );
  }

  if (!status) {
    return (
      <div className="boot">
        <div className="brand-mark">T</div>
        <p>{error || t("boot.unreachable")}</p>
        <button className="button primary" onClick={() => window.location.reload()}>
          {t("boot.retry")}
        </button>
      </div>
    );
  }

  if (!status.account) {
    return (
      <Onboarding
        t={t}
        error={error}
        setError={setError}
        onCreated={async () => {
          setBooting(true);
          try {
            const next = await api.status();
            setStatus(next);
            const result = await api.rooms();
            setRooms(result.rooms);
            setActiveRoomID(result.rooms[0]?.id ?? null);
          } finally {
            setBooting(false);
          }
        }}
      />
    );
  }

  if (status.locked) {
    return (
      <LockScreen
        t={t}
        account={status.account}
        error={error}
        setError={setError}
        onUnlocked={async () => {
          const next = await api.status();
          setStatus(next);
          await refreshRooms();
        }}
      />
    );
  }

  const common: CommonProps = {
    t,
    lang,
    rooms,
    notify,
    onError: handleError,
    onRoomsChanged: async () => {
      await refreshRooms();
    },
    openRoom: selectRoom,
  };

  return (
    <div className={`app ${navOpen ? "nav-open" : ""}`}>
      <aside className="sidebar">
        <header className="sidebar-header">
          <div className="brand">
            <span className="brand-mark small">T</span>
            <span>Toget</span>
            <button className="icon-button nav-close" onClick={() => setNavOpen(false)} aria-label={t("common.close")}>
              ×
            </button>
          </div>
          <AccountCard account={status.account} t={t} />
        </header>

        <nav className="main-nav">
          <NavButton active={view === "chat"} onClick={() => setView("chat")} glyph="◉" label={t("nav.chat")} />
          <NavButton
            active={view === "approvals"}
            onClick={() => setView("approvals")}
            glyph="✓"
            label={t("nav.approvals")}
            badge={status.pending_approvals || undefined}
          />
          <NavButton active={view === "agents"} onClick={() => setView("agents")} glyph="✦" label={t("nav.agents")} />
          <NavButton active={view === "devices"} onClick={() => setView("devices")} glyph="▤" label={t("nav.devices")} />
          <NavButton active={view === "audit"} onClick={() => setView("audit")} glyph="⛓" label={t("nav.audit")} />
          <NavButton active={view === "search"} onClick={() => setView("search")} glyph="⌕" label={t("nav.search")} />
          <NavButton active={view === "settings"} onClick={() => setView("settings")} glyph="⚙" label={t("nav.settings")} />
        </nav>

        <div className="sidebar-section-title">
          <span>{t("sidebar.rooms")}</span>
          <span className="count">{rooms.length}</span>
        </div>
        <RoomList rooms={rooms} activeID={view === "chat" ? activeRoomID : null} onSelect={selectRoom} onCreate={async (name) => {
          const created = await api.createRoom(name);
          setRooms((current) => [created.room, ...current]);
          selectRoom(created.room.id);
        }} onJoin={async (code) => {
          const joined = await api.redeemInvite(code);
          await refreshRooms();
          selectRoom(joined.room.id);
        }} onError={handleError} t={t} />

        <footer className="sidebar-footer">
          <span className="status-dot" />
          <span>
            {t("sidebar.node")} · {status.listen}
          </span>
          <button className="icon-button lock-button" onClick={() => void lock()} title={t("settings.lockNow")}>
            ⏻
          </button>
        </footer>
      </aside>

      <main className="main">
        <div className="mobile-bar">
          <button className="icon-button" onClick={() => setNavOpen(true)} aria-label="Menu">
            ☰
          </button>
          <span className="brand-mark small">T</span>
          <span>{viewTitle(view, t)}</span>
        </div>
        {error && (
          <div className="error-banner">
            <span>{error}</span>
            <button className="icon-button" onClick={() => setError("")} aria-label={t("common.close")}>
              ×
            </button>
          </div>
        )}
        {view === "chat" &&
          (activeRoomID ? (
            <Conversation
              key={activeRoomID}
              room={rooms.find((room) => room.id === activeRoomID) ?? null}
              account={status.account}
              t={t}
              locale={locale}
              notify={notify}
              onError={handleError}
              onRoomGone={async () => {
                await refreshRooms();
                setView("chat");
              }}
            />
          ) : (
            <EmptyState
              t={t}
              onCreate={async (name) => {
                const created = await api.createRoom(name);
                setRooms((current) => [created.room, ...current]);
                selectRoom(created.room.id);
              }}
            />
          ))}
        {view === "approvals" && <ApprovalsView {...common} />}
        {view === "agents" && <AgentsView {...common} />}
        {view === "devices" && <DevicesView {...common} />}
        {view === "audit" && <AuditView {...common} />}
        {view === "search" && <SearchView {...common} />}
        {view === "settings" && (
          <SettingsView
            {...common}
            account={status.account}
            status={status}
            lang={lang}
            setLang={setLang}
            lock={lock}
          />
        )}
      </main>

      {toast && <div className="toast">{toast}</div>}
    </div>
  );
}

function viewTitle(view: View, t: (key: string) => string): string {
  switch (view) {
    case "chat":
      return t("nav.chat");
    case "approvals":
      return t("nav.approvals");
    case "agents":
      return t("nav.agents");
    case "devices":
      return t("nav.devices");
    case "audit":
      return t("nav.audit");
    case "search":
      return t("nav.search");
    default:
      return t("nav.settings");
  }
}

function NavButton({
  active,
  onClick,
  glyph,
  label,
  badge,
}: {
  active: boolean;
  onClick: () => void;
  glyph: string;
  label: string;
  badge?: number;
}) {
  return (
    <button className={`nav-button ${active ? "active" : ""}`} onClick={onClick}>
      <span className="nav-glyph">{glyph}</span>
      <span>{label}</span>
      {badge ? <span className="nav-badge">{badge}</span> : null}
    </button>
  );
}

function LockScreen({
  t,
  account,
  error,
  setError,
  onUnlocked,
}: {
  t: (key: string) => string;
  account: Account;
  error: string;
  setError: (value: string) => void;
  onUnlocked: () => Promise<void>;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.unlock(password);
      await onUnlocked();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="onboarding">
      <div className="onboarding-card compact">
        <div className="brand-mark">T</div>
        <p className="eyebrow">{account.name}</p>
        <h1>{t("lock.title")}</h1>
        <p className="muted">{t("lock.subtitle")}</p>
        {error && <div className="form-error">{error}</div>}
        <form onSubmit={submit}>
          <label>
            {t("lock.password")}
            <input
              autoFocus
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              required
            />
          </label>
          <button className="button primary wide" type="submit" disabled={busy}>
            {busy ? t("lock.unlocking") : t("lock.unlock")}
          </button>
        </form>
        <p className="fine-print">{t("lock.note")}</p>
      </div>
    </div>
  );
}

function Onboarding({
  t,
  error,
  setError,
  onCreated,
}: {
  t: (key: string) => string;
  error: string;
  setError: (value: string) => void;
  onCreated: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [deviceName, setDeviceName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (password.length < 8) {
      setError(t("onboard.passwordShort"));
      return;
    }
    if (password !== confirm) {
      setError(t("onboard.mismatch"));
      return;
    }
    setBusy(true);
    try {
      await api.createAccount(name, password, deviceName || undefined);
      await onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="onboarding">
      <div className="onboarding-card">
        <div className="brand-mark">T</div>
        <p className="eyebrow">{t("onboard.eyebrow")}</p>
        <h1>{t("onboard.title")}</h1>
        <p className="muted">{t("onboard.subtitle")}</p>
        {error && <div className="form-error">{error}</div>}
        <form onSubmit={submit}>
          <label>
            {t("onboard.name")}
            <input autoFocus value={name} onChange={(event) => setName(event.target.value)} maxLength={64} required />
          </label>
          <label>
            {t("onboard.password")}
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              minLength={8}
              required
            />
          </label>
          <label>
            {t("onboard.confirm")}
            <input
              type="password"
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
              minLength={8}
              required
            />
          </label>
          <label>
            {t("devices.title")} · {t("devices.current")}
            <input
              value={deviceName}
              onChange={(event) => setDeviceName(event.target.value)}
              placeholder="MacBook · Chrome"
              maxLength={64}
            />
          </label>
          <button className="button primary wide" type="submit" disabled={busy}>
            {busy ? t("onboard.creating") : t("onboard.create")}
          </button>
        </form>
        <p className="fine-print">{t("onboard.fine")}</p>
      </div>
    </div>
  );
}

function AccountCard({ account, t }: { account: Account; t: (key: string) => string }) {
  return (
    <div className="account-card">
      <div className="account-name">{account.name}</div>
      <div className="account-id-row">
        <code title={formatAccountID(account.id)}>{formatAccountID(account.id)}</code>
        <CopyButton value={account.id} labels={{ copy: t("common.copy"), copied: t("common.copied") }} />
      </div>
      {account.device && <p className="fine-print">{account.device.name}</p>}
    </div>
  );
}

function RoomList({
  rooms,
  activeID,
  onSelect,
  onCreate,
  onJoin,
  onError,
  t,
}: {
  rooms: Room[];
  activeID: string | null;
  onSelect: (id: string) => void;
  onCreate: (name: string) => Promise<void>;
  onJoin: (code: string) => Promise<void>;
  onError: (err: unknown) => void;
  t: (key: string) => string;
}) {
  const [mode, setMode] = useState<"create" | "join" | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      if (mode === "join") {
        await onJoin(name.trim());
      } else {
        await onCreate(name.trim());
      }
      setName("");
      setMode(null);
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  function closeForm() {
    setMode(null);
    setName("");
  }

  return (
    <div className="room-list">
      {rooms.map((room) => (
        <button
          key={room.id}
          className={`room-item ${room.id === activeID ? "active" : ""}`}
          onClick={() => onSelect(room.id)}
        >
          <span className="room-avatar">{room.name.slice(0, 1).toUpperCase()}</span>
          <span className="room-copy">
            <span className="room-name">{room.name}</span>
            <span className="room-meta">{room.topic || `Epoch ${room.epoch}`}</span>
          </span>
        </button>
      ))}
      {rooms.length === 0 && !mode && <p className="empty-hint">{t("room.empty.subtitle")}</p>}
      {mode ? (
        <form className="new-room" onSubmit={submit}>
          <input
            autoFocus
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={mode === "join" ? t("sidebar.joinPlaceholder") : t("sidebar.roomPlaceholder")}
            maxLength={mode === "join" ? 4096 : 80}
          />
          <div className="new-room-actions">
            <button type="button" className="button ghost" onClick={closeForm}>
              {t("common.cancel")}
            </button>
            <button type="submit" className="button primary" disabled={busy || !name.trim()}>
              {mode === "join" ? t("sidebar.join") : t("common.create")}
            </button>
          </div>
        </form>
      ) : (
        <>
          <button className="new-room-button" onClick={() => setMode("create")}>
            <span>+</span> {t("sidebar.newRoom")}
          </button>
          <button className="new-room-button secondary" onClick={() => setMode("join")}>
            <span>⇥</span> {t("sidebar.joinRoom")}
          </button>
        </>
      )}
    </div>
  );
}

function Conversation({
  room,
  account,
  t,
  locale,
  notify,
  onError,
  onRoomGone,
}: {
  room: Room | null;
  account: Account;
  t: (key: string, vars?: Record<string, string | number>) => string;
  locale: string;
  notify: (message: string) => void;
  onError: (err: unknown) => void;
  onRoomGone: () => Promise<void>;
}) {
  const [events, setEvents] = useState<RoomEvent[]>([]);
  const [members, setMembers] = useState<Member[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [approvals, setApprovals] = useState<Record<string, Approval>>({});
  const [attachments, setAttachments] = useState<Record<string, Attachment>>({});
  const [text, setText] = useState("");
  const [format, setFormat] = useState<"plain" | "markdown">("plain");
  const [files, setFiles] = useState<File[]>([]);
  const [sending, setSending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [showInfo, setShowInfo] = useState(false);
  const lastSeq = useRef(0);
  const bottomRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const loadEvents = useCallback(async () => {
    if (!room) return;
    const result = await api.events(room.id, lastSeq.current, 500);
    if (result.events.length > 0) {
      lastSeq.current = Math.max(lastSeq.current, result.events[result.events.length - 1].seq);
      setEvents((current) => mergeEvents(current, result.events));
      void api.markRead(room.id, lastSeq.current).catch(() => undefined);
    }
  }, [room]);

  const loadContext = useCallback(async () => {
    if (!room) return;
    const [memberResult, agentResult, approvalResult, attachmentResult] = await Promise.all([
      api.members(room.id),
      api.roomAgents(room.id),
      api.approvals(room.id),
      api.attachments(room.id),
    ]);
    setMembers(memberResult.members);
    setAgents(agentResult.agents);
    setApprovals(Object.fromEntries(approvalResult.approvals.map((approval) => [approval.id, approval])));
    setAttachments(Object.fromEntries(attachmentResult.attachments.map((attachment) => [attachment.id, attachment])));
  }, [room]);

  useEffect(() => {
    if (!room) return;
    lastSeq.current = 0;
    setEvents([]);
    setLoading(true);
    let cancelled = false;
    (async () => {
      try {
        await loadEvents();
        await loadContext();
      } catch (err) {
        if (!cancelled) onError(err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    const eventTimer = window.setInterval(() => {
      void loadEvents().catch(() => undefined);
    }, 2000);
    const contextTimer = window.setInterval(() => {
      void loadContext().catch(() => undefined);
    }, 8000);
    return () => {
      cancelled = true;
      window.clearInterval(eventTimer);
      window.clearInterval(contextTimer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [room?.id]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [events.length, room?.id]);

  if (!room) return null;

  async function send() {
    if (sending || !room) return;
    const value = text.trim();
    if (!value && files.length === 0) return;
    setSending(true);
    try {
      const uploaded: string[] = [];
      for (const file of files) {
        if (file.size > 25 * 1024 * 1024) throw new Error(`${file.name}: file is too large`);
        const attachment = await api.uploadAttachment(room.id, file);
        uploaded.push(attachment.id);
        setAttachments((current) => ({ ...current, [attachment.id]: attachment }));
      }
      const event = await api.send(room.id, {
        text: value,
        format,
        attachments: uploaded.length ? uploaded : undefined,
      });
      setEvents((current) => mergeEvents(current, [event]));
      lastSeq.current = Math.max(lastSeq.current, event.seq);
      setText("");
      setFiles([]);
      if (fileRef.current) fileRef.current.value = "";
    } catch (err) {
      onError(err);
    } finally {
      setSending(false);
    }
  }

  async function decide(approval: Approval, decision: "grant" | "reject") {
    try {
      const result = await api.decideApproval(approval.id, decision);
      setApprovals((current) => ({ ...current, [approval.id]: result.approval }));
      notify(decision === "grant" ? t("approval.granted") : t("approval.rejected"));
      await loadEvents();
    } catch (err) {
      onError(err);
    }
  }

  const memberNames = Object.fromEntries(members.map((member) => [member.account_id, member.display_name]));
  const agentNames = Object.fromEntries(agents.map((agent) => [agent.id, agent.name]));

  return (
    <section className="conversation">
      <header className="conversation-header">
        <div>
          <h2>{room.name}</h2>
          <p>
            {t("conversation.private")} · {events.length} {t("conversation.events")} · {t("conversation.home")}{" "}
            {shortID(room.home_node)}
          </p>
        </div>
        <div className="conversation-actions">
          <div className="room-badge">{t("conversation.encrypted")}</div>
          <button className="button ghost small" onClick={() => setShowInfo(true)}>
            {t("conversation.info")}
          </button>
        </div>
      </header>

      <div className="timeline">
        {loading && <Spinner label={t("common.loading")} />}
        {!loading && events.length === 0 && (
          <div className="timeline-empty">
            <p>{t("conversation.quiet")}</p>
            <span>{t("conversation.first")}</span>
          </div>
        )}
        {events.map((event, index) => {
          const previous = events[index - 1];
          const newDay = !previous || formatDay(previous.created_at, locale) !== formatDay(event.created_at, locale);
          return (
            <div key={event.id}>
              {newDay && <div className="day-divider">{formatDay(event.created_at, locale)}</div>}
              <EventRow
                event={event}
                account={account}
                t={t}
                locale={locale}
                memberNames={memberNames}
                agentNames={agentNames}
                approvals={approvals}
                attachments={attachments}
                onDecide={decide}
              />
            </div>
          );
        })}
        <div ref={bottomRef} />
      </div>

      <div className="composer">
        {files.length > 0 && (
          <div className="file-chips">
            {files.map((file, index) => (
              <span className="file-chip" key={`${file.name}-${index}`}>
                {file.name} · {formatBytes(file.size)}
                <button
                  className="mini"
                  onClick={() => setFiles((current) => current.filter((_, position) => position !== index))}
                  aria-label={t("common.delete")}
                >
                  ×
                </button>
              </span>
            ))}
          </div>
        )}
        <div className="composer-row">
          <textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                void send();
              }
            }}
            placeholder={`${t("conversation.placeholder")}  ${t("conversation.mentionsHint")}`}
            rows={1}
            maxLength={64 * 1024}
          />
          <div className="composer-actions">
            <button
              className={`icon-button format-toggle ${format === "markdown" ? "active" : ""}`}
              onClick={() => setFormat((current) => (current === "plain" ? "markdown" : "plain"))}
              title="Markdown"
            >
              M↓
            </button>
            <button className="icon-button" onClick={() => fileRef.current?.click()} title={t("conversation.attach")}>
              ⌘
            </button>
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(event) => {
                const selected = Array.from(event.target.files ?? []);
                setFiles((current) => [...current, ...selected].slice(0, 10));
              }}
            />
            <button className="button primary send" onClick={() => void send()} disabled={sending || (!text.trim() && files.length === 0)}>
              {sending ? t("conversation.sending") : t("conversation.send")}
            </button>
          </div>
        </div>
      </div>

      {showInfo && (
        <RoomInfo
          room={room}
          account={account}
          t={t}
          members={members}
          agents={agents}
          onClose={() => setShowInfo(false)}
          notify={notify}
          onError={onError}
          onChanged={loadContext}
          onRoomGone={async () => {
            setShowInfo(false);
            await onRoomGone();
          }}
        />
      )}
    </section>
  );
}

function EventRow({
  event,
  account,
  t,
  locale,
  memberNames,
  agentNames,
  approvals,
  attachments,
  onDecide,
}: {
  event: RoomEvent;
  account: Account;
  t: (key: string, vars?: Record<string, string | number>) => string;
  locale: string;
  memberNames: Record<string, string>;
  agentNames: Record<string, string>;
  approvals: Record<string, Approval>;
  attachments: Record<string, Attachment>;
  onDecide: (approval: Approval, decision: "grant" | "reject") => Promise<void>;
}) {
  const payload = event.payload ?? {};
  switch (event.type) {
    case "room.created":
      return <SystemEvent text={t("event.roomCreated", { name: String(payload.name ?? "") })} />;
    case "room.settings_changed":
      return <SystemEvent text={t("event.settings")} />;
    case "room.archived":
      return <SystemEvent text={t("event.archived")} />;
    case "room.member_joined":
      return <SystemEvent text={t("event.joined", { name: String(payload.display_name || shortID(String(payload.account_id))) })} />;
    case "room.member_left":
      return <SystemEvent text={t("event.left", { name: memberNames[String(payload.account_id)] || shortID(String(payload.account_id)) })} />;
    case "room.member_invited":
      return <SystemEvent text={t("event.invited", { history: String(payload.history ?? "none") })} />;
    case "room.history_attached":
      return (
        <SystemEvent
          text={t("event.historyAttached", {
            name: memberNames[String(payload.account_id)] || shortID(String(payload.account_id)),
            choice: String(payload.choice ?? "none"),
          })}
        />
      );
    case "room.epoch_rotated":
      return <SystemEvent text={t("event.epoch", { epoch: Number(payload.epoch ?? event.epoch) })} />;
    case "agent.joined":
      return <SystemEvent text={`${String(payload.name ?? payload.agent_id)} + ${t("nav.chat")}`} />;
    case "agent.left":
      return <SystemEvent text={`${String(payload.name ?? payload.agent_id)} − ${t("nav.chat")}`} />;
    case "agent.status":
      return (
        <SystemEvent
          text={t("event.agentStatus", {
            name: agentNames[String(payload.agent_id)] || shortID(String(payload.agent_id)),
            status: String(payload.status),
          })}
        />
      );
    case "message.edited":
      return (
        <article className="message">
          <div className="message-meta">
            <span>{memberNames[event.author_id] || shortID(event.author_id)}</span>
            <time>{formatTime(event.created_at, locale)}</time>
          </div>
          <div className="bubble">
            <Markdown text={String(payload.text ?? "")} />
            <span className="fine-print"> · {t("conversation.edited")}</span>
          </div>
        </article>
      );
    case "message.posted": {
      const mine = event.author_id === account.id;
      const body = String(payload.text ?? "");
      const attachmentIDs = Array.isArray(payload.attachments) ? (payload.attachments as string[]) : [];
      const mentions = Array.isArray(payload.mentions) ? (payload.mentions as string[]) : [];
      return (
        <article className={`message ${mine ? "mine" : ""}`}>
          <div className="message-meta">
            <span>{mine ? account.name : memberNames[event.author_id] || shortID(event.author_id)}</span>
            <time>{formatTime(event.created_at, locale)}</time>
          </div>
          <div className="bubble">
            {body && (payload.format === "markdown" ? <Markdown text={body} /> : <p className="plain-text">{body}</p>)}
            {mentions.length > 0 && <p className="fine-print">@ {mentions.join(" ")}</p>}
            {attachmentIDs.length > 0 && (
              <div className="attachment-list">
                {attachmentIDs.map((id) => {
                  const attachment = attachments[id];
                  return (
                    <a
                      className="attachment"
                      key={id}
                      href={api.attachmentURL(id)}
                      download={attachment?.name}
                    >
                      <span className="attachment-icon">▣</span>
                      <span>
                        <strong>{attachment?.name ?? shortID(id)}</strong>
                        <small>
                          {attachment ? `${formatBytes(attachment.size)} · ${attachment.mime}` : t("common.loading")}
                        </small>
                      </span>
                    </a>
                  );
                })}
              </div>
            )}
          </div>
        </article>
      );
    }
    case "approval.requested": {
      const approval = approvals[String(payload.approval_id)];
      const status = approval?.status ?? "pending";
      const description = approval?.description ?? String(payload.description ?? "");
      return (
        <article className={`approval-card inline ${status}`}>
          <div className="approval-top">
            <span className="approval-title">{t("approval.requested")}</span>
            <Badge status={status} />
          </div>
          <p className="approval-description">{description}</p>
          <p className="fine-print">
            {t("approval.agent")}: {agentNames[String(payload.agent_id)] || shortID(String(payload.agent_id))} ·{" "}
            {t("approval.expires")}: {new Date(String(payload.expires_at)).toLocaleString()}
          </p>
          {approval && (
            <details>
              <summary>{t("approval.details")}</summary>
              <pre className="json-block">{JSON.stringify(approval.action, null, 2)}</pre>
              <p className="fine-print">
                {t("approval.digest")}: <code>{approval.action_digest}</code>
              </p>
            </details>
          )}
          {status === "pending" && approval && (
            <div className="approval-actions">
              <button className="button ghost" onClick={() => void onDecide(approval, "reject")}>
                {t("approval.reject")}
              </button>
              <button className="button primary" onClick={() => void onDecide(approval, "grant")}>
                {t("approval.approve")}
              </button>
            </div>
          )}
        </article>
      );
    }
    case "approval.granted":
    case "approval.rejected":
    case "approval.expired":
    case "approval.consumed": {
      const id = String(payload.approval_id);
      return (
        <SystemEvent
          text={`${t("approval.requested")} · ${approvals[id]?.description ?? id} · ${event.type.replace("approval.", "")}`}
        />
      );
    }
    case "tool.requested":
    case "tool.result":
    case "tool.failed":
      return (
        <article className="tool-card">
          <div className="tool-head">
            <span className="chip">{String(payload.tool ?? "tool")}</span>
            <Badge status={String(payload.status ?? event.type.replace("tool.", ""))} />
          </div>
          {payload.input != null && <pre className="json-block">{JSON.stringify(payload.input, null, 2)}</pre>}
          {payload.output && <pre className="json-block output">{String(payload.output)}</pre>}
        </article>
      );
    default:
      return <SystemEvent text={event.type} />;
  }
}

function SystemEvent({ text }: { text: string }) {
  return (
    <div className="system-event">
      <span>{text}</span>
    </div>
  );
}

function RoomInfo({
  room,
  account,
  t,
  members,
  agents,
  onClose,
  notify,
  onError,
  onChanged,
  onRoomGone,
}: {
  room: Room;
  account: Account;
  t: (key: string, vars?: Record<string, string | number>) => string;
  members: Member[];
  agents: Agent[];
  onClose: () => void;
  notify: (message: string) => void;
  onError: (err: unknown) => void;
  onChanged: () => Promise<void>;
  onRoomGone: () => Promise<void>;
}) {
  const [history, setHistory] = useState("none");
  const [invite, setInvite] = useState<string>("");
  const [busy, setBusy] = useState(false);
  const remote = Boolean(room.home_server_id && room.home_server_id !== account.server_id);
  const isOwner = room.created_by === account.id;

  async function createInvite() {
    setBusy(true);
    try {
      const result = await api.createInvite(room.id, history);
      setInvite(result.invite.code || result.invite.secret || "");
      await onChanged();
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function leave() {
    if (!window.confirm(t("roomInfo.leave") + "?")) return;
    try {
      await api.leaveRoom(room.id);
      notify(t("roomInfo.left"));
      await onRoomGone();
    } catch (err) {
      onError(err);
    }
  }

  async function archive() {
    if (!window.confirm(t("roomInfo.archive") + "?")) return;
    try {
      await api.archiveRoom(room.id);
      notify(t(remote ? "roomInfo.archivedLocal" : "roomInfo.archived"));
      await onRoomGone();
    } catch (err) {
      onError(err);
    }
  }

  return (
    <Modal title={t("roomInfo.title")} onClose={onClose} wide>
      <div className="room-info">
        <section>
          <h3>{t("roomInfo.members")}</h3>
          <ul className="people-list">
            {members.map((member) => (
              <li key={member.account_id}>
                <span className="person-avatar">{member.display_name.slice(0, 1).toUpperCase()}</span>
                <span>
                  {member.display_name}
                  <small>
                    {member.role} · {member.status}
                  </small>
                </span>
              </li>
            ))}
          </ul>
        </section>
        <section>
          <h3>{t("roomInfo.agents")}</h3>
          {agents.length === 0 && <p className="muted">{t("roomInfo.noAgents")}</p>}
          <ul className="people-list">
            {agents.map((agent) => (
              <li key={agent.id}>
                <span className="person-avatar agent">{agent.name.slice(0, 1).toUpperCase()}</span>
                <span>
                  {agent.name}
                  <small>
                    {agent.runtime} · {agent.status}
                  </small>
                </span>
              </li>
            ))}
          </ul>
        </section>
        <section className="wide-section">
          <h3>{t("roomInfo.invite")}</h3>
          {remote ? (
            <p className="muted">{t("roomInfo.remoteNotice")}</p>
          ) : (
            <>
              <div className="invite-controls">
                <select value={history} onChange={(event) => setHistory(event.target.value)}>
                  <option value="none">{t("roomInfo.none")}</option>
                  <option value="recent">{t("roomInfo.recent")}</option>
                  <option value="all">{t("roomInfo.all")}</option>
                </select>
                <button className="button primary" onClick={() => void createInvite()} disabled={busy}>
                  {t("roomInfo.createInvite")}
                </button>
              </div>
              {invite && (
                <div className="invite-secret">
                  <code>{invite}</code>
                  <CopyButton value={invite} labels={{ copy: t("roomInfo.copy"), copied: t("roomInfo.copied") }} />
                </div>
              )}
              <p className="fine-print">{t("roomInfo.shareHint")}</p>
            </>
          )}
        </section>
        <section className="wide-section">
          <dl className="facts">
            <div>
              <dt>{t("roomInfo.epoch")}</dt>
              <dd>{room.epoch}</dd>
            </div>
            <div>
              <dt>{t("roomInfo.role")}</dt>
              <dd>{members.find((member) => member.account_id === room.created_by)?.role ?? "member"}</dd>
            </div>
            <div>
              <dt>{t("roomInfo.home")}</dt>
              <dd>{remote ? t("roomInfo.remote") : t("roomInfo.local")}</dd>
            </div>
          </dl>
          <div className="danger-actions">
            <button className="button ghost" onClick={() => void leave()}>
              {t("roomInfo.leave")}
            </button>
            {(remote || isOwner) && (
              <button className="button ghost danger" onClick={() => void archive()}>
                {t(remote ? "roomInfo.archiveLocal" : "roomInfo.archive")}
              </button>
            )}
          </div>
        </section>
      </div>
    </Modal>
  );
}

function EmptyState({ t, onCreate }: { t: (key: string) => string; onCreate: (name: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      await onCreate(name.trim());
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="empty-state">
      <div className="empty-orb">
        <span />
        <span />
        <span />
      </div>
      <h2>{t("room.empty.title")}</h2>
      <p>{t("room.empty.subtitle")}</p>
      <form className="empty-form" onSubmit={submit}>
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder={t("room.empty.placeholder")}
          maxLength={80}
        />
        <button className="button primary" type="submit" disabled={busy || !name.trim()}>
          {busy ? t("common.loading") : t("room.empty.create")}
        </button>
      </form>
    </div>
  );
}
