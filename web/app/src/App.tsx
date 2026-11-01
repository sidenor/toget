import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { APIError, api } from "./api";
import { formatAccountID, formatBytes, formatDay, formatTime, shortID } from "./format";
import { Lang, detectLang, localeOf, makeT } from "./i18n";
import { Markdown } from "./Markdown";
import { QRCodeImage, QRScanButton } from "./QR";
import { decodeQRImage, qrScanErrorMessage } from "./qr";
import type {
  Account,
  Agent,
  AgentStream,
  Approval,
  Attachment,
  Member,
  PairJoinView,
  RecoveryCredential,
  Room,
  RoomEvent,
  Status,
  UpdateStatusView,
} from "./types";
import { Badge, CopyButton, Modal, Spinner } from "./ui";
import {
  AgentsView,
  ApprovalsView,
  AuditView,
  CommonProps,
  DevicesView,
  MachineView,
  PoliciesView,
  SearchView,
  SettingsView,
  UpdateBanner,
} from "./views";

type View = "chat" | "approvals" | "agents" | "machine" | "policies" | "devices" | "audit" | "search" | "settings";

const views: View[] = ["chat", "approvals", "agents", "machine", "policies", "devices", "audit", "search", "settings"];

function initialView(): View {
  const requested = new URLSearchParams(window.location.search).get("view");
  return requested && views.includes(requested as View) ? (requested as View) : "chat";
}

