import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Account, Room, RoomEvent, Status, api } from "./api";

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

function formatTime(iso: string): string {
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(iso));
}

function formatDay(iso: string): string {
  const date = new Date(iso);
  const today = new Date();
  if (date.toDateString() === today.toDateString()) return "Today";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(date);
}

function shortID(id: string): string {
  return id.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [rooms, setRooms] = useState<Room[]>([]);
  const [activeRoomID, setActiveRoomID] = useState<string | null>(null);
  const [events, setEvents] = useState<RoomEvent[]>([]);
  const [booting, setBooting] = useState(true);
  const [error, setError] = useState("");

  const activeRoom = rooms.find((room) => room.id === activeRoomID) ?? null;
  const account = status?.account ?? null;

  const refreshStatus = useCallback(async () => {
    const next = await api.status();
    setStatus(next);
    if (next.account) {
      const result = await api.rooms();
      setRooms(result.rooms);
      setActiveRoomID((current) => current ?? result.rooms[0]?.id ?? null);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    async function boot() {
      try {
        const next = await api.status();
        if (cancelled) return;
        setStatus(next);
        if (next.account) {
          const result = await api.rooms();
          if (cancelled) return;
          setRooms(result.rooms);
          setActiveRoomID(result.rooms[0]?.id ?? null);
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not reach the local node");
      } finally {
        if (!cancelled) setBooting(false);
      }
    }
    void boot();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!account) return;
    const timer = window.setInterval(() => {
      void refreshStatus().catch((err) => {
        setError(err instanceof Error ? err.message : "Could not refresh the local node");
      });
    }, 5000);
    return () => window.clearInterval(timer);
  }, [account, refreshStatus]);

  useEffect(() => {
    if (!activeRoomID) {
      setEvents([]);
      return;
    }
    let cancelled = false;
    let lastSeq = 0;
    async function load() {
      try {
        const result = await api.events(activeRoomID!, lastSeq);
        if (cancelled || result.events.length === 0) return;
        lastSeq = result.events[result.events.length - 1].seq;
        setEvents((current) => mergeEvents(current, result.events));
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Could not load messages");
        }
      }
    }
    setEvents([]);
    void load();
    const timer = window.setInterval(() => void load(), 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [activeRoomID]);

  async function onCreateRoom(name: string) {
    const created = await api.createRoom(name);
    setRooms((current) => [created.room, ...current]);
    setActiveRoomID(created.room.id);
  }

  if (booting) {
    return (
      <div className="boot">
        <div className="brand-mark">T</div>
        <p>Starting your local node…</p>
      </div>
    );
  }

  if (!status) {
    return (
      <div className="boot">
        <div className="brand-mark">T</div>
        <p>{error || "The local node is not reachable."}</p>
        <button className="button primary" onClick={() => window.location.reload()}>
          Try again
        </button>
      </div>
    );
  }

  if (!account) {
    return <Onboarding onCreated={refreshStatus} error={error} setError={setError} />;
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <header className="sidebar-header">
          <div className="brand">
            <span className="brand-mark small">T</span>
            <span>Toget</span>
          </div>
          <AccountCard account={account} />
        </header>

        <div className="sidebar-section-title">
          <span>Rooms</span>
          <span className="count">{rooms.length}</span>
        </div>
        <RoomList
          rooms={rooms}
          activeID={activeRoomID}
          onSelect={setActiveRoomID}
          onCreate={onCreateRoom}
          onError={setError}
        />
        <footer className="sidebar-footer">
          <span className="status-dot" />
          Local node · {status.listen}
        </footer>
      </aside>

      <main className="main">
        {error && (
          <div className="error-banner">
            <span>{error}</span>
            <button className="icon-button" onClick={() => setError("")} aria-label="Dismiss error">
              ×
            </button>
          </div>
        )}
        {activeRoom ? (
          <Conversation
            room={activeRoom}
            events={events}
            account={account}
            onError={setError}
          />
        ) : (
          <EmptyState onCreate={onCreateRoom} />
        )}
      </main>
    </div>
  );
}

function Onboarding({
  onCreated,
  error,
  setError,
}: {
  onCreated: () => Promise<void>;
  error: string;
  setError: (value: string) => void;
}) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (password.length < 8) {
      setError("Use at least 8 characters for the password.");
      return;
    }
    if (password !== confirm) {
      setError("The two passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api.createAccount(name, password);
      await onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create the account");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="onboarding">
      <div className="onboarding-card">
        <div className="brand-mark">T</div>
        <p className="eyebrow">A local-first place for people and Agents</p>
        <h1>Create your Toget account</h1>
        <p className="muted">
          Your keys and data stay on this machine. You can add more devices later.
        </p>
        {error && <div className="form-error">{error}</div>}
        <form onSubmit={submit}>
          <label>
            Your name
            <input
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="Aiden"
              maxLength={64}
              required
            />
          </label>
          <label>
            Password
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder="At least 8 characters"
              minLength={8}
              required
            />
          </label>
          <label>
            Confirm password
            <input
              type="password"
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
              placeholder="Type it again"
              minLength={8}
              required
            />
          </label>
          <button className="button primary wide" disabled={busy} type="submit">
            {busy ? "Creating…" : "Create account"}
          </button>
        </form>
        <p className="fine-print">
          This is an early build. The account lives on this machine; back up the data directory.
        </p>
      </div>
    </div>
  );
}

function AccountCard({ account }: { account: Account }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(account.id);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="account-card">
      <div className="account-name">{account.name}</div>
      <button className="account-id" onClick={copy} title="Copy account ID">
        <span>{account.id.slice(0, 8)}</span>
        <span className="dash">-</span>
        <span>{account.id.slice(8)}</span>
        <span className="copy-hint">{copied ? "copied" : "copy"}</span>
      </button>
    </div>
  );
}

function RoomList({
  rooms,
  activeID,
  onSelect,
  onCreate,
  onError,
}: {
  rooms: Room[];
  activeID: string | null;
  onSelect: (id: string) => void;
  onCreate: (name: string) => Promise<void>;
  onError: (message: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    onError("");
    try {
      await onCreate(name.trim());
      setName("");
      setAdding(false);
    } catch (err) {
      onError(err instanceof Error ? err.message : "Could not create the room");
    } finally {
      setBusy(false);
    }
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
            <span className="room-meta">Epoch {room.epoch}</span>
          </span>
        </button>
      ))}
      {rooms.length === 0 && !adding && <p className="empty-hint">No rooms yet. Make the first one.</p>}
      {adding ? (
        <form className="new-room" onSubmit={submit}>
          <input
            autoFocus
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Room name"
            maxLength={80}
          />
          <div className="new-room-actions">
            <button type="button" className="button ghost" onClick={() => setAdding(false)}>
              Cancel
            </button>
            <button type="submit" className="button primary" disabled={busy || !name.trim()}>
              Create
            </button>
          </div>
        </form>
      ) : (
        <button className="new-room-button" onClick={() => setAdding(true)}>
          <span>+</span> New room
        </button>
      )}
    </div>
  );
}

