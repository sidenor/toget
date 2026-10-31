import { FormEvent, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { formatAccountID, formatBytes, formatTime, shortID } from "./format";
import type { Lang, Translate } from "./i18n";
import { QRCodeImage, QRScanButton } from "./QR";
import { decodeQRImage, qrScanErrorMessage } from "./qr";
import type {
  Agent,
  AgentSession,
  Approval,
  AuditResponse,
  BackupArchive,
  BackupManifest,
  CardView,
  Device,
  DiscoveredPeer,
  NodeInfo,
  NotificationStatus,
  NotificationSubscription,
  PairingView,
  Policy,
  PolicyDecision,
  PolicyRule,
  RecoveryStatus,
  RestoreStatus,
  RestoreUpload,
  Room,
  SearchHit,
  UpdateStatusView,
} from "./types";
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
  const [rotatingKey, setRotatingKey] = useState(false);
  const [syncingAccount, setSyncingAccount] = useState(false);
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

  async function syncAccount() {
    setPairError("");
    setPairWarnings([]);
    setSyncingAccount(true);
    try {
      const result = await api.syncAccount();
      const summary = t("devices.syncResult", { synced: result.synced, peers: result.peers });
      const warnings = result.warnings || [];
      notify(warnings.length ? `${summary} ${warnings.join(" · ")}` : summary);
      await load();
    } catch (err) {
      setPairError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setSyncingAccount(false);
    }
  }

  async function rotateKey() {
    if (!window.confirm(t("devices.rotateConfirm"))) return;
    setPairError("");
    setPairWarnings([]);
    setRotatingKey(true);
    try {
      const result = await api.rotateAccountKey();
      notify(t("devices.rotateResult", { generation: result.generation, devices: result.devices }));
      setPairWarnings(result.warnings || []);
      await load();
    } catch (err) {
      setPairError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setRotatingKey(false);
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
          <div className="pair-panel-actions">
            <button className="button ghost" onClick={() => void syncAccount()} disabled={syncingAccount}>
              {syncingAccount ? t("devices.syncing") : t("devices.syncNow")}
            </button>
            <button className="button ghost" onClick={() => void rotateKey()} disabled={rotatingKey || creating}>
              {rotatingKey ? t("devices.rotating") : t("devices.rotateKey")}
            </button>
            <button className="button primary" onClick={() => void createPairing()} disabled={creating || rotatingKey}>
              {creating ? t("onboard.linking") : t("devices.pairCreate")}
            </button>
          </div>
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
                <>
                  <div className="pair-code-row">
                    <code className="pair-code">{pairing.code}</code>
                    <CopyButton
                      value={pairing.code}
                      labels={{ copy: t("common.copy"), copied: t("common.copied") }}
                    />
                  </div>
                  <div className="pair-qr">
                    <QRCodeImage
                      value={pairing.code}
                      alt={t("devices.pairQRAlt")}
                      size={200}
                      errorLabel={t("common.qrError")}
                    />
                    <p className="fine-print">{t("devices.pairQRHint")}</p>
                  </div>
                </>
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


export type UpdateControls = {
  update: UpdateStatusView | null;
  updateBusy: string;
  onCheckUpdate: () => void;
  onInstallUpdate: () => void;
  onDismissUpdate: () => void;
  onRestartUpdate: () => void;
  onRollbackUpdate: () => void;
  onClearUpdateDismissal: () => void;
};

function updateLevelLabel(t: Translate, level: string | undefined): string {
  switch (level) {
    case "security":
      return t("update.level.security");
    case "forced":
      return t("update.level.forced");
    default:
      return t("update.level.recommended");
  }
}

function updateReasonLabel(t: Translate, reason: string): string {
  const key = `update.reason.${reason}`;
  const label = t(key);
  return label === key ? reason : label;
}

function updateModeLabel(t: Translate, mode: string): string {
  const key = `update.mode.${mode}`;
  const label = t(key);
  return label === key ? mode : label;
}

function updateChangelog(changelog: string | undefined): string {
  const text = (changelog ?? "").trim();
  if (text.length <= 320) return text;
  return `${text.slice(0, 317).trimEnd()}…`;
}

export function UpdateBanner({
  t,
  update,
  busy,
  onInstall,
  onDismiss,
  onRestart,
  onDetails,
}: {
  t: Translate;
  update: UpdateStatusView | null;
  busy: boolean;
  onInstall: () => void;
  onDismiss: () => void;
  onRestart: () => void;
  onDetails: () => void;
}) {
  if (!update || !update.enabled) return null;
  const result = update.result?.available ? update.result : null;
  const level = result?.manifest?.level ?? "recommended";

  if (update.staged?.needs_restart) {
    const release = update.staged.release;
    return (
      <aside className="update-banner ready" role="status">
        <div className="update-banner-copy">
          <span className="update-kicker">{t("update.restartNeeded")}</span>
          <strong>{t("update.readyText", { release })}</strong>
          {update.restart_configured && update.restart_supported ? (
            <p className="update-changelog">{t("update.staged", { release })}</p>
          ) : (
            <p className="update-changelog">{t("update.manualRestart")}</p>
          )}
        </div>
        <div className="update-banner-actions">
          {update.restart_configured && update.restart_supported && (
            <button className="button primary" onClick={onRestart} disabled={busy}>
              {t("update.restartNow")}
            </button>
          )}
          <button className="button ghost" onClick={onDetails} disabled={busy}>
            {t("update.details")}
          </button>
        </div>
      </aside>
    );
  }

  if (!result || !result.manifest) return null;
  const forced = level === "forced";
  if (update.dismissed && !forced) return null;

  const release = result.manifest.release;
  const title = forced
    ? update.dismissed
      ? t("update.forcedPostponed", { release })
      : t("update.forcedTitle", { release })
    : level === "security"
      ? t("update.securityTitle", { release })
      : t("update.availableTitle", { release });
  const changelog = updateChangelog(result.manifest.changelog);
  const tone = forced ? "forced" : level === "security" ? "security" : "recommended";

  return (
    <aside className={`update-banner ${tone}`} role="status">
      <div className="update-banner-copy">
        <span className="update-kicker">
          {updateLevelLabel(t, level)} · {t("update.current")} {update.current.release || update.current.app}
        </span>
        <strong>{title}</strong>
        {changelog && <p className="update-changelog">{changelog}</p>}
        {result.warning && <p className="update-warning">{result.warning}</p>}
        {update.dismissed && update.dismissed_until && (
          <p className="update-changelog">
            {t("update.dismissed", { date: new Date(update.dismissed_until).toLocaleString() })}
          </p>
        )}
      </div>
      <div className="update-banner-actions">
        <button className="button primary" onClick={onInstall} disabled={busy}>
          {forced ? t("update.forcedNow") : t("update.now")}
        </button>
        {!forced && (
          <button className="button ghost" onClick={onDismiss} disabled={busy}>
            {t("update.later")}
          </button>
        )}
        {forced && !update.dismissed && (
          <button className="button ghost" onClick={onDismiss} disabled={busy}>
            {t("update.forcedLater")}
          </button>
        )}
        <button className="button ghost" onClick={onDetails} disabled={busy}>
          {t("update.details")}
        </button>
      </div>
    </aside>
  );
}

export function UpdateSettingsPanel({
  t,
  update,
  updateBusy,
  onCheckUpdate,
  onInstallUpdate,
  onDismissUpdate,
  onRestartUpdate,
  onRollbackUpdate,
  onClearUpdateDismissal,
}: UpdateControls & { t: Translate }) {
  const busy = updateBusy !== "";
  const result = update?.result;
  const staged = update?.staged;
  const pending = update?.status?.state.pending;
  const previous = update?.status?.state.previous;
  const canRollback = Boolean(previous || pending);

  return (
    <article className="panel wide">
      <h2>{t("update.settingsTitle")}</h2>
      <p className="muted">{t("update.settingsText")}</p>
      {!update ? (
        <p className="muted">{t("update.unavailable")}</p>
      ) : !update.enabled ? (
        <>
          <p className="form-error">{update.config_error || t("update.notConfigured")}</p>
          <p className="fine-print">{t("update.configHint")}</p>
        </>
      ) : (
        <>
          <dl className="facts vertical">
            <div>
              <dt>{t("update.current")}</dt>
              <dd>
                {update.current.release || update.current.app}
                <span className="muted"> · protocol {update.current.protocol} · schema {update.current.schema}</span>
              </dd>
            </div>
            <div>
              <dt>{t("update.latest")}</dt>
              <dd>
                {result?.manifest ? `${result.manifest.release} (${updateLevelLabel(t, result.manifest.level)})` : "—"}
                {result && !result.available && (
                  <span className="muted"> · {updateReasonLabel(t, result.reason)}</span>
                )}
              </dd>
            </div>
            <div>
              <dt>{t("update.channel")}</dt>
              <dd>
                {update.channel} · {t("update.mode")}: {updateModeLabel(t, update.mode)} · {update.check_interval}
              </dd>
            </div>
            <div>
              <dt>{t("update.lastCheck")}</dt>
              <dd>{update.last_check ? new Date(update.last_check).toLocaleString() : t("update.never")}</dd>
            </div>
            <div>
              <dt>{t("update.restart")}</dt>
              <dd>
                {update.restart_configured ? t("update.configured") : t("update.notConfiguredRestart")}
                {update.restart_configured && !update.restart_supported && (
                  <span className="muted"> · {t("update.manualRestart")}</span>
                )}
              </dd>
            </div>
          </dl>

          {update.last_error && <div className="form-error">{update.last_error}</div>}
          {result?.warning && <p className="fine-print">{result.warning}</p>}

          {staged?.needs_restart && (
            <div className="update-ready-box">
              <strong>{t("update.readyText", { release: staged.release })}</strong>
              {update.restart_configured && update.restart_supported ? (
                <button className="button primary" onClick={onRestartUpdate} disabled={busy}>
                  {updateBusy === "restart" ? t("update.restarting") : t("update.restartNow")}
                </button>
              ) : (
                <p className="fine-print">{t("update.manualRestart")}</p>
              )}
            </div>
          )}

          {!staged?.needs_restart && result?.available && result.manifest && (
            <>
              <p className="muted">
                {result.manifest.changelog?.trim() || t("update.reason.available")}
              </p>
              <div className="button-row">
                <button className="button primary" onClick={onInstallUpdate} disabled={busy}>
                  {updateBusy === "install" ? t("update.checking") : t("update.now")}
                </button>
                <button className="button ghost" onClick={onDismissUpdate} disabled={busy}>
                  {t("update.later")}
                </button>
              </div>
            </>
          )}

          {update.dismissed && (
            <div className="button-row">
              <p className="muted">
                {update.dismissed_until
                  ? t("update.dismissed", { date: new Date(update.dismissed_until).toLocaleString() })
                  : t("update.later")}
              </p>
              <button className="button ghost" onClick={onClearUpdateDismissal} disabled={busy}>
                {t("update.showAgain")}
              </button>
            </div>
          )}

          <div className="button-row">
            <button className="button ghost" onClick={onCheckUpdate} disabled={busy}>
              {updateBusy === "check" ? t("update.checking") : t("update.checkNow")}
            </button>
            {canRollback && (
              <button className="button ghost danger" onClick={onRollbackUpdate} disabled={busy}>
                {pending ? t("update.discardPending") : t("update.rollback")}
              </button>
            )}
          </div>
        </>
      )}
    </article>
  );
}


type RuleDraft = {
  key: string;
  id: string;
  effect: "allow" | "deny";
  kind: string;
  tool: string;
  target: string;
  recipient: string;
  amountMin: string;
  amountMax: string;
  currency: string;
  weekdays: number[];
  start: string;
  end: string;
  timezone: string;
  note: string;
};

function draftFromRule(rule: PolicyRule): RuleDraft {
  return {
    key: rule.id || Math.random().toString(36).slice(2),
    id: rule.id ?? "",
    effect: rule.effect === "deny" ? "deny" : "allow",
    kind: rule.kind ?? "",
    tool: rule.tool ?? "",
    target: rule.target ?? "",
    recipient: rule.recipient ?? "",
    amountMin: rule.amount_min ?? "",
    amountMax: rule.amount_max ?? "",
    currency: rule.currency ?? "",
    weekdays: rule.weekdays ?? [],
    start: rule.start ?? "",
    end: rule.end ?? "",
    timezone: rule.timezone ?? "",
    note: rule.note ?? "",
  };
}

function blankRule(): RuleDraft {
  return {
    key: Math.random().toString(36).slice(2),
    id: "",
    effect: "allow",
    kind: "",
    tool: "",
    target: "",
    recipient: "",
    amountMin: "",
    amountMax: "",
    currency: "",
    weekdays: [],
    start: "",
    end: "",
    timezone: "",
    note: "",
  };
}

function cleanRule(rule: RuleDraft): PolicyRule {
  const value = (text: string) => text.trim() || undefined;
  return {
    id: rule.id,
    effect: rule.effect,
    kind: value(rule.kind),
    tool: value(rule.tool),
    target: value(rule.target),
    recipient: value(rule.recipient),
    amount_min: value(rule.amountMin),
    amount_max: value(rule.amountMax),
    currency: value(rule.currency),
    weekdays: rule.weekdays.length > 0 ? [...rule.weekdays].sort((a, b) => a - b) : undefined,
    start: value(rule.start),
    end: value(rule.end),
    timezone: value(rule.timezone),
    note: value(rule.note),
  };
}

const weekdayKeys = [
  "policies.weekdayMon",
  "policies.weekdayTue",
  "policies.weekdayWed",
  "policies.weekdayThu",
  "policies.weekdayFri",
  "policies.weekdaySat",
  "policies.weekdaySun",
];

export function PoliciesView({ t, rooms, notify, onError }: CommonProps) {
  const [roomID, setRoomID] = useState(rooms[0]?.id ?? "");
  const [agentID, setAgentID] = useState("");
  const [agents, setAgents] = useState<Agent[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [drafts, setDrafts] = useState<RuleDraft[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [preview, setPreview] = useState<PolicyDecision | null>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [action, setAction] = useState({ kind: "tool.call", tool: "", target: "", recipient: "", amount: "", currency: "" });

  useEffect(() => {
    if (!rooms.some((room) => room.id === roomID)) setRoomID(rooms[0]?.id ?? "");
  }, [rooms, roomID]);

  useEffect(() => {
    if (!roomID) {
      setAgents([]);
      setPolicies([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    Promise.all([api.roomAgents(roomID), api.policies(roomID)])
      .then(([agentResult, policyResult]) => {
        if (cancelled) return;
        setAgents(agentResult.agents);
        setPolicies(policyResult.policies);
        setAgentID((current) => (current && agentResult.agents.some((agent) => agent.id === current) ? current : ""));
      })
      .catch((err) => {
        if (!cancelled) onError(err);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [roomID, onError]);

  useEffect(() => {
    const found = policies.find((policy) => (policy.agent_id ?? "") === agentID);
    setDrafts((found?.rules ?? []).map(draftFromRule));
    setPreview(null);
  }, [policies, agentID]);

  function updateRule(index: number, patch: Partial<RuleDraft>) {
    setDrafts((current) => current.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  }

  function toggleWeekday(index: number, day: number) {
    setDrafts((current) =>
      current.map((rule, i) => {
        if (i !== index) return rule;
        const has = rule.weekdays.includes(day);
        return { ...rule, weekdays: has ? rule.weekdays.filter((value) => value !== day) : [...rule.weekdays, day] };
      }),
    );
  }

  async function save() {
    if (!roomID) return;
    setSaving(true);
    try {
      const result = await api.savePolicy(roomID, agentID, drafts.map(cleanRule));
      setPolicies((current) => [...current.filter((policy) => (policy.agent_id ?? "") !== agentID), result.policy]);
      notify(t("policies.saved"));
    } catch (err) {
      onError(err);
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!roomID || !window.confirm(t("policies.deleteConfirm"))) return;
    try {
      await api.deletePolicy(roomID, agentID);
      setPolicies((current) => current.filter((policy) => (policy.agent_id ?? "") !== agentID));
      setDrafts([]);
      notify(t("policies.deleted"));
    } catch (err) {
      onError(err);
    }
  }

  async function evaluate() {
    if (!roomID) return;
    setPreviewBusy(true);
    try {
      const result = await api.evaluatePolicy(roomID, agentID, {
        kind: action.kind.trim() || "tool.call",
        tool: action.tool.trim() || undefined,
        target: action.target.trim() || undefined,
        recipient: action.recipient.trim() || undefined,
        amount: action.amount.trim() || undefined,
        currency: action.currency.trim() || undefined,
      });
      setPreview(result);
    } catch (err) {
      onError(err);
    } finally {
      setPreviewBusy(false);
    }
  }

  const hasSaved = policies.some((policy) => (policy.agent_id ?? "") === agentID);

  return (
    <div className="view policies-view">
      <header className="view-header">
        <div>
          <p className="eyebrow">{t("policies.eyebrow")}</p>
          <h1>{t("policies.title")}</h1>
          <p className="muted">{t("policies.subtitle")}</p>
        </div>
        <div className="policy-actions">
          {hasSaved && (
            <button className="button ghost" onClick={() => void remove()}>
              {t("policies.delete")}
            </button>
          )}
          <button className="button primary" onClick={() => void save()} disabled={saving || !roomID}>
            {saving ? t("policies.saving") : t("policies.save")}
          </button>
        </div>
      </header>

      <div className="policy-toolbar">
        <label>
          <span className="field-label">{t("policies.room")}</span>
          <select value={roomID} onChange={(event) => setRoomID(event.target.value)}>
            {rooms.map((room) => (
              <option key={room.id} value={room.id}>
                {room.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          <span className="field-label">{t("policies.target")}</span>
          <select value={agentID} onChange={(event) => setAgentID(event.target.value)}>
            <option value="">{t("policies.roomDefault")}</option>
            {agents.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name} · {shortID(agent.id)}
              </option>
            ))}
          </select>
        </label>
      </div>
      <p className="muted policy-semantics">{t("policies.semantics")}</p>

      {loading ? (
        <Spinner label={t("common.loading")} />
      ) : (
        <>
          <section className="policy-rules">
            {drafts.map((rule, index) => (
              <article className={`policy-rule effect-${rule.effect}`} key={rule.key}>
                <header className="policy-rule-head">
                  <span className="policy-rule-index">
                    {t("policies.rule")} {index + 1}
                  </span>
                  <label className="policy-effect">
                    <span className="field-label">{t("policies.effect")}</span>
                    <select
                      value={rule.effect}
                      onChange={(event) => updateRule(index, { effect: event.target.value as "allow" | "deny" })}
                    >
                      <option value="allow">{t("policies.allow")}</option>
                      <option value="deny">{t("policies.deny")}</option>
                    </select>
                  </label>
                  <input
                    className="policy-note"
                    value={rule.note}
                    maxLength={200}
                    placeholder={t("policies.note")}
                    onChange={(event) => updateRule(index, { note: event.target.value })}
                  />
                  <button
                    className="mini"
                    aria-label={t("policies.remove")}
                    title={t("policies.remove")}
                    onClick={() => setDrafts((current) => current.filter((_, i) => i !== index))}
                  >
                    ×
                  </button>
                </header>
                <div className="policy-grid">
                  <label>
                    <span>{t("policies.kind")}</span>
                    <input value={rule.kind} maxLength={256} placeholder="tool.call" onChange={(event) => updateRule(index, { kind: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.tool")}</span>
                    <input value={rule.tool} maxLength={256} placeholder="mail.*" onChange={(event) => updateRule(index, { tool: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.targetField")}</span>
                    <input value={rule.target} maxLength={256} onChange={(event) => updateRule(index, { target: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.recipient")}</span>
                    <input value={rule.recipient} maxLength={256} placeholder="*@example.com" onChange={(event) => updateRule(index, { recipient: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.amountMin")}</span>
                    <input value={rule.amountMin} maxLength={64} inputMode="decimal" onChange={(event) => updateRule(index, { amountMin: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.amountMax")}</span>
                    <input value={rule.amountMax} maxLength={64} inputMode="decimal" onChange={(event) => updateRule(index, { amountMax: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.currency")}</span>
                    <input value={rule.currency} maxLength={16} placeholder="USD" onChange={(event) => updateRule(index, { currency: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.timezone")}</span>
                    <input value={rule.timezone} maxLength={64} placeholder="Local" onChange={(event) => updateRule(index, { timezone: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.start")}</span>
                    <input type="time" value={rule.start} onChange={(event) => updateRule(index, { start: event.target.value })} />
                  </label>
                  <label>
                    <span>{t("policies.end")}</span>
                    <input type="time" value={rule.end} onChange={(event) => updateRule(index, { end: event.target.value })} />
                  </label>
                </div>
                <div className="weekday-row" role="group" aria-label={t("policies.weekdays")}>
                  <span className="field-label">{t("policies.weekdays")}</span>
                  {weekdayKeys.map((key, position) => {
                    const day = (position + 1) % 7;
                    const active = rule.weekdays.includes(day);
                    return (
                      <button
                        type="button"
                        key={key}
                        className={`weekday ${active ? "active" : ""}`}
                        aria-pressed={active}
                        onClick={() => toggleWeekday(index, day)}
                      >
                        {t(key)}
                      </button>
                    );
                  })}
                </div>
              </article>
            ))}
            {drafts.length === 0 && (
              <p className="empty-hint">{agentID ? t("policies.emptyAgent") : t("policies.none")}</p>
            )}
            <button className="button ghost policy-add" onClick={() => setDrafts((current) => [...current, blankRule()])}>
              + {t("policies.addRule")}
            </button>
          </section>

          <section className="policy-preview">
            <div>
              <h2>{t("policies.preview")}</h2>
              <p className="muted">{t("policies.previewHint")}</p>
            </div>
            <div className="policy-grid">
              <label>
                <span>{t("policies.kind")}</span>
                <input value={action.kind} onChange={(event) => setAction({ ...action, kind: event.target.value })} />
              </label>
              <label>
                <span>{t("policies.tool")}</span>
                <input value={action.tool} onChange={(event) => setAction({ ...action, tool: event.target.value })} />
              </label>
              <label>
                <span>{t("policies.targetField")}</span>
                <input value={action.target} onChange={(event) => setAction({ ...action, target: event.target.value })} />
              </label>
              <label>
                <span>{t("policies.recipient")}</span>
                <input value={action.recipient} onChange={(event) => setAction({ ...action, recipient: event.target.value })} />
              </label>
              <label>
                <span>{t("policies.amountMax")}</span>
                <input value={action.amount} inputMode="decimal" onChange={(event) => setAction({ ...action, amount: event.target.value })} />
              </label>
              <label>
                <span>{t("policies.currency")}</span>
                <input value={action.currency} onChange={(event) => setAction({ ...action, currency: event.target.value })} />
              </label>
            </div>
            <div className="policy-preview-actions">
              <button className="button" onClick={() => void evaluate()} disabled={previewBusy || !roomID}>
                {previewBusy ? t("common.loading") : t("policies.runPreview")}
              </button>
              {preview && (
                <span className={`policy-verdict effect-${preview.effect}`}>
                  {preview.effect === "allow"
                    ? t("policies.effectAllow")
                    : preview.effect === "deny"
                      ? t("policies.effectDeny")
                      : t("policies.effectApproval")}
                  {preview.rule_id ? ` · ${preview.rule_id}` : ""}
                </span>
              )}
            </div>
            {preview && <p className="muted">{preview.reason}</p>}
          </section>
        </>
      )}
    </div>
  );
}


type ManifestSummaryProps = {
  t: Translate;
  lang: Lang;
  manifest: BackupManifest;
  size: number;
};

function ManifestSummary({ t, lang, manifest, size }: ManifestSummaryProps) {
  if (!manifest.format) {
    return <p className="fine-print muted">{t("settings.backupInvalid")}</p>;
  }
  const created = manifest.created_at
    ? new Intl.DateTimeFormat(lang, { dateStyle: "medium", timeStyle: "short" }).format(new Date(manifest.created_at))
    : "—";
  return (
    <p className="fine-print muted">
      {created} · {formatBytes(size)} · {t("settings.backupMeta", {
        count: manifest.files.length,
        schema: manifest.schema_version,
        app: manifest.app_version || "?",
      })}
    </p>
  );
}

type BackupPanelProps = Pick<CommonProps, "t" | "lang" | "notify" | "onError">;

type RestoreConfirm = {
  kind: "backup" | "upload";
  key: string;
  manifest: BackupManifest;
  size: number;
};

function BackupPanel({ t, lang, notify, onError }: BackupPanelProps) {
  const [backups, setBackups] = useState<BackupArchive[] | null>(null);
  const [restore, setRestore] = useState<RestoreStatus | null>(null);
  const [upload, setUpload] = useState<RestoreUpload | null>(null);
  const [confirm, setConfirm] = useState<RestoreConfirm | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const [restarting, setRestarting] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  async function load() {
    try {
      const [list, status] = await Promise.all([api.backups(), api.restoreStatus()]);
      setBackups(list.backups);
      setRestore(status);
    } catch (err) {
      onError(err);
    }
  }

  useEffect(() => {
    void load();
    // The panel loads once when Settings opens; actions refresh explicitly.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function createBackup() {
    setBusy("create");
    setMessage("");
    try {
      const created = await api.createBackup();
      setMessage(t("settings.backupCreated", { name: created.name }));
      await load();
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  async function chooseFile(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setBusy("upload");
    setMessage("");
    setConfirm(null);
    try {
      const inspected = await api.uploadRestore(file);
      setUpload(inspected);
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  async function discardUpload() {
    if (!upload) return;
    setBusy("upload");
    try {
      await api.discardRestoreUpload(upload.id);
      setUpload(null);
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  async function stageRestore() {
    if (!confirm) return;
    setBusy("stage");
    try {
      if (confirm.kind === "upload") {
        await api.stageRestore({ upload_id: confirm.key });
      } else {
        await api.stageRestore({ backup_name: confirm.key });
      }
      setConfirm(null);
      setUpload(null);
      setMessage(t("settings.backupStaged"));
      await load();
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  async function cancelRestore() {
    setBusy("cancel");
    try {
      await api.cancelRestore();
      setMessage(t("settings.backupCancelled"));
      await load();
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  async function restartNow() {
    setRestarting(true);
    setMessage("");
    try {
      await api.restartRestore();
      setMessage(t("settings.backupRestarting"));
    } catch (err) {
      setRestarting(false);
      onError(err);
    }
  }

  async function download(name: string) {
    try {
      await api.download(api.backupDownloadURL(name), name);
    } catch (err) {
      onError(err);
    }
  }

  async function remove(name: string) {
    setBusy("delete");
    try {
      await api.deleteBackup(name);
      setDeleting(null);
      notify(t("settings.backupDeleted"));
      await load();
    } catch (err) {
      onError(err);
    } finally {
      setBusy("");
    }
  }

  const staged = restore?.staged ?? null;

  return (
    <article className="panel wide backup-panel">
      <div className="backup-heading">
        <div>
          <h2>{t("settings.backupTitle")}</h2>
          <p className="muted">{t("settings.backupText")}</p>
        </div>
        <button className="button primary" disabled={busy !== ""} onClick={() => void createBackup()}>
          {busy === "create" ? t("settings.backupCreating") : t("settings.backupCreate")}
        </button>
      </div>

      {message && <p className="form-success">{message}</p>}

      {staged && (
        <div className="backup-callout danger">
          <div>
            <strong>{t("settings.backupStagedTitle")}</strong>
            <p className="muted">{t("settings.backupStagedText")}</p>
            <ManifestSummary t={t} lang={lang} manifest={staged.manifest} size={staged.size} />
          </div>
          <div className="button-row">
            {restore?.restart_configured && restore.restart_supported ? (
              <button className="button danger" disabled={restarting} onClick={() => void restartNow()}>
                {restarting ? t("settings.backupRestartingShort") : t("settings.backupRestart")}
              </button>
            ) : (
              <span className="fine-print muted">{t("settings.backupRestartManual")}</span>
            )}
            <button className="button ghost" disabled={busy !== ""} onClick={() => void cancelRestore()}>
              {t("settings.backupCancelStaged")}
            </button>
          </div>
        </div>
      )}

      {upload && (
        <div className="backup-callout">
          <div>
            <strong>{t("settings.backupUploadReady")}</strong>
            <p className="muted">{t("settings.backupUploadReadyText")}</p>
            <ManifestSummary t={t} lang={lang} manifest={upload.manifest} size={upload.size} />
          </div>
          <div className="button-row">
            <button
              className="button primary"
              disabled={busy !== ""}
              onClick={() => setConfirm({ kind: "upload", key: upload.id, manifest: upload.manifest, size: upload.size })}
            >
              {t("settings.backupReviewUpload")}
            </button>
            <button className="button ghost" disabled={busy !== ""} onClick={() => void discardUpload()}>
              {t("common.cancel")}
            </button>
          </div>
        </div>
      )}

      {confirm && (
        <div className="backup-callout danger">
          <div>
            <strong>{t("settings.backupConfirmTitle")}</strong>
            <p className="muted">{t("settings.backupConfirmText")}</p>
            <ManifestSummary t={t} lang={lang} manifest={confirm.manifest} size={confirm.size} />
          </div>
          <div className="button-row">
            <button className="button danger" disabled={busy !== ""} onClick={() => void stageRestore()}>
              {busy === "stage" ? t("settings.backupStaging") : t("settings.backupStage")}
            </button>
            <button className="button ghost" disabled={busy !== ""} onClick={() => setConfirm(null)}>
              {t("common.cancel")}
            </button>
          </div>
        </div>
      )}

      <div className="backup-list">
        {backups === null ? (
          <Spinner />
        ) : backups.length === 0 ? (
          <p className="muted">{t("settings.backupEmpty")}</p>
        ) : (
          backups.map((item) => (
            <div className="backup-row" key={item.name}>
              <div className="backup-row-main">
                <strong className="mono">{item.name}</strong>
                <ManifestSummary t={t} lang={lang} manifest={item.manifest} size={item.size} />
              </div>
              <div className="backup-row-actions">
                <button className="button ghost small" onClick={() => void download(item.name)}>
                  {t("settings.backupDownload")}
                </button>
                <button
                  className="button ghost small"
                  disabled={!item.manifest.format || busy !== ""}
                  onClick={() => setConfirm({ kind: "backup", key: item.name, manifest: item.manifest, size: item.size })}
                >
                  {t("settings.backupRestore")}
                </button>
                {deleting === item.name ? (
                  <>
                    <button className="button danger small" disabled={busy !== ""} onClick={() => void remove(item.name)}>
                      {t("settings.backupDeleteConfirm")}
                    </button>
                    <button className="button ghost small" onClick={() => setDeleting(null)}>
                      {t("common.cancel")}
                    </button>
                  </>
                ) : (
                  <button className="button ghost danger small" onClick={() => setDeleting(item.name)}>
                    {t("settings.backupDelete")}
                  </button>
                )}
              </div>
            </div>
          ))
        )}
      </div>

      <div className="backup-upload">
        <input
          ref={inputRef}
          type="file"
          accept=".toget-backup,application/zip"
          hidden
          onChange={(event) => void chooseFile(event)}
        />
        <button className="button" disabled={busy !== ""} onClick={() => inputRef.current?.click()}>
          {busy === "upload" ? t("settings.backupVerifying") : t("settings.backupUpload")}
        </button>
        <span className="fine-print muted">{t("settings.backupUploadHint")}</span>
      </div>
      <p className="fine-print muted">{t("settings.backupSafety")}</p>
    </article>
  );
}


const PUSH_ID_KEY = "toget.pushSubscription";

function pushSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    "serviceWorker" in navigator &&
    "PushManager" in window &&
    "Notification" in window
  );
}

function applicationServerKey(value: string): Uint8Array {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (value.length % 4)) % 4);
  const raw = atob(padded);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i);
  return bytes;
}

function browserLabel(): string {
  const ua = navigator.userAgent;
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const os = /iPhone|iPad|iPod/.test(ua)
    ? "iOS"
    : /Android/.test(ua)
      ? "Android"
      : /Mac OS X/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return os ? `${browser} · ${os}` : browser;
}

function NotificationsPanel({
  t,
  lang,
  notify,
}: {
  t: Translate;
  lang: Lang;
  notify: (message: string) => void;
}) {
  const supported = pushSupported();
  const [status, setStatus] = useState<NotificationStatus | null>(null);
  const [localID, setLocalID] = useState(() => localStorage.getItem(PUSH_ID_KEY) ?? "");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [loadError, setLoadError] = useState("");

  async function load() {
    try {
      const next = await api.notificationStatus();
      setStatus(next);
      setLoadError("");
      if (localID && !next.subscriptions.some((sub) => sub.id === localID)) {
        localStorage.removeItem(PUSH_ID_KEY);
        setLocalID("");
      }
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : t("settings.notificationsLoadFailed"));
    }
  }

  useEffect(() => {
    void load();
    // localStorage is per browser; status changes are reloaded explicitly.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lang]);

  const localSub = status?.subscriptions.find((sub) => sub.id === localID);
  const muted = Boolean(status?.muted_until && new Date(status.muted_until).getTime() > Date.now());

  async function enable() {
    if (!status || !supported) return;
    setBusy("enable");
    setError("");
    setMessage("");
    try {
      const permission =
        Notification.permission === "granted" ? "granted" : await Notification.requestPermission();
      if (permission !== "granted") {
        setError(t(permission === "denied" ? "settings.notificationsPermissionDenied" : "settings.notificationsTestFailed"));
        return;
      }
      let registration = await navigator.serviceWorker.getRegistration();
      if (!registration) registration = await navigator.serviceWorker.register("/sw.js");
      await navigator.serviceWorker.ready;
      const existing = await registration.pushManager.getSubscription();
      const subscription =
        existing ??
        (await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: applicationServerKey(status.public_key) as BufferSource,
        }));
      const raw = subscription.toJSON();
      if (!raw.endpoint || !raw.keys?.p256dh || !raw.keys?.auth) {
        throw new Error(t("settings.notificationsNoWorker"));
      }
      const saved = await api.subscribeNotifications({
        endpoint: raw.endpoint,
        p256dh: raw.keys.p256dh,
        auth: raw.keys.auth,
        language: lang,
        user_agent: navigator.userAgent,
        label: browserLabel(),
      });
      localStorage.setItem(PUSH_ID_KEY, saved.id);
      setLocalID(saved.id);
      await load();
      notify(t("settings.notificationsEnabledToast"));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsTestFailed"));
    } finally {
      setBusy("");
    }
  }

  async function disable() {
    setBusy("disable");
    setError("");
    setMessage("");
    try {
      const registration = await navigator.serviceWorker.getRegistration();
      const existing = await registration?.pushManager.getSubscription();
      if (localID) await api.deleteNotificationSubscription(localID);
      if (existing) await existing.unsubscribe();
      localStorage.removeItem(PUSH_ID_KEY);
      setLocalID("");
      await load();
      notify(t("settings.notificationsDisabledToast"));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsTestFailed"));
    } finally {
      setBusy("");
    }
  }

  async function remove(sub: NotificationSubscription) {
    if (!window.confirm(t("settings.notificationsDeleteConfirm"))) return;
    setBusy(sub.id);
    setError("");
    setMessage("");
    try {
      await api.deleteNotificationSubscription(sub.id);
      if (sub.id === localID) {
        const registration = await navigator.serviceWorker.getRegistration();
        const existing = await registration?.pushManager.getSubscription();
        if (existing) await existing.unsubscribe();
        localStorage.removeItem(PUSH_ID_KEY);
        setLocalID("");
      }
      await load();
      notify(t("settings.notificationsDeleted"));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsTestFailed"));
    } finally {
      setBusy("");
    }
  }

  async function update(sub: NotificationSubscription, patch: {
    enabled?: boolean;
    message_events?: boolean;
    approval_events?: boolean;
  }) {
    setBusy(sub.id);
    setError("");
    try {
      const updated = await api.updateNotificationSubscription(sub.id, patch);
      setStatus((current) =>
        current
          ? { ...current, subscriptions: current.subscriptions.map((item) => (item.id === updated.id ? updated : item)) }
          : current,
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsLoadFailed"));
    } finally {
      setBusy("");
    }
  }

  async function test(sub: NotificationSubscription) {
    setBusy(sub.id);
    setError("");
    setMessage("");
    try {
      await api.testNotificationSubscription(sub.id);
      setMessage(t("settings.notificationsTestSent"));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsTestFailed"));
    } finally {
      setBusy("");
    }
  }

  async function mute(hours: number) {
    setBusy("mute");
    setError("");
    try {
      setStatus(await api.muteNotifications(hours));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.notificationsLoadFailed"));
    } finally {
      setBusy("");
    }
  }

  return (
    <article className="panel wide notification-panel">
      <div className="backup-heading">
        <div>
          <h2>{t("settings.notificationsTitle")}</h2>
          <p className="muted">{t("settings.notificationsText")}</p>
        </div>
        {supported && status && (
          <button
            className="button primary"
            onClick={() => void (localSub ? disable() : enable())}
            disabled={busy !== ""}
          >
            {busy === "enable"
              ? t("settings.notificationsEnabling")
              : busy === "disable"
                ? t("settings.notificationsDisabling")
                : localSub
                ? t("settings.notificationsDisable")
                : t("settings.notificationsEnable")}
          </button>
        )}
      </div>
      {!supported ? (
        <div className="backup-callout">
          <p>{t("settings.notificationsUnsupported")}</p>
        </div>
      ) : (
        <>
          <p className="fine-print notification-privacy">{t("settings.notificationsPrivacy")}</p>
          {Notification.permission === "denied" && (
            <div className="form-error">{t("settings.notificationsPermissionDenied")}</div>
          )}
          {error && <div className="form-error">{error}</div>}
          {message && <p className="form-success">{message}</p>}
          {loadError && <div className="form-error">{loadError}</div>}
          {status === null ? (
            !loadError && <Spinner label={t("common.loading")} />
          ) : (
            <>
              <div className="notification-mute">
                <div>
                  <strong>{t("settings.notificationsMuteTitle")}</strong>
                  <p className="fine-print muted">{t("settings.notificationsMuteText")}</p>
                  {muted && (
                    <p className="fine-print muted">
                      {t("settings.notificationsMutedUntil", {
                        time: new Date(status.muted_until!).toLocaleString(lang),
                      })}
                    </p>
                  )}
                </div>
                <div className="segmented">
                  <button className={muted ? "" : "active"} onClick={() => void mute(0)} disabled={busy !== ""}>
                    {t("settings.notificationsMuteOff")}
                  </button>
                  {[1, 8, 24].map((hours) => (
                    <button key={hours} onClick={() => void mute(hours)} disabled={busy !== ""}>
                      {t("settings.notificationsMuteHour", { hours })}
                    </button>
                  ))}
                </div>
              </div>
              <h3>{t("settings.notificationsDevices")}</h3>
              {status.subscriptions.length === 0 ? (
                <p className="muted">{t("settings.notificationsEmpty")}</p>
              ) : (
                <div className="backup-list">
                  {status.subscriptions.map((sub) => (
                    <div className="notification-row" key={sub.id}>
                      <div className="notification-row-main">
                        <div className="notification-row-title">
                          <strong>{sub.label || sub.endpoint_host || sub.id}</strong>
                          {sub.id === localID && (
                            <span className="chip">{t("settings.notificationsThisBrowser")}</span>
                          )}
                        </div>
                        <p className="fine-print muted">
                          {t("settings.notificationsEndpoint")}: {sub.endpoint_host || "—"} · {sub.language.toUpperCase()}
                        </p>
                        <p className="fine-print muted">
                          {sub.last_success_at
                            ? `${t("settings.notificationsLastSuccess")}: ${new Date(sub.last_success_at).toLocaleString(lang)}`
                            : t("settings.notificationsNever")}
                          {sub.failure_count > 0 &&
                            ` · ${t("settings.notificationsFailures", { count: sub.failure_count })}`}
                          {sub.muted_until && new Date(sub.muted_until) > new Date() &&
                            ` · ${t("settings.notificationsMutedUntil", {
                              time: new Date(sub.muted_until).toLocaleString(lang),
                            })}`}
                        </p>
                        <div className="notification-toggles">
                          <label>
                            <input
                              type="checkbox"
                              checked={sub.enabled}
                              disabled={busy !== ""}
                              onChange={(event) => void update(sub, { enabled: event.target.checked })}
                            />
                            {t("settings.notificationsEnabled")}
                          </label>
                          <label>
                            <input
                              type="checkbox"
                              checked={sub.message_events}
                              disabled={busy !== ""}
                              onChange={(event) => void update(sub, { message_events: event.target.checked })}
                            />
                            {t("settings.notificationsMessages")}
                          </label>
                          <label>
                            <input
                              type="checkbox"
                              checked={sub.approval_events}
                              disabled={busy !== ""}
                              onChange={(event) => void update(sub, { approval_events: event.target.checked })}
                            />
                            {t("settings.notificationsApprovals")}
                          </label>
                        </div>
                      </div>
                      <div className="backup-row-actions">
                        <button className="button ghost small" onClick={() => void test(sub)} disabled={busy !== ""}>
                          {busy === sub.id ? t("settings.notificationsTesting") : t("settings.notificationsTest")}
                        </button>
                        <button className="button ghost small danger" onClick={() => void remove(sub)} disabled={busy !== ""}>
                          {t("common.delete")}
                        </button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </>
      )}
    </article>
  );
}

export function SettingsView({
  t,
  lang,
  setLang,
  lock,
  account,
  status,
  notify,
  onError,
  update,
  updateBusy,
  onCheckUpdate,
  onInstallUpdate,
  onDismissUpdate,
  onRestartUpdate,
  onRollbackUpdate,
  onClearUpdateDismissal,
  installed,
  canInstall,
  onInstallApp,
}: CommonProps & UpdateControls & {
  account: { id: string; name: string; server_id: string };
  status: { listen: string; version: { app: string; protocol: number; schema: number } } | null;
  lang: Lang;
  setLang: (lang: Lang) => void;
  lock: () => Promise<void>;
  installed: boolean;
  canInstall: boolean;
  onInstallApp: () => void;
}) {
  const [node, setNode] = useState<{ enabled: boolean; info?: NodeInfo } | null>(null);
  const [discovery, setDiscovery] = useState<{ enabled: boolean; peers: DiscoveredPeer[] } | null>(null);
  const [ownCard, setOwnCard] = useState<CardView | null>(null);
  const [cards, setCards] = useState<CardView[]>([]);
  const [cardInput, setCardInput] = useState("");
  const [cardInputLabel, setCardInputLabel] = useState("");
  const [cardBusy, setCardBusy] = useState(false);
  const [cardScanning, setCardScanning] = useState(false);
  const [cardError, setCardError] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [relayMode, setRelayMode] = useState<"off" | "auto" | "pinned">("off");
  const [relayAddress, setRelayAddress] = useState("");
  const [relayBusy, setRelayBusy] = useState(false);
  const [relayError, setRelayError] = useState("");
  const [recovery, setRecovery] = useState<RecoveryStatus | null>(null);
  const [recoveryBusy, setRecoveryBusy] = useState(false);
  const [recoveryError, setRecoveryError] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordBusy, setPasswordBusy] = useState(false);
  const [passwordError, setPasswordError] = useState("");
  const [localReset, setLocalReset] = useState(false);

  function iosHint() {
    return /iphone|ipad|ipod/i.test(navigator.userAgent);
  }

  useEffect(() => {
    api.node().then(setNode).catch(() => setNode({ enabled: false }));
    api.nodeDiscover().then(setDiscovery).catch(() => setDiscovery({ enabled: false, peers: [] }));
    api.nodeCard().then((result) => setOwnCard(result.card)).catch(() => setOwnCard(null));
    api.nodeCards().then((result) => setCards(result.cards)).catch(() => setCards([]));
    api.recovery().then(setRecovery).catch(() => setRecovery(null));
  }, []);

  useEffect(() => {
    if (!node?.info) return;
    setRelayMode(node.info.relay_mode);
    setRelayAddress(node.info.relay_mode === "pinned" ? node.info.relay_setting : "");
  }, [node?.info?.relay_mode, node?.info?.relay_setting]);

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

  async function saveRelay() {
    if (!node?.info) return;
    if (relayMode === "pinned" && !relayAddress.trim()) {
      setRelayError(t("settings.relayAddressRequired"));
      return;
    }
    if (relayMode === "auto" && !node.info.directory_url) {
      setRelayError(t("settings.relayAutoNeedsDirectory"));
      return;
    }
    setRelayBusy(true);
    setRelayError("");
    try {
      const result = await api.setNodeRelay(relayMode === "pinned" ? relayAddress.trim() : relayMode);
      setNode(result);
      notify(t("settings.relaySaved"));
    } catch (err) {
      setRelayError(err instanceof Error ? err.message : String(err));
    } finally {
      setRelayBusy(false);
    }
  }

  async function addCard() {
    if (!cardInput.trim()) return;
    setCardBusy(true);
    setCardError("");
    try {
      const result = await api.addNodeCard(cardInput.trim(), cardInputLabel.trim());
      setCards((current) => [result.card, ...current.filter((item) => item.server_id !== result.card.server_id)]);
      setCardInput("");
      setCardInputLabel("");
      notify(t("settings.cardAddSuccess"));
    } catch (err) {
      setCardError(err instanceof Error ? err.message : String(err));
    } finally {
      setCardBusy(false);
    }
  }

  async function removeCard(id: string) {
    setCardBusy(true);
    setCardError("");
    try {
      await api.removeNodeCard(id);
      setCards((current) => current.filter((item) => item.server_id !== id));
      notify(t("settings.cardRemoveSuccess"));
    } catch (err) {
      setCardError(err instanceof Error ? err.message : String(err));
    } finally {
      setCardBusy(false);
    }
  }

  async function submitPassword(event: FormEvent) {
    event.preventDefault();
    if (newPassword.length < 8) {
      setPasswordError(t("settings.passwordShort"));
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordError(t("settings.passwordMismatch"));
      return;
    }
    if (localReset && !window.confirm(t("settings.localResetConfirm"))) return;
    setPasswordBusy(true);
    setPasswordError("");
    try {
      if (localReset) {
        await api.resetPasswordLocal(newPassword);
        notify(t("settings.localResetDone"));
      } else {
        await api.changePassword(currentPassword, newPassword);
        notify(t("settings.passwordChanged"));
      }
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setLocalReset(false);
    } catch (err) {
      setPasswordError(err instanceof Error ? err.message : String(err));
    } finally {
      setPasswordBusy(false);
    }
  }

  async function createRecovery() {
    if (!window.confirm(t("settings.recoveryCreateConfirm"))) return;
    setRecoveryBusy(true);
    setRecoveryError("");
    try {
      const created = await api.createRecovery();
      const blob = new Blob([created.content], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = created.filename;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      setRecovery(await api.recovery());
      notify(t("settings.recoveryCreated"));
    } catch (err) {
      setRecoveryError(err instanceof Error ? err.message : String(err));
    } finally {
      setRecoveryBusy(false);
    }
  }

  async function removeRecovery() {
    if (!window.confirm(t("settings.recoveryRemoveConfirm"))) return;
    setRecoveryBusy(true);
    setRecoveryError("");
    try {
      await api.removeRecovery();
      setRecovery(await api.recovery());
      notify(t("settings.recoveryRemoved"));
    } catch (err) {
      setRecoveryError(err instanceof Error ? err.message : String(err));
    } finally {
      setRecoveryBusy(false);
    }
  }

  async function scanCard(file: File) {
    setCardScanning(true);
    setCardError("");
    try {
      const payload = await decodeQRImage(file);
      if (!payload.startsWith("toget-card:v1:")) throw new Error(t("settings.cardScanInvalid"));
      setCardInput(payload);
      notify(t("settings.cardScanned"));
    } catch (err) {
      setCardError(qrScanErrorMessage(t, err, "settings.cardScan"));
    } finally {
      setCardScanning(false);
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
        <article className="panel wide">
          <h2>{t("settings.passwordTitle")}</h2>
          <p className="muted">{t("settings.passwordText")}</p>
          <form className="settings-form" onSubmit={submitPassword}>
            {localReset ? (
              <>
                <p className="muted">{t("settings.localResetText")}</p>
                <button className="link" type="button" onClick={() => setLocalReset(false)}>
                  {t("settings.localResetCancel")}
                </button>
              </>
            ) : (
              <label>
                {t("settings.currentPassword")}
                <input
                  type="password"
                  value={currentPassword}
                  onChange={(event) => setCurrentPassword(event.target.value)}
                  autoComplete="current-password"
                  required
                />
              </label>
            )}
            <label>
              {t("settings.newPassword")}
              <input
                type="password"
                value={newPassword}
                onChange={(event) => setNewPassword(event.target.value)}
                autoComplete="new-password"
                required
              />
            </label>
            <label>
              {t("settings.confirmPassword")}
              <input
                type="password"
                value={confirmPassword}
                onChange={(event) => setConfirmPassword(event.target.value)}
                autoComplete="new-password"
                required
              />
            </label>
            {passwordError && <div className="form-error">{passwordError}</div>}
            <button
              className="button primary"
              type="submit"
              disabled={passwordBusy || (!localReset && !currentPassword) || !newPassword}
            >
              {passwordBusy
                ? t("settings.recoveryBusy")
                : localReset
                  ? t("settings.localReset")
                  : t("settings.changePassword")}
            </button>
            {!localReset && (
              <button className="link" type="button" onClick={() => setLocalReset(true)}>
                {t("settings.forgotPassword")}
              </button>
            )}
          </form>
          <hr className="settings-divider" />
          <h3>{t("settings.recoveryTitle")}</h3>
          <p className="muted">{t("settings.recoveryText")}</p>
          {recoveryError && <div className="form-error">{recoveryError}</div>}
          {recovery === null ? (
            <Spinner label={t("common.loading")} />
          ) : recovery.enabled ? (
            <>
              <p className="muted">
                {t("settings.recoveryEnabled", {
                  date: recovery.created_at ? new Date(recovery.created_at).toLocaleString() : "—",
                })}
              </p>
              <div className="button-row">
                <button className="button ghost" onClick={() => void createRecovery()} disabled={recoveryBusy}>
                  {recoveryBusy ? t("settings.recoveryBusy") : t("settings.recoveryReplace")}
                </button>
                <button className="button ghost danger" onClick={() => void removeRecovery()} disabled={recoveryBusy}>
                  {t("settings.recoveryRemove")}
                </button>
              </div>
            </>
          ) : (
            <>
              <p className="form-error">{t("settings.recoveryDisabled")}</p>
              <button className="button primary" onClick={() => void createRecovery()} disabled={recoveryBusy}>
                {recoveryBusy ? t("settings.recoveryBusy") : t("settings.recoveryCreate")}
              </button>
            </>
          )}
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
        <UpdateSettingsPanel
          t={t}
          update={update}
          updateBusy={updateBusy}
          onCheckUpdate={onCheckUpdate}
          onInstallUpdate={onInstallUpdate}
          onDismissUpdate={onDismissUpdate}
          onRestartUpdate={onRestartUpdate}
          onRollbackUpdate={onRollbackUpdate}
          onClearUpdateDismissal={onClearUpdateDismissal}
        />
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
                <div>
                  <dt>{t("settings.relay")}</dt>
                  <dd>
                    {node.info.relay_mode === "off"
                      ? t("settings.relayModeOff")
                      : node.info.relay_mode === "auto"
                        ? t("settings.relayModeAuto")
                        : t("settings.relayModePinned")}
                    {node.info.relay_mode !== "off" && (
                      <span className="muted">
                        {" "}· {node.info.relay_connected
                          ? t("settings.relayConnected")
                          : t("settings.relayDisconnected")}
                      </span>
                    )}
                  </dd>
                </div>
                {node.info.relay_connected && (
                  <div>
                    <dt>{t("settings.relayTransport")}</dt>
                    <dd>{node.info.relay_tunnel ? t("settings.relayTunnelWS") : t("settings.relayTunnelPoll")}</dd>
                  </div>
                )}
                {node.info.relay_url && (
                  <div>
                    <dt>{t("settings.relayRoute")}</dt>
                    <dd className="mono">{node.info.relay_url}</dd>
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
                  <dt>{t("settings.quic")}</dt>
                  <dd>{node.info.quic ? t("settings.quicOn") : t("settings.quicOff")}</dd>
                </div>
                {node.info.nat_method && (
                  <div>
                    <dt>{t("settings.nat")}</dt>
                    <dd>
                      {t("settings.natMethod", { method: node.info.nat_method })}
                      {node.info.external_endpoint ? (
                        <span className="muted mono"> · {node.info.external_endpoint}</span>
                      ) : (
                        <span className="muted"> · {t("settings.natNoPublic")}</span>
                      )}
                    </dd>
                  </div>
                )}
                <div>
                  <dt>{t("settings.lanFingerprint")}</dt>
                  <dd className="mono fingerprint">{node.info.tls_fingerprint}</dd>
                </div>
              </dl>
              <hr className="settings-divider" />
              <h3>{t("settings.relayTitle")}</h3>
              <p className="muted">{t("settings.relayText")}</p>
              <div className="settings-form">
                <label>
                  <span className="field-label">{t("settings.relayMode")}</span>
                  <select
                    value={relayMode}
                    onChange={(event) => {
                      setRelayMode(event.target.value as "off" | "auto" | "pinned");
                      setRelayError("");
                    }}
                  >
                    <option value="off">{t("settings.relayModeOff")}</option>
                    <option value="auto" disabled={!node.info.directory_url}>
                      {t("settings.relayModeAuto")}
                    </option>
                    <option value="pinned">{t("settings.relayModePinned")}</option>
                  </select>
                </label>
                {relayMode === "auto" && !node.info.directory_url && (
                  <p className="form-error">{t("settings.relayAutoNeedsDirectory")}</p>
                )}
                {relayMode === "pinned" && (
                  <label>
                    <span className="field-label">{t("settings.relayAddress")}</span>
                    <input
                      className="mono"
                      value={relayAddress}
                      onChange={(event) => {
                        setRelayAddress(event.target.value);
                        setRelayError("");
                      }}
                      placeholder={t("settings.relayAddressPlaceholder")}
                      spellCheck={false}
                      autoCapitalize="off"
                      autoCorrect="off"
                    />
                  </label>
                )}
                {relayError && <p className="form-error">{relayError}</p>}
                <div className="button-row">
                  <button className="button primary" onClick={() => void saveRelay()} disabled={relayBusy}>
                    {relayBusy ? t("settings.relaySaving") : t("settings.relaySave")}
                  </button>
                </div>
              </div>
              <hr className="settings-divider" />
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
        <article className="panel wide">
          <h2>{t("settings.cardTitle")}</h2>
          <p className="muted">{t("settings.cardText")}</p>
          {cardError && <div className="form-error">{cardError}</div>}
          {ownCard ? (
            <div className="card-layout">
              <div className="card-details">
                <dl className="facts vertical">
                  <div>
                    <dt>{t("settings.cardLabel")}</dt>
                    <dd>{ownCard.label || t("common.none")}</dd>
                  </div>
                  <div>
                    <dt>{t("settings.server")}</dt>
                    <dd className="mono">{ownCard.server_code}</dd>
                  </div>
                  <div>
                    <dt>{t("settings.account")}</dt>
                    <dd className="mono">{formatAccountID(ownCard.account_id)}</dd>
                  </div>
                  <div>
                    <dt>{t("settings.cardIdentityExpires")}</dt>
                    <dd>{new Date(ownCard.identity_expires_at).toLocaleDateString()}</dd>
                  </div>
                  <div>
                    <dt>{t("settings.cardNodeExpires")}</dt>
                    <dd>{new Date(ownCard.node_expires_at).toLocaleDateString()}</dd>
                  </div>
                </dl>
                <details className="card-text">
                  <summary>{t("settings.cardCopy")}</summary>
                  <textarea readOnly value={ownCard.card} rows={5} onFocus={(event) => event.currentTarget.select()} />
                  <CopyButton
                    value={ownCard.card}
                    labels={{ copy: t("common.copy"), copied: t("common.copied") }}
                  />
                </details>
              </div>
              <div className="card-qr">
                <img src="/api/v1/card/qr?scale=8" alt={t("settings.cardQRHint")} width={248} height={248} />
                <p className="fine-print">{t("settings.cardQRHint")}</p>
                <a className="button ghost small" href="/api/v1/card/qr?scale=10" download={`toget-card-${ownCard.server_code}.png`}>
                  {t("settings.cardDownload")}
                </a>
              </div>
            </div>
          ) : (
            <p className="muted">{t("settings.cardUnavailable")}</p>
          )}
        </article>
        <article className="panel wide">
          <h2>{t("settings.cardsTitle")}</h2>
          <p className="muted">{t("settings.cardsText")}</p>
          <div className="card-import">
            <div className="card-scan-row">
              <QRScanButton
                label={t("settings.cardScan")}
                busyLabel={t("settings.cardScanning")}
                scanning={cardScanning}
                onFile={(file) => void scanCard(file)}
              />
              <span className="fine-print">{t("settings.cardScanHint")}</span>
            </div>
            <textarea
              value={cardInput}
              onChange={(event) => setCardInput(event.target.value)}
              placeholder={t("settings.cardPlaceholder")}
              rows={3}
            />
            <div className="card-import-actions">
              <input
                value={cardInputLabel}
                onChange={(event) => setCardInputLabel(event.target.value)}
                placeholder={t("settings.cardAddLabel")}
              />
              <button className="button primary" onClick={() => void addCard()} disabled={cardBusy || !cardInput.trim()}>
                {t("settings.cardAdd")}
              </button>
            </div>
          </div>
          {cards.length === 0 ? (
            <p className="muted">{t("settings.cardsEmpty")}</p>
          ) : (
            <div className="card-list">
              {cards.map((saved) => (
                <article className="card-row" key={saved.server_id}>
                  <div>
                    <h3>{saved.label || saved.server_code}</h3>
                    <p className="muted mono">
                      {saved.server_code} · {shortID(saved.server_id)}
                    </p>
                    <p className="fine-print">
                      {t("settings.cardIdentityExpires")}: {new Date(saved.identity_expires_at).toLocaleDateString()}
                    </p>
                  </div>
                  <button className="button ghost danger" onClick={() => void removeCard(saved.server_id)} disabled={cardBusy}>
                    {t("common.delete")}
                  </button>
                </article>
              ))}
            </div>
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
        <article className="panel">
          <h2>{t("settings.installTitle")}</h2>
          {installed ? (
            <p className="form-success">{t("settings.installDone")}</p>
          ) : (
            <>
              <p className="muted">{t("settings.installText")}</p>
              {canInstall && (
                <button className="button primary" onClick={onInstallApp}>
                  {t("pwa.install")}
                </button>
              )}
              <p className="fine-print">
                {iosHint() ? t("settings.installIOS") : canInstall ? t("settings.installReady") : t("settings.installHint")}
              </p>
            </>
          )}
        </article>
        <BackupPanel t={t} lang={lang} notify={notify} onError={onError} />
        <NotificationsPanel t={t} lang={lang} notify={notify} />
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