function isStandalone(): boolean {
  return window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
}

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
  const [view, setView] = useState<View>(initialView);
  const [booting, setBooting] = useState(true);
  const [installPrompt, setInstallPrompt] = useState<BeforeInstallPromptEvent | null>(null);
  const [installed, setInstalled] = useState(isStandalone);
  const [installDismissed, setInstallDismissed] = useState(() => localStorage.getItem("toget.installDismissed") === "1");
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const [navOpen, setNavOpen] = useState(false);
  const [update, setUpdate] = useState<UpdateStatusView | null>(null);
  const [updateBusy, setUpdateBusy] = useState("");

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

  const refreshUpdate = useCallback(async () => {
    try {
      setUpdate(await api.updateStatus());
    } catch {
      // A node without update configuration still has a healthy local API;
      // leave the last known state instead of showing a persistent error.
    }
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
          void refreshUpdate();
          const session = await api.session();
          if (cancelled) return;
          if (!session.locked) {
            const result = await api.rooms();
            if (cancelled) return;
            setRooms(result.rooms);
            const requestedRoom = new URLSearchParams(window.location.search).get("room");
            const room = result.rooms.find((item) => item.id === requestedRoom);
            setActiveRoomID(room?.id ?? result.rooms[0]?.id ?? null);
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
    if (!status?.account) return;
    void refreshUpdate();
    const timer = window.setInterval(() => void refreshUpdate(), 60_000);
    return () => window.clearInterval(timer);
  }, [status?.account?.id, refreshUpdate]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 2600);
    return () => window.clearTimeout(timer);
  }, [toast]);

  const notify = useCallback((message: string) => setToast(message), []);

  useEffect(() => {
    function onPrompt(event: Event) {
      event.preventDefault();
      setInstallPrompt(event as BeforeInstallPromptEvent);
    }
    function onInstalled() {
      setInstalled(true);
      setInstallPrompt(null);
      notify(t("pwa.installed"));
    }
    window.addEventListener("beforeinstallprompt", onPrompt);
    window.addEventListener("appinstalled", onInstalled);
    return () => {
      window.removeEventListener("beforeinstallprompt", onPrompt);
      window.removeEventListener("appinstalled", onInstalled);
    };
  }, [notify, t]);

  const installApp = useCallback(async () => {
    if (!installPrompt) return;
    await installPrompt.prompt();
    const choice = await installPrompt.userChoice;
    if (choice.outcome === "accepted") {
      setInstallPrompt(null);
    } else {
      notify(t("pwa.dismissed"));
    }
  }, [installPrompt, notify, t]);

  const dismissInstall = useCallback(() => {
    localStorage.setItem("toget.installDismissed", "1");
    setInstallDismissed(true);
  }, []);

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

  async function checkForUpdate() {
    setUpdateBusy("check");
    try {
      const result = await api.checkUpdate(true);
      setUpdate(result.update);
      if (result.result.available && result.result.manifest) {
        notify(t("update.checkFound", { release: result.result.manifest.release }));
      } else {
        notify(t("update.upToDate"));
      }
    } catch (err) {
      handleError(err);
    } finally {
      setUpdateBusy("");
    }
  }

  async function installUpdate() {
    const offered = update?.result;
    if (!update || !offered?.available || !offered.manifest) return;
    const release = offered.manifest.release;
    if (!window.confirm(t("update.installConfirm", { release }))) return;
    setUpdateBusy("install");
    try {
      const applied = await api.applyUpdate(release);
      setUpdate(applied.update);
      if (!applied.result.needs_restart) {
        notify(t("update.staged", { release }));
        await refreshUpdate();
        return;
      }
      if (!applied.update.restart_configured || !applied.update.restart_supported) {
        notify(t("update.stagedManual", { release }));
        return;
      }
      if (window.confirm(t("update.restartConfirm"))) {
        await api.restartUpdate();
        notify(t("update.restarting"));
      } else {
        notify(t("update.staged", { release }));
      }
    } catch (err) {
      handleError(err);
    } finally {
      setUpdateBusy("");
    }
  }

  async function dismissUpdate() {
    const release = update?.result?.manifest?.release ?? update?.staged?.release;
    if (!release) return;
    setUpdateBusy("dismiss");
    try {
      setUpdate(await api.dismissUpdate(release, 24));
      notify(t("update.later"));
    } catch (err) {
      handleError(err);
    } finally {
      setUpdateBusy("");
    }
  }

  async function clearUpdateDismissal() {
    setUpdateBusy("clear");
    try {
      setUpdate(await api.clearUpdateDismissal());
    } catch (err) {
      handleError(err);
    } finally {
      setUpdateBusy("");
    }
  }

  async function restartUpdate() {
    if (!window.confirm(t("update.restartConfirm"))) return;
    await performRestart();
  }

  async function performRestart() {
    setUpdateBusy("restart");
    try {
      await api.restartUpdate();
      notify(t("update.restarting"));
    } catch (err) {
      handleError(err);
      setUpdateBusy("");
    }
  }

  async function rollbackUpdate() {
    if (!window.confirm(t("update.rollbackConfirm"))) return;
    setUpdateBusy("rollback");
    try {
      const result = await api.rollbackUpdate();
      setUpdate(result.update);
      notify(t("update.rolledBack", { release: result.result.release || "—" }));
      if (result.result.needs_restart && result.update.restart_configured && result.update.restart_supported) {
        if (window.confirm(t("update.restartConfirm"))) {
          await performRestart();
          return;
        }
      }
    } catch (err) {
      handleError(err);
    } finally {
      setUpdateBusy("");
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
          <NavButton active={view === "machine"} onClick={() => setView("machine")} glyph="⌂" label={t("nav.machine")} />
          <NavButton active={view === "policies"} onClick={() => setView("policies")} glyph="⚖" label={t("nav.policies")} />
          <NavButton active={view === "devices"} onClick={() => setView("devices")} glyph="▤" label={t("nav.devices")} />
          <NavButton active={view === "audit"} onClick={() => setView("audit")} glyph="⛓" label={t("nav.audit")} />
          <NavButton active={view === "search"} onClick={() => setView("search")} glyph="⌕" label={t("nav.search")} />
          <NavButton active={view === "settings"} onClick={() => setView("settings")} glyph="⚙" label={t("nav.settings")} />
        </nav>

        <div className="sidebar-section-title">
          <span>{t("sidebar.rooms")}</span>
          <span className="count">{rooms.length}</span>
        </div>
        <RoomList rooms={rooms} activeID={view === "chat" ? activeRoomID : null} onSelect={selectRoom} onCreate={async (name, official) => {
          const created = await api.createRoom(name, "", official);
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
        {installPrompt && !installed && !installDismissed && view !== "settings" && (
          <div className="install-banner">
            <div>
              <strong>{t("pwa.bannerTitle")}</strong>
              <span>{t("pwa.bannerText")}</span>
            </div>
            <div className="install-banner-actions">
              <button className="button primary" onClick={() => void installApp()}>
                {t("pwa.install")}
              </button>
              <button className="icon-button" onClick={dismissInstall} aria-label={t("common.close")}>
                ×
              </button>
            </div>
          </div>
        )}
        {view !== "settings" && (
          <UpdateBanner
            t={t}
            update={update}
            busy={updateBusy !== ""}
            onInstall={() => void installUpdate()}
            onDismiss={() => void dismissUpdate()}
            onRestart={() => void restartUpdate()}
            onDetails={() => setView("settings")}
          />
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
        {view === "machine" && <MachineView {...common} onOpenAgents={() => setView("agents")} />}
        {view === "policies" && <PoliciesView {...common} />}
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
            update={update}
            updateBusy={updateBusy}
            onCheckUpdate={() => void checkForUpdate()}
            onInstallUpdate={() => void installUpdate()}
            onDismissUpdate={() => void dismissUpdate()}
            onRestartUpdate={() => void restartUpdate()}
            onRollbackUpdate={() => void rollbackUpdate()}
            onClearUpdateDismissal={() => void clearUpdateDismissal()}
            installed={installed}
            canInstall={installPrompt !== null}
            onInstallApp={() => void installApp()}
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
    case "machine":
      return t("nav.machine");
    case "policies":
      return t("nav.policies");
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
  const [mode, setMode] = useState<"unlock" | "reset">("unlock");
  const [password, setPassword] = useState("");
  const [recoveryFile, setRecoveryFile] = useState<File | null>(null);
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
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

  async function reset(event: FormEvent) {
    event.preventDefault();
    if (!recoveryFile) {
      setError(t("lock.recoveryRequired"));
      return;
    }
    if (newPassword.length < 8) {
      setError(t("lock.passwordShort"));
      return;
    }
    if (newPassword !== confirm) {
      setError(t("lock.mismatch"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const credential = await recoveryFile.text();
      await api.resetPassword(credential, newPassword);
      await onUnlocked();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  if (mode === "reset") {
    return (
      <div className="onboarding">
        <div className="onboarding-card compact">
          <div className="brand-mark">T</div>
          <p className="eyebrow">{account.name}</p>
          <h1>{t("lock.resetTitle")}</h1>
          <p className="muted">{t("lock.resetSubtitle")}</p>
          {error && <div className="form-error">{error}</div>}
          <form onSubmit={reset}>
            <label>
              {t("lock.recoveryFile")}
              <input
                type="file"
                accept=".togetkey,application/json"
                onChange={(event) => setRecoveryFile(event.target.files?.[0] ?? null)}
                required
              />
            </label>
            <label>
              {t("lock.newPassword")}
              <input
                type="password"
                value={newPassword}
                onChange={(event) => setNewPassword(event.target.value)}
                required
              />
            </label>
            <label>
              {t("lock.confirmPassword")}
              <input
                type="password"
                value={confirm}
                onChange={(event) => setConfirm(event.target.value)}
                required
              />
            </label>
            <button className="button primary wide" type="submit" disabled={busy}>
              {busy ? t("lock.resetting") : t("lock.reset")}
            </button>
          </form>
          <button
            className="link"
            type="button"
            onClick={() => {
              setMode("unlock");
              setError("");
            }}
          >
            {t("lock.back")}
          </button>
        </div>
      </div>
    );
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
        <button
          className="link"
          type="button"
          onClick={() => {
            setMode("reset");
            setError("");
          }}
        >
          {t("lock.recoveryLink")}
        </button>
        <p className="fine-print">{t("lock.note")}</p>
      </div>
    </div>
  );
}

function downloadRecoveryCredential(credential: RecoveryCredential) {
  const blob = new Blob([credential.content], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = credential.filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
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
  const [mode, setMode] = useState<"create" | "official" | "link">("create");
  const [officialMode, setOfficialMode] = useState<"create" | "signin">("create");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [deviceName, setDeviceName] = useState("");
  const [hostURL, setHostURL] = useState("");
  const [email, setEmail] = useState("");
  const [accountID, setAccountID] = useState("");
  const [pendingRecovery, setPendingRecovery] = useState<RecoveryCredential | null>(null);
  const [recoverySaved, setRecoverySaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [code, setCode] = useState("");
  const [join, setJoin] = useState<PairJoinView | null>(null);
  const [linking, setLinking] = useState(false);
  const [scanning, setScanning] = useState(false);

  const joinTerminal = !!join && ["ready", "failed", "canceled", "expired"].includes(join.status);

  useEffect(() => {
    if (!join || joinTerminal) return;
    let cancelled = false;
    let finished = false;
    async function poll() {
      try {
        const next = await api.pairJoinStatus(join!.id);
        if (cancelled) return;
        setJoin(next);
        if (next.status === "ready" && !finished) {
          finished = true;
          await onCreated();
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : t("common.error"));
      }
    }
    void poll();
    const timer = window.setInterval(() => void poll(), 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
    // Polling is keyed by the join id; status labels update from each poll.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [join?.id, join?.status]);

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
    if (mode === "official" && !hostURL.trim()) {
      setError(t("onboard.hostRequired"));
      return;
    }
    setBusy(true);
    try {
      const created = await api.createAccount(
        name,
        password,
        deviceName || undefined,
        mode === "official" ? { hostURL: hostURL.trim(), email: email.trim() || undefined } : undefined,
      );
      if (mode === "official") {
        if (!created.recovery) {
          setError(t("onboard.recoveryMissing"));
          return;
        }
        downloadRecoveryCredential(created.recovery);
        setPendingRecovery(created.recovery);
        setRecoverySaved(false);
        return;
      }
      await onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  async function hostedSignIn(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!hostURL.trim() || !accountID.trim()) {
      setError(t("onboard.officialRequired"));
      return;
    }
    if (password.length < 8) {
      setError(t("onboard.passwordShort"));
      return;
    }
    setBusy(true);
    try {
      await api.hostedLogin(hostURL.trim(), accountID.trim(), password, deviceName || undefined);
      await onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  async function link(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!code.trim()) {
      setError(t("onboard.linkCode"));
      return;
    }
    if (password.length < 8) {
      setError(t("onboard.passwordShort"));
      return;
    }
    setLinking(true);
    try {
      const started = await api.startPairJoin(code.trim(), deviceName, password);
      setJoin(started);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("common.error"));
    } finally {
      setLinking(false);
    }
  }

  async function scanPairing(file: File) {
    setError("");
    setScanning(true);
    try {
      const payload = await decodeQRImage(file);
      if (!payload.startsWith("toget-pair:v1:")) throw new Error(t("onboard.linkScanInvalid"));
      setCode(payload);
    } catch (err) {
      setError(qrScanErrorMessage(t, err, "onboard.linkScan"));
    } finally {
      setScanning(false);
    }
  }

  async function resetLink() {
    if (join && !joinTerminal) {
      try {
        await api.cancelPairJoin(join.id);
      } catch {
        // The join may already have finished; the local screen still resets.
      }
    }
    setJoin(null);
    setCode("");
    setError("");
  }

  function switchMode(next: "create" | "official" | "link") {
    if (join && !joinTerminal) return;
    setMode(next);
    setJoin(null);
    setError("");
  }

  function joinStatusLabel(status: string): string {
    switch (status) {
      case "waiting":
      case "awaiting_approval":
        return t("onboard.linkWaiting");
      case "importing":
        return t("onboard.linkImporting");
      case "ready":
        return t("onboard.linkImporting");
      default:
        return t("onboard.linkWaiting");
    }
  }

  if (pendingRecovery) {
    return (
      <div className="onboarding">
        <div className="onboarding-card compact">
          <div className="brand-mark">T</div>
          <p className="eyebrow">{t("onboard.officialRecoveryEyebrow")}</p>
          <h1>{t("onboard.officialRecoveryTitle")}</h1>
          <p className="muted">{t("onboard.officialRecoveryText")}</p>
          <p className="fine-print">{pendingRecovery.filename}</p>
          <button
            className="button ghost wide"
            type="button"
            onClick={() => downloadRecoveryCredential(pendingRecovery)}
          >
            {t("onboard.downloadRecovery")}
          </button>
          <label className="checkbox-row">
            <input
              type="checkbox"
              checked={recoverySaved}
              onChange={(event) => setRecoverySaved(event.target.checked)}
            />
            {t("onboard.recoverySaved")}
          </label>
          <button
            className="button primary wide"
            type="button"
            disabled={!recoverySaved}
            onClick={() => void onCreated()}
          >
            {t("onboard.continue")}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="onboarding">
      <div className="onboarding-card">
        <div className="brand-mark">T</div>
        <p className="eyebrow">{t("onboard.eyebrow")}</p>
        <div className="onboard-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={mode === "create"}
            className={mode === "create" ? "active" : ""}
            onClick={() => switchMode("create")}
          >
            {t("onboard.tabCreate")}
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "official"}
            className={mode === "official" ? "active" : ""}
            onClick={() => switchMode("official")}
          >
            {t("onboard.tabOfficial")}
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "link"}
            className={mode === "link" ? "active" : ""}
            onClick={() => switchMode("link")}
          >
            {t("onboard.tabLink")}
          </button>
        </div>
        <h1>
          {mode === "create"
            ? t("onboard.title")
            : mode === "official"
              ? officialMode === "create"
                ? t("onboard.officialCreateTitle")
                : t("onboard.officialSignInTitle")
              : t("onboard.linkTitle")}
        </h1>
        <p className="muted">
          {mode === "create"
            ? t("onboard.subtitle")
            : mode === "official"
              ? officialMode === "create"
                ? t("onboard.officialCreateSubtitle")
                : t("onboard.officialSignInSubtitle")
              : t("onboard.linkSubtitle")}
        </p>
        {error && <div className="form-error">{error}</div>}
        {mode === "official" ? (
          <div className="official-onboard">
            <div className="onboard-tabs subtle" role="tablist">
              <button
                type="button"
                role="tab"
                aria-selected={officialMode === "create"}
                className={officialMode === "create" ? "active" : ""}
                onClick={() => {
                  setOfficialMode("create");
                  setError("");
                }}
              >
                {t("onboard.officialCreateTab")}
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={officialMode === "signin"}
                className={officialMode === "signin" ? "active" : ""}
                onClick={() => {
                  setOfficialMode("signin");
                  setError("");
                }}
              >
                {t("onboard.officialSignInTab")}
              </button>
            </div>
            {officialMode === "create" ? (
              <form onSubmit={submit}>
                <label>
                  {t("onboard.officialHost")}
                  <input
                    autoFocus
                    type="url"
                    value={hostURL}
                    onChange={(event) => setHostURL(event.target.value)}
                    placeholder="https://host.example.com"
                    required
                  />
                </label>
                <label>
                  {t("onboard.name")}
                  <input value={name} onChange={(event) => setName(event.target.value)} maxLength={64} required />
                </label>
                <label>
                  {t("onboard.officialEmail")}
                  <input
                    type="email"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    placeholder={t("onboard.officialEmailHint")}
                  />
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
                  {busy ? t("onboard.creating") : t("onboard.officialCreate")}
                </button>
                <p className="fine-print">{t("onboard.officialFine")}</p>
              </form>
            ) : (
              <form onSubmit={hostedSignIn}>
                <label>
                  {t("onboard.officialHost")}
                  <input
                    autoFocus
                    type="url"
                    value={hostURL}
                    onChange={(event) => setHostURL(event.target.value)}
                    placeholder="https://host.example.com"
                    required
                  />
                </label>
                <label>
                  {t("onboard.officialAccountID")}
                  <input
                    value={accountID}
                    onChange={(event) => setAccountID(event.target.value.replace(/\D/g, ""))}
                    inputMode="numeric"
                    pattern="[0-9]{14}"
                    maxLength={14}
                    required
                  />
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
                  {t("devices.title")} · {t("devices.current")}
                  <input
                    value={deviceName}
                    onChange={(event) => setDeviceName(event.target.value)}
                    placeholder="MacBook · Chrome"
                    maxLength={64}
                  />
                </label>
                <button className="button primary wide" type="submit" disabled={busy}>
                  {busy ? t("onboard.linking") : t("onboard.officialSignIn")}
                </button>
                <p className="fine-print">{t("onboard.officialSignInHint")}</p>
              </form>
            )}
          </div>
        ) : mode === "create" ? (
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
        ) : join ? (
          <div className="pair-link-progress">
            <p className="pair-status">{joinStatusLabel(join.status)}</p>
            {join.fingerprint && (
              <>
                <p className="fine-print">{t("onboard.linkCompare")}</p>
                <code className="pair-fingerprint">{join.fingerprint}</code>
              </>
            )}
            {join.error && <div className="form-error">{join.error}</div>}
            {joinTerminal && join.status !== "ready" && (
              <button className="button primary wide" type="button" onClick={() => void resetLink()}>
                {t("onboard.link")}
              </button>
            )}
            {!joinTerminal && (
              <button className="button ghost wide" type="button" onClick={() => void resetLink()}>
                {t("common.cancel")}
              </button>
            )}
          </div>
        ) : (
          <form onSubmit={link}>
            <label>
              {t("onboard.linkCode")}
              <textarea
                autoFocus
                className="pair-code-input"
                value={code}
                onChange={(event) => setCode(event.target.value)}
                placeholder={t("onboard.linkCodePlaceholder")}
                rows={4}
                spellCheck={false}
                required
              />
            </label>
            <div className="card-scan-row">
              <QRScanButton
                label={t("onboard.linkScan")}
                busyLabel={t("onboard.linkScanning")}
                scanning={scanning}
                onFile={(file) => void scanPairing(file)}
              />
              <span className="fine-print">{t("onboard.linkScanHint")}</span>
            </div>
            <label>
              {t("onboard.linkPassword")}
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                minLength={8}
                required
              />
            </label>
            <label>
              {t("onboard.linkName")}
              <input
                value={deviceName}
                onChange={(event) => setDeviceName(event.target.value)}
                placeholder="MacBook · Chrome"
                maxLength={64}
              />
            </label>
            <button className="button primary wide" type="submit" disabled={linking}>
              {linking ? t("onboard.linking") : t("onboard.link")}
            </button>
            <p className="fine-print">{t("onboard.linkNote")}</p>
          </form>
        )}
        {mode === "create" && <p className="fine-print">{t("onboard.fine")}</p>}
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
  onCreate: (name: string, official: boolean) => Promise<void>;
  onJoin: (code: string) => Promise<void>;
  onError: (err: unknown) => void;
  t: (key: string) => string;
}) {
  const [mode, setMode] = useState<"create" | "join" | null>(null);
  const [name, setName] = useState("");
  const [official, setOfficial] = useState(false);
  const [busy, setBusy] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [scanError, setScanError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setScanError("");
    try {
      if (mode === "join") {
        await onJoin(name.trim());
      } else {
        await onCreate(name.trim(), official);
      }
      setName("");
      setOfficial(false);
      setMode(null);
    } catch (err) {
      onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function scanInvite(file: File) {
    setScanError("");
    setScanning(true);
    try {
      const payload = await decodeQRImage(file);
      if (!payload.startsWith("toget-invite:v1:")) throw new Error(t("sidebar.scanInvalid"));
      setName(payload);
    } catch (err) {
      setScanError(qrScanErrorMessage(t, err, "sidebar.scan"));
    } finally {
      setScanning(false);
    }
  }

  function closeForm() {
    setMode(null);
    setName("");
    setOfficial(false);
    setScanError("");
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
          {mode === "join" && (
            <div className="card-scan-row">
              <QRScanButton
                label={t("sidebar.scan")}
                busyLabel={t("sidebar.scanning")}
                scanning={scanning}
                onFile={(file) => void scanInvite(file)}
              />
              <span className="fine-print">{t("sidebar.scanHint")}</span>
            </div>
          )}
          {mode === "create" && (
            <label className="official-toggle">
              <input
                type="checkbox"
                checked={official}
                onChange={(event) => setOfficial(event.target.checked)}
              />
              <span>
                <strong>{t("sidebar.officialRoom")}</strong>
                <small>{t("sidebar.officialRoomHint")}</small>
              </span>
            </label>
          )}
          {scanError && <div className="form-error">{scanError}</div>}
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

function AgentStreamCard({
  stream,
  t,
}: {
  stream: AgentStream;
  t: (key: string, vars?: Record<string, string | number>) => string;
}) {
  return (
    <article className="message agent streaming">
      <div className="message-meta">
        <span>
          {stream.agent_name}
          <span className="agent-badge">{t("conversation.agentBadge")}</span>
        </span>
        <span className="stream-indicator">
          <span className="stream-dot" aria-hidden="true" />
          {t("conversation.streaming")}
        </span>
      </div>
      <div className="bubble">
        {stream.text ? (
          <p className="plain-text">
            {stream.text}
            <span className="stream-caret" aria-hidden="true" />
          </p>
        ) : (
          <p className="stream-waiting">{t("conversation.streamWaiting")}</p>
        )}
        {stream.truncated && <p className="fine-print">{t("conversation.streamTruncated")}</p>}
      </div>
    </article>
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
  const [streams, setStreams] = useState<AgentStream[]>([]);
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
  const hadStreams = useRef(false);
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

  const loadStreams = useCallback(async () => {
    if (!room) return;
    const result = await api.agentStreams(room.id);
    const had = hadStreams.current;
    hadStreams.current = result.streams.length > 0;
    setStreams(result.streams);
    // The live card disappears just after the final message is posted. Pull
    // the log once more so the finished reply replaces it immediately.
    if (had && result.streams.length === 0) void loadEvents();
  }, [room, loadEvents]);

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
    hadStreams.current = false;
    setEvents([]);
    setStreams([]);
    setLoading(true);
    let cancelled = false;
    (async () => {
      try {
        await loadEvents();
        await loadStreams();
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
    const streamTimer = window.setInterval(() => {
      void loadStreams().catch(() => undefined);
    }, 1000);
    const contextTimer = window.setInterval(() => {
      void loadContext().catch(() => undefined);
    }, 8000);
    return () => {
      cancelled = true;
      window.clearInterval(eventTimer);
      window.clearInterval(streamTimer);
      window.clearInterval(contextTimer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [room?.id]);

  const streamCursor = streams.map((stream) => `${stream.run_id}:${stream.text.length}`).join("|");

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [events.length, streamCursor, room?.id]);

  if (!room) return null;
  const official = room.home_kind === "official";

  async function send() {
    if (sending || !room) return;
    const value = text.trim();
    if (!value && files.length === 0) return;
    if (official && files.length > 0) {
      onError(new Error(t("conversation.attachUnsupported")));
      return;
    }
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
            {official ? t("conversation.officialHome") : shortID(room.home_node)}
          </p>
        </div>
        <div className="conversation-actions">
          <div className="room-badge">{official ? t("conversation.officialBadge") : t("conversation.encrypted")}</div>
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
        {streams.map((stream) => (
          <AgentStreamCard key={stream.run_id} stream={stream} t={t} />
        ))}
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
            <button
              className="icon-button"
              onClick={() => fileRef.current?.click()}
              title={official ? t("conversation.attachUnsupported") : t("conversation.attach")}
              disabled={official}
            >
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
    case "agent.joined": {
      const name = String(payload.name ?? payload.agent_id);
      const ownerID = String(payload.owner_id ?? "");
      const sharedNote =
        ownerID && ownerID !== account.id
          ? ` · ${t("event.sharedAgent", { name: memberNames[ownerID] || shortID(ownerID) })}`
          : "";
      return <SystemEvent text={`${name} + ${t("nav.chat")}${sharedNote}`} />;
    }
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
    case "policy.updated": {
      const target = String(payload.agent_id ?? "")
        ? agentNames[String(payload.agent_id)] || shortID(String(payload.agent_id))
        : t("policies.roomDefault");
      return (
        <SystemEvent
          text={t("event.policyUpdated", { target, count: Number(payload.rule_count ?? 0) })}
        />
      );
    }
    case "policy.deleted": {
      const target = String(payload.agent_id ?? "")
        ? agentNames[String(payload.agent_id)] || shortID(String(payload.agent_id))
        : t("policies.roomDefault");
      return <SystemEvent text={t("event.policyDeleted", { target })} />;
    }
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
      const agentID = typeof payload.agent_id === "string" ? payload.agent_id : "";
      const isAgent = agentID !== "";
      const mine = !isAgent && event.author_id === account.id;
      const authorName = isAgent
        ? agentNames[agentID] || shortID(agentID)
        : mine
          ? account.name
          : memberNames[event.author_id] || shortID(event.author_id);
      const body = String(payload.text ?? "");
      const attachmentIDs = Array.isArray(payload.attachments) ? (payload.attachments as string[]) : [];
      const mentions = Array.isArray(payload.mentions) ? (payload.mentions as string[]) : [];
      return (
        <article className={`message ${mine ? "mine" : ""} ${isAgent ? "agent" : ""}`}>
          <div className="message-meta">
            <span>
              {authorName}
              {isAgent && <span className="agent-badge">{t("conversation.agentBadge")}</span>}
            </span>
            <time>{formatTime(event.created_at, locale)}</time>
          </div>
          <div className="bubble">
            {body && (payload.format === "markdown" ? <Markdown text={body} /> : <p className="plain-text">{body}</p>)}
            {mentions.length > 0 && (
              <p className="fine-print">
                @ {mentions.map((id) => agentNames[id] || memberNames[id] || shortID(id)).join(" ")}
              </p>
            )}
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
  const official = room.home_kind === "official";
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
          {official ? (
            <>
              <p className="muted">{t("roomInfo.officialNotice")}</p>
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
                <>
                  <div className="invite-secret">
                    <code>{invite}</code>
                    <CopyButton value={invite} labels={{ copy: t("roomInfo.copy"), copied: t("roomInfo.copied") }} />
                  </div>
                  <div className="invite-qr">
                    <QRCodeImage
                      value={invite}
                      alt={t("roomInfo.inviteQRAlt")}
                      size={220}
                      errorLabel={t("common.qrError")}
                    />
                    <p className="fine-print">{t("roomInfo.inviteQRHint")}</p>
                  </div>
                </>
              )}
            </>
          ) : remote ? (
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
                <>
                  <div className="invite-secret">
                    <code>{invite}</code>
                    <CopyButton value={invite} labels={{ copy: t("roomInfo.copy"), copied: t("roomInfo.copied") }} />
                  </div>
                  <div className="invite-qr">
                    <QRCodeImage
                      value={invite}
                      alt={t("roomInfo.inviteQRAlt")}
                      size={220}
                      errorLabel={t("common.qrError")}
                    />
                    <p className="fine-print">{t("roomInfo.inviteQRHint")}</p>
                  </div>
                </>
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
              <dd>{official ? t("roomInfo.official") : remote ? t("roomInfo.remote") : t("roomInfo.local")}</dd>
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