function Conversation({
  room,
  events,
  account,
  onError,
}: {
  room: Room;
  events: RoomEvent[];
  account: Account;
  onError: (message: string) => void;
}) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const lastSeq = events.length > 0 ? events[events.length - 1].seq : 0;

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [events.length, room.id]);

  async function send() {
    const value = text.trim();
    if (!value || sending) return;
    setSending(true);
    onError("");
    try {
      await api.send(room.id, value);
      setText("");
      // The next poll picks up the event; this keeps the composer responsive.
    } catch (err) {
      onError(err instanceof Error ? err.message : "Could not send the message");
    } finally {
      setSending(false);
    }
  }

  return (
    <section className="conversation">
      <header className="conversation-header">
        <div>
          <h2>{room.name}</h2>
          <p>
            Private room · {lastSeq} {lastSeq === 1 ? "event" : "events"} · home {shortID(room.home_node)}
          </p>
        </div>
        <div className="room-badge">encrypted locally</div>
      </header>

      <div className="timeline">
        {events.length === 0 && (
          <div className="timeline-empty">
            <p>This room is quiet.</p>
            <span>Send the first message.</span>
          </div>
        )}
        {events.map((event, index) => {
          const previous = events[index - 1];
          const newDay = !previous || formatDay(previous.created_at) !== formatDay(event.created_at);
          return (
            <div key={event.id}>
              {newDay && <div className="day-divider">{formatDay(event.created_at)}</div>}
              <EventRow event={event} account={account} />
            </div>
          );
        })}
        <div ref={bottomRef} />
      </div>

      <div className="composer">
        <textarea
          value={text}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              void send();
            }
          }}
          placeholder="Write a message…"
          rows={1}
          maxLength={64 * 1024}
        />
        <button className="button primary send" onClick={() => void send()} disabled={sending || !text.trim()}>
          {sending ? "…" : "Send"}
        </button>
      </div>
    </section>
  );
}

function EventRow({ event, account }: { event: RoomEvent; account: Account }) {
  if (event.type === "room.created") {
    const name = String(event.payload.name ?? "this room");
    return (
      <div className="system-event">
        <span>Room “{name}” created</span>
      </div>
    );
  }

  if (event.type !== "message.posted") {
    return (
      <div className="system-event">
        <span>{event.type}</span>
      </div>
    );
  }

  const mine = event.author_id === account.id;
  const text = String(event.payload.text ?? "");
  return (
    <article className={`message ${mine ? "mine" : ""}`}>
      <div className="message-meta">
        <span>{mine ? account.name : shortID(event.author_id)}</span>
        <time>{formatTime(event.created_at)}</time>
      </div>
      <div className="bubble">{text}</div>
    </article>
  );
}

function EmptyState({ onCreate }: { onCreate: (name: string) => Promise<void> }) {
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
      <h2>Your rooms live here</h2>
      <p>Start a room for a project, a trip, or a conversation with an Agent.</p>
      <form className="empty-form" onSubmit={submit}>
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="Give the room a name"
          maxLength={80}
        />
        <button className="button primary" type="submit" disabled={busy || !name.trim()}>
          {busy ? "Creating…" : "Create room"}
        </button>
      </form>
    </div>
  );
}
