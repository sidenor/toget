import { FormEvent, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { formatAccountID, formatTime, shortID } from "./format";
import type { Lang, Translate } from "./i18n";
import type { Agent, AgentSession, Approval, AuditResponse, Device, DiscoveredPeer, NodeInfo, PairingView, Room, SearchHit } from "./types";
import { Badge, CopyButton, Modal, Spinner } from "./ui";

export type CommonProps = {
  t: Translate;
  lang: Lang;
  rooms: Room[];
  notify: (message: string) => void;
  onError: (err: unknown) => void;
  onRoomsChanged: () => Promise<void> | void;
  openRoom: (roomID: string) => void;
};

function statusLabel(t: Translate, status: string): string {
  switch (status) {
    case "idle":
      return t("agents.idle");
    case "running":
      return t("agents.running");
    case "waiting_approval":
      return t("agents.waiting");
    case "offline":
      return t("agents.offline");
    case "revoked":
      return t("agents.revoked");
    default:
      return status;
  }
}

export function ApprovalsView({ t, rooms, notify, onError, openRoom }: CommonProps) {
  const [items, setItems] = useState<{ approval: Approval; room: Room }[]>([]);
  const [loading, setLoading] = useState(true);

  async function load() {
    try {
      const all = await Promise.all(
        rooms.map(async (room) => {
          const result = await api.approvals(room.id);
          return result.approvals.map((approval) => ({ approval, room }));
        }),
      );
      setItems(all.flat().sort((a, b) => a.approval.created_at.localeCompare(b.approval.created_at)));
    } catch (err) {
      onError(err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rooms.map((room) => room.id).join(",")]);

  async function decide(approval: Approval, decision: "grant" | "reject") {
    try {
      await api.decideApproval(approval.id, decision);
      notify(decision === "grant" ? t("approval.granted") : t("approval.rejected"));
      await load();
    } catch (err) {
      onError(err);
    }
  }

  const pending = items.filter((item) => item.approval.status === "pending");
  const past = items.filter((item) => item.approval.status !== "pending");

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.approvals")}</p>
          <h1>{t("approval.title")}</h1>
          <p className="muted">{t("approval.subtitle")}</p>
        </div>
      </header>
      {loading && <Spinner label={t("common.loading")} />}
      {!loading && pending.length === 0 && past.length === 0 && <p className="empty-hint">{t("approval.none")}</p>}
      {pending.length > 0 && (
        <div className="approval-grid">
          {pending.map(({ approval, room }) => (
            <article className="approval-card pending" key={approval.id}>
              <div className="approval-top">
                <span className="approval-title">{approval.action.kind}</span>
                <Badge status={approval.status} />
              </div>
              <p className="approval-description">{approval.description}</p>
              <dl className="facts">
                <div>
                  <dt>{t("common.room")}</dt>
                  <dd>
                    <button className="link" onClick={() => openRoom(room.id)}>
                      {room.name}
                    </button>
                  </dd>
                </div>
                <div>
                  <dt>{t("approval.agent")}</dt>
                  <dd>{shortID(approval.agent_id)}</dd>
                </div>
                <div>
                  <dt>{t("approval.expires")}</dt>
                  <dd>{new Date(approval.expires_at).toLocaleString()}</dd>
                </div>
              </dl>
              <details>
                <summary>{t("approval.details")}</summary>
                <pre className="json-block">{JSON.stringify(approval.action, null, 2)}</pre>
                <p className="fine-print">
                  {t("approval.digest")}: <code>{approval.action_digest}</code>
                </p>
              </details>
              <div className="approval-actions">
                <button className="button ghost" onClick={() => void decide(approval, "reject")}>
                  {t("approval.reject")}
                </button>
                <button className="button primary" onClick={() => void decide(approval, "grant")}>
                  {t("approval.approve")}
                </button>
              </div>
            </article>
          ))}
        </div>
      )}
      {past.length > 0 && (
        <div className="past-approvals">
          <h3>{t("common.actions")}</h3>
          {past.slice(0, 50).map(({ approval, room }) => (
            <div className="past-row" key={approval.id}>
              <Badge status={approval.status} />
              <span className="past-description">{approval.description}</span>
              <span className="muted">{room.name}</span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

export function AgentsView({ t, rooms, notify, onError, onRoomsChanged, openRoom }: CommonProps) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [membership, setMembership] = useState<Record<string, string[]>>({});
  const [sessions, setSessions] = useState<Record<string, AgentSession[]>>({});
  const [loading, setLoading] = useState(true);
  const [showAdd, setShowAdd] = useState(false);
  const [name, setName] = useState("");
  const [runtime, setRuntime] = useState("claude-code");
  const [perms, setPerms] = useState({ write: false, shell: false, external: false });
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const result = await api.agents();
      setAgents(result.agents);
      const pairs = await Promise.all(
        result.agents.map(async (agent) => {
          const [refs, state] = await Promise.all([api.agentRooms(agent.id), api.agentSessions(agent.id)]);
          return [agent.id, refs.rooms.map((room) => room.room_id), state.sessions] as const;
        }),
      );
      setMembership(Object.fromEntries(pairs.map(([id, roomIDs]) => [id, roomIDs])));
      setSessions(Object.fromEntries(pairs.map(([id, , list]) => [id, list])));
    } catch (err) {
      onError(err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function create(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      await api.createAgent(name.trim(), runtime, perms);
      setName("");
      setShowAdd(false);
      notify(`${name.trim()} ${t("agents.add")}`);
      await load();
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function setStatus(agent: Agent, status: string) {
    try {
      const shared = membership[agent.id] ?? [];
      await api.setAgentStatus(agent.id, status, shared[0] ?? "");
      await load();
    } catch (err) {
      onError(err);
    }
  }

  async function join(agentID: string, roomID: string) {
    if (!roomID) return;
    try {
      await api.joinAgent(roomID, agentID);
      await load();
      await onRoomsChanged();
    } catch (err) {
      onError(err);
    }
  }

  async function leave(agentID: string, roomID: string) {
    try {
      await api.leaveAgent(roomID, agentID);
      await load();
    } catch (err) {
      onError(err);
    }
  }

  async function stop(agent: Agent) {
    try {
      await api.stopAgent(agent.id);
      notify(t("agents.stopped"));
      await load();
    } catch (err) {
      onError(err);
    }
  }

  async function resetSession(agent: Agent, roomID: string) {
    if (!window.confirm(t("agents.resetConfirm"))) return;
    try {
      await api.resetAgentSession(agent.id, roomID);
      notify(t("agents.resetDone"));
      await load();
    } catch (err) {
      onError(err);
    }
  }

  async function revoke(agent: Agent) {
    if (!window.confirm(t("agents.revoke") + " " + agent.name + "?")) return;
    try {
      await api.revokeAgent(agent.id);
      await load();
    } catch (err) {
      onError(err);
    }
  }

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.agents")}</p>
          <h1>{t("agents.title")}</h1>
          <p className="muted">{t("agents.subtitle")}</p>
        </div>
        <button className="button primary" onClick={() => setShowAdd(true)}>
          + {t("agents.add")}
        </button>
      </header>
      {loading && <Spinner label={t("common.loading")} />}
      {!loading && agents.length === 0 && <p className="empty-hint">{t("agents.none")}</p>}
      <div className="agent-grid">
        {agents.map((agent) => {
          const shared = membership[agent.id] ?? [];
          const agentSessions = sessions[agent.id] ?? [];
          const available = rooms.filter((room) => !shared.includes(room.id));
          return (
            <article className={`agent-card status-${agent.status}`} key={agent.id}>
              <div className="agent-top">
                <div className="agent-avatar">{agent.name.slice(0, 1).toUpperCase()}</div>
                <div>
                  <h3>{agent.name}</h3>
                  <p className="muted">
                    {agent.runtime} · {shortID(agent.id)}
                  </p>
                </div>
                <Badge status={agent.status} />
              </div>
              <p className="agent-status-line">
                {t("agents.status")}: <strong>{statusLabel(t, agent.status)}</strong>
              </p>
              <div className="permission-chips">
                {Object.entries(agent.permissions ?? {}).map(([key, value]) =>
                  value ? (
                    <span className="chip" key={key}>
                      {key}
                    </span>
                  ) : null,
                )}
              </div>
              <div className="agent-rooms">
                <span className="field-label">{t("agents.rooms")}</span>
                {shared.length === 0 && <span className="muted">{t("agents.notInRoom")}</span>}
                {shared.map((roomID) => {
                  const room = rooms.find((item) => item.id === roomID);
                  return (
                    <span className="room-chip" key={roomID}>
                      <button className="link" onClick={() => openRoom(roomID)}>
                        {room?.name ?? shortID(roomID)}
                      </button>
                      <button className="mini" onClick={() => void leave(agent.id, roomID)} aria-label={t("agents.leave")}>
                        ×
                      </button>
                    </span>
                  );
                })}
              </div>
              {agentSessions.length > 0 && (
                <div className="agent-rooms">
                  <span className="field-label">{t("agents.sessions")}</span>
                  {agentSessions.map((session) => {
                    const room = rooms.find((item) => item.id === session.room_id);
                    return (
                      <span className="room-chip session-chip" key={session.room_id}>
                        <button className="link" onClick={() => openRoom(session.room_id)}>
                          {session.room_name || room?.name || shortID(session.room_id)}
                        </button>
                        <span className="session-meta">
                          {t("agents.runs", { count: session.run_count })}
                          {session.status !== "idle" ? ` · ${statusLabel(t, session.status)}` : ""}
                        </span>
                        {session.status === "running" && (
                          <button
                            className="mini"
                            onClick={() => void stop(agent)}
                            aria-label={t("agents.stop")}
                            title={t("agents.stop")}
                          >
                            ■
                          </button>
                        )}
                        <button
                          className="mini"
                          onClick={() => void resetSession(agent, session.room_id)}
                          aria-label={t("agents.resetSession")}
                          title={t("agents.resetSession")}
                        >
                          ↺
                        </button>
                      </span>
                    );
                  })}
                </div>
              )}
              {agent.status !== "revoked" && (
                <div className="agent-actions">
                  <select value={available[0]?.id ?? ""} onChange={(event) => void join(agent.id, event.target.value)}>
                    <option value="">{t("agents.join")}…</option>
                    {available.map((room) => (
                      <option value={room.id} key={room.id}>
                        {room.name}
                      </option>
                    ))}
                  </select>
                  <select value={agent.status} onChange={(event) => void setStatus(agent, event.target.value)}>
                    <option value="idle">{t("agents.idle")}</option>
                    <option value="running">{t("agents.running")}</option>
                    <option value="waiting_approval">{t("agents.waiting")}</option>
                    <option value="offline">{t("agents.offline")}</option>
                  </select>
                  <button className="button ghost small danger" onClick={() => void revoke(agent)}>
                    {t("agents.revoke")}
                  </button>
                </div>
              )}
            </article>
          );
        })}
      </div>
      {showAdd && (
        <Modal title={t("agents.add")} onClose={() => setShowAdd(false)}>
          <form onSubmit={create} className="form-stack">
            <label>
              {t("agents.name")}
              <input value={name} onChange={(event) => setName(event.target.value)} maxLength={64} autoFocus required />
            </label>
            <label>
              {t("agents.runtime")}
              <select value={runtime} onChange={(event) => setRuntime(event.target.value)}>
                <option value="claude-code">Claude Code</option>
                <option value="codex">Codex</option>
                <option value="custom">Custom</option>
              </select>
            </label>
            <fieldset className="permissions">
              <legend>{t("agents.permissions")}</legend>
              {(["write", "shell", "external"] as const).map((key) => (
                <label className="check" key={key}>
                  <input
                    type="checkbox"
                    checked={perms[key]}
                    onChange={(event) => setPerms((current) => ({ ...current, [key]: event.target.checked }))}
                  />
                  {t(key === "write" ? "agents.write" : key === "shell" ? "agents.shell" : "agents.external")}
                </label>
              ))}
            </fieldset>
            <div className="form-actions">
              <button type="button" className="button ghost" onClick={() => setShowAdd(false)}>
                {t("common.cancel")}
              </button>
              <button type="submit" className="button primary" disabled={busy || !name.trim()}>
                {busy ? t("common.loading") : t("common.create")}
              </button>
            </div>
          </form>
        </Modal>
      )}
    </section>
  );
}

export function DevicesView({ t, notify, onError }: CommonProps) {
  const [devices, setDevices] = useState<Device[]>([]);
  const [pairings, setPairings] = useState<PairingView[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [pairError, setPairError] = useState("");
  const [pairWarnings, setPairWarnings] = useState<string[]>([]);

  const activePairing = pairings.some((pairing) =>
    ["waiting", "requested", "approving"].includes(pairing.status),
  );

  async function load() {
    try {
      const [deviceResult, pairingResult] = await Promise.all([api.devices(), api.pairings()]);
      setDevices(deviceResult.devices);
      setPairings(pairingResult.pairings);
    } catch (err) {
      onError(err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!activePairing) return;
    const timer = window.setInterval(() => void load(), 2000);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activePairing]);

  async function createPairing() {
    setPairError("");
    setPairWarnings([]);
    setCreating(true);
    try {
      const created = await api.createPairing();
      setPairings((current) => [created, ...current.filter((item) => item.id !== created.id)]);
    } catch (err) {
      setPairError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setCreating(false);
    }
  }

  async function approve(pairing: PairingView) {
    setPairError("");
    try {
      const approved = await api.approvePairing(pairing.id);
      setPairWarnings(approved.warnings || []);
      notify(t("devices.pairApproved"));
      await load();
    } catch (err) {
      setPairError(err instanceof Error ? err.message : t("common.error"));
    }
  }

  async function cancel(pairing: PairingView) {
    setPairError("");
    try {
      await api.cancelPairing(pairing.id);
      await load();
    } catch (err) {
      setPairError(err instanceof Error ? err.message : t("common.error"));
    }
  }

  async function revoke(device: Device) {
    if (!window.confirm(t("devices.confirm"))) return;
    try {
      const result = await api.revokeDevice(device.id);
      const warnings = result?.warnings || [];
      notify(warnings.length ? warnings.join(" · ") : t("devices.revoke"));
      await load();
    } catch (err) {
      onError(err);
    }
  }

  function pairingLabel(pairing: PairingView): string {
    switch (pairing.status) {
      case "waiting":
        return t("devices.pairWaiting");
      case "requested":
      case "approving":
        return t("devices.pairRequested");
      case "approved":
        return t("devices.pairApproved");
      case "expired":
        return t("approval.expired");
      case "canceled":
        return t("devices.pairCancel");
      default:
        return pairing.status;
    }
  }

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.devices")}</p>
          <h1>{t("devices.title")}</h1>
          <p className="muted">{t("devices.subtitle")}</p>
        </div>
      </header>

      <div className="pair-panel">
        <div className="pair-panel-head">
          <div>
            <h2>{t("devices.pairTitle")}</h2>
            <p className="muted">{t("devices.pairText")}</p>
          </div>
          <button className="button primary" onClick={() => void createPairing()} disabled={creating}>
            {creating ? t("onboard.linking") : t("devices.pairCreate")}
          </button>
        </div>
        {pairError && <div className="form-error">{pairError}</div>}
        {pairWarnings.map((warning) => (
          <p className="pair-warning" key={warning}>
            {warning}
          </p>
        ))}
        {pairings.length === 0 && <p className="empty-hint">{t("devices.pairEmpty")}</p>}
        {pairings.map((pairing) => (
          <article className={`pair-session status-${pairing.status}`} key={pairing.id}>
            <div className="pair-session-copy">
              <h3>
                {pairingLabel(pairing)}
                {pairing.device_name && <span className="pair-device-name"> · {pairing.device_name}</span>}
              </h3>
              {pairing.code && pairing.status === "waiting" && (
                <div className="pair-code-row">
                  <code className="pair-code">{pairing.code}</code>
                  <CopyButton
                    value={pairing.code}
                    labels={{ copy: t("common.copy"), copied: t("common.copied") }}
                  />
                </div>
              )}
              {pairing.fingerprint && (
                <>
                  <p className="fine-print">{t("devices.pairCompare")}</p>
                  <p className="fingerprint">{pairing.fingerprint}</p>
                </>
              )}
              <p className="fine-print">
                {t("devices.pairExpires")}: {new Date(pairing.expires_at).toLocaleString()}
              </p>
            </div>
            <div className="pair-session-actions">
              {pairing.status === "requested" && (
                <button className="button primary" onClick={() => void approve(pairing)}>
                  {t("devices.pairApprove")}
                </button>
              )}
              {["waiting", "requested", "approved"].includes(pairing.status) && (
                <button className="button ghost" onClick={() => void cancel(pairing)}>
                  {t("devices.pairCancel")}
                </button>
              )}
            </div>
          </article>
        ))}
      </div>

      {loading && <Spinner label={t("common.loading")} />}
      <div className="device-table">
        {devices.map((device) => (
          <article className={`device-row ${device.revoked_at ? "revoked" : ""}`} key={device.id}>
            <div>
              <h3>
                {device.name} {device.current && <span className="chip accent">{t("devices.current")}</span>}
                {device.revoked_at && <span className="chip danger">{t("agents.revoked")}</span>}
              </h3>
              <p className="muted">
                {device.role} · {device.id.slice(0, 8)}…{device.id.slice(-4)}
              </p>
              <p className="fingerprint">{device.fingerprint}</p>
              {device.last_seen_at && (
                <p className="fine-print">
                  {t("devices.lastSeen")}: {new Date(device.last_seen_at).toLocaleString()}
                </p>
              )}
            </div>
            {!device.current && !device.revoked_at && (
              <button className="button ghost danger" onClick={() => void revoke(device)}>
                {t("devices.revoke")}
              </button>
            )}
          </article>
        ))}
        {!loading && devices.length === 0 && <p className="empty-hint">{t("devices.none")}</p>}
      </div>
    </section>
  );
}

export function AuditView({ t, rooms, onError }: CommonProps) {
  const [roomID, setRoomID] = useState(rooms[0]?.id ?? "");
  const [bundle, setBundle] = useState<AuditResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!roomID && rooms[0]) setRoomID(rooms[0].id);
  }, [rooms, roomID]);

  async function load() {
    if (!roomID) return;
    setLoading(true);
    setResult("");
    try {
      setBundle(await api.audit(roomID));
    } catch (err) {
      onError(err);
    } finally {
      setLoading(false);
    }
  }

  async function download() {
    if (!bundle) return;
    const blob = new Blob([JSON.stringify(bundle.audit, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `toget-audit-${bundle.audit.room.name.replace(/[^\w.-]+/g, "-")}.json`;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
  }

  async function verifyFile(file: File) {
    try {
      const text = await file.text();
      const parsed = JSON.parse(text);
      const verified = await api.verifyAudit(parsed);
      setResult(t("audit.resultOk", { events: verified.events, head: verified.head.slice(0, 16) + "…" }));
    } catch (err) {
      setResult(t("audit.resultFail", { message: err instanceof Error ? err.message : String(err) }));
    }
  }

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.audit")}</p>
          <h1>{t("audit.title")}</h1>
          <p className="muted">{t("audit.subtitle")}</p>
        </div>
      </header>
      <div className="audit-controls">
        <select value={roomID} onChange={(event) => setRoomID(event.target.value)}>
          {rooms.map((room) => (
            <option value={room.id} key={room.id}>
              {room.name}
            </option>
          ))}
        </select>
        <button className="button primary" onClick={() => void load()} disabled={!roomID || loading}>
          {loading ? t("common.loading") : t("audit.load")}
        </button>
      </div>
      {bundle && (
        <div className="audit-card">
          <div className="audit-summary">
            <Badge status={bundle.verified ? "granted" : "rejected"} />
            <span>{bundle.verified ? t("audit.verified") : t("audit.failed")}</span>
          </div>
          <dl className="facts">
            <div>
              <dt>{t("audit.events")}</dt>
              <dd>{bundle.audit.events.length}</dd>
            </div>
            <div>
              <dt>{t("audit.members")}</dt>
              <dd>{bundle.audit.members.length}</dd>
            </div>
            <div>
              <dt>{t("audit.head")}</dt>
              <dd className="mono">{bundle.audit.head.slice(0, 24)}…</dd>
            </div>
          </dl>
          <button className="button ghost" onClick={() => void download()}>
            {t("audit.download")}
          </button>
        </div>
      )}
      <div className="verify-box">
        <h3>{t("audit.verifyFile")}</h3>
        <input
          ref={fileRef}
          type="file"
          accept="application/json,.json"
          onChange={(event) => {
            const file = event.target.files?.[0];
            if (file) void verifyFile(file);
          }}
        />
        {result && <p className="verify-result">{result}</p>}
      </div>
    </section>
  );
}

export function SettingsView({
  t,
  lang,
  setLang,
  lock,
  account,
  status,
  onError,
}: CommonProps & {
  account: { id: string; name: string; server_id: string };
  status: { listen: string; version: { app: string; protocol: number; schema: number } } | null;
  lang: Lang;
  setLang: (lang: Lang) => void;
  lock: () => Promise<void>;
}) {
  const [node, setNode] = useState<{ enabled: boolean; info?: NodeInfo } | null>(null);
  const [discovery, setDiscovery] = useState<{ enabled: boolean; peers: DiscoveredPeer[] } | null>(null);
  const [syncing, setSyncing] = useState(false);

  useEffect(() => {
    api.node().then(setNode).catch(() => setNode({ enabled: false }));
    api.nodeDiscover().then(setDiscovery).catch(() => setDiscovery({ enabled: false, peers: [] }));
  }, []);

  async function sync() {
    setSyncing(true);
    try {
      await api.syncAllRooms();
    } catch (err) {
      onError(err);
    } finally {
      setSyncing(false);
    }
  }

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.settings")}</p>
          <h1>{t("settings.title")}</h1>
          <p className="muted">{t("settings.subtitle")}</p>
        </div>
      </header>
      <div className="settings-grid">
        <article className="panel">
          <h2>{t("settings.account")}</h2>
          <p className="mono big">{formatAccountID(account.id)}</p>
          <p className="muted">{account.name}</p>
          <p className="fine-print mono">{account.server_id}</p>
          <button className="button ghost" onClick={() => void lock()}>
            {t("settings.lockNow")}
          </button>
        </article>
        <article className="panel">
          <h2>{t("settings.node")}</h2>
          <dl className="facts vertical">
            <div>
              <dt>{t("settings.version")}</dt>
              <dd>{status?.version.app ?? "—"}</dd>
            </div>
            <div>
              <dt>{t("settings.protocol")}</dt>
              <dd>v{status?.version.protocol ?? "—"}</dd>
            </div>
            <div>
              <dt>{t("settings.schema")}</dt>
              <dd>v{status?.version.schema ?? "—"}</dd>
            </div>
            <div>
              <dt>{t("settings.listen")}</dt>
              <dd className="mono">{status?.listen ?? "—"}</dd>
            </div>
          </dl>
        </article>
        <article className="panel wide">
          <h2>{t("settings.lanTitle")}</h2>
          {node === null ? (
            <Spinner label={t("common.loading")} />
          ) : !node.enabled || !node.info ? (
            <p className="muted">{t("settings.lanDisabled")}</p>
          ) : (
            <>
              <dl className="facts vertical">
                <div>
                  <dt>{t("settings.lanURL")}</dt>
                  <dd className="mono">{node.info.url}</dd>
                </div>
                {node.info.direct_url && (
                  <div>
                    <dt>{t("settings.directURL")}</dt>
                    <dd className="mono">{node.info.direct_url}</dd>
                  </div>
                )}
                {node.info.relay_url && (
                  <div>
                    <dt>{t("settings.relay")}</dt>
                    <dd className="mono">
                      {node.info.relay_url}{" "}
                      <span className="muted">
                        ({node.info.relay_connected
                          ? t("settings.relayConnected")
                          : t("settings.relayDisconnected")})
                      </span>
                    </dd>
                  </div>
                )}
                {node.info.directory_url && (
                  <div>
                    <dt>{t("settings.directory")}</dt>
                    <dd className="mono">{node.info.directory_url}</dd>
                  </div>
                )}
                <div>
                  <dt>{t("settings.mdns")}</dt>
                  <dd>
                    {node.info.mdns ? t("settings.mdnsOn") : t("settings.mdnsOff")}
                    {node.info.mdns && node.info.mdns_peers ? (
                      <span className="muted">
                        {" "}· {node.info.mdns_peers} {t("settings.mdnsPeers")}
                      </span>
                    ) : null}
                  </dd>
                </div>
                <div>
                  <dt>{t("settings.lanFingerprint")}</dt>
                  <dd className="mono fingerprint">{node.info.tls_fingerprint}</dd>
                </div>
              </dl>
              <button className="button ghost" onClick={() => void sync()} disabled={syncing}>
                {syncing ? t("settings.syncing") : t("settings.syncNow")}
              </button>
            </>
          )}
        </article>
        <article className="panel wide">
          <h2>{t("settings.nearby")}</h2>
          {discovery === null ? (
            <Spinner label={t("common.loading")} />
          ) : !discovery.enabled ? (
            <p className="muted">{t("settings.mdnsOff")}</p>
          ) : discovery.peers.length === 0 ? (
            <p className="muted">{t("settings.noNearby")}</p>
          ) : (
            <>
              <p className="fine-print muted">{t("settings.nearbyHint")}</p>
              <dl className="facts vertical">
                {discovery.peers.map((peer) => (
                  <div key={`${peer.server_id}-${peer.port}`}>
                    <dt className="mono">{peer.server_code}</dt>
                    <dd>
                      {peer.addresses.map((address) => `${address}:${peer.port}`).join(" · ")}
                      <span className="muted"> · {shortID(peer.server_id)}</span>
                    </dd>
                  </div>
                ))}
              </dl>
            </>
          )}
        </article>
        <article className="panel">
          <h2>{t("settings.language")}</h2>
          <div className="segmented">
            <button className={lang === "en" ? "active" : ""} onClick={() => setLang("en")}>
              English
            </button>
            <button className={lang === "zh" ? "active" : ""} onClick={() => setLang("zh")}>
              中文
            </button>
          </div>
        </article>
        <article className="panel wide">
          <h2>{t("settings.backupTitle")}</h2>
          <p className="muted">{t("settings.backupText")}</p>
        </article>
        <article className="panel wide">
          <h2>{t("settings.securityTitle")}</h2>
          <p className="muted">{t("settings.securityText")}</p>
        </article>
      </div>
    </section>
  );
}

export function SearchView({ t, onError, openRoom }: CommonProps) {
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<SearchHit[] | null>(null);
  const [busy, setBusy] = useState(false);

  async function search(event: FormEvent) {
    event.preventDefault();
    if (!query.trim()) return;
    setBusy(true);
    try {
      const result = await api.search(query.trim());
      setHits(result.hits);
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("nav.search")}</p>
          <h1>{t("search.title")}</h1>
        </div>
      </header>
      <form className="search-bar" onSubmit={search}>
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search.placeholder")} />
        <button className="button primary" type="submit" disabled={busy || !query.trim()}>
          {t("search.button")}
        </button>
      </form>
      {hits && hits.length === 0 && <p className="empty-hint">{t("search.none")}</p>}
      <div className="search-results">
        {(hits ?? []).map((hit) => (
          <article className="search-hit" key={hit.event_id}>
            <div className="search-hit-top">
              <span className="chip">{hit.room_name}</span>
              <time>{formatTime(hit.created_at)}</time>
              <button className="link" onClick={() => openRoom(hit.room_id)}>
                {t("search.open")} →
              </button>
            </div>
            <p>{hit.text || hit.type}</p>
          </article>
        ))}
      </div>
    </section>
  );
}
