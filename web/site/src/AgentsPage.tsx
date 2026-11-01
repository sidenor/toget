import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Modal, FormError } from "./dialogs";
import { copyText } from "./helpers";
import {
  BridgeError,
  INSTALL_COMMAND,
  LOCAL_UI_URL,
  agents as listAgents,
  createAgent,
  directory as listDirectory,
  forgetBridge,
  loadBridge,
  machine as loadMachine,
  pairLocal,
  probeLocal,
  revokeAgent,
  session as loadSession,
  unpairLocal,
  updateAgent,
} from "./local";
import type {
  BridgeAgent,
  BridgeDirectory,
  BridgeMachine,
  SavedBridge,
} from "./local";
import { Icon } from "./ui";

type PageState = "idle" | "loading" | "ready" | "locked" | "offline" | "error";

const RUNTIMES = [
  { value: "claude-code", label: "Claude Code" },
  { value: "codex", label: "Codex" },
  { value: "custom", label: "自定义" },
];

const PERMISSIONS = [
  { key: "write", label: "写入文件", hint: "在自己的工作目录里改动文件" },
  { key: "shell", label: "执行命令", hint: "运行 shell 命令" },
  { key: "external", label: "对外发送", hint: "发消息、调用外部服务" },
];

function osLabel(os: string): string {
  switch (os) {
    case "darwin":
      return "macOS";
    case "linux":
      return "Linux";
    case "windows":
      return "Windows";
    default:
      return os || "未知系统";
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "running":
      return "运行中";
    case "waiting_approval":
      return "等待审批";
    case "offline":
      return "离线";
    case "revoked":
      return "已撤销";
    default:
      return "空闲";
  }
}

function describeBridgeError(cause: unknown): string {
  if (cause instanceof BridgeError) {
    if (cause.status === 0) {
      return cause.message;
    }
    if (cause.status === 401) {
      return "连接已失效，请在本机重新生成配对码。";
    }
    if (cause.status === 403) {
      return "本机没有允许这个网页访问，请从 https://toget.vu 打开。";
    }
    if (cause.status === 409) {
      return cause.code === "no_account"
        ? "这台电脑还没有创建 Toget 账号。先在本地页面创建账号，再回来配对。"
        : cause.message;
    }
    if (cause.status === 423) {
      return "本机已锁定。请先在电脑上打开 Toget 解锁。";
    }
    if (cause.status === 503) {
      return "本机的 Toget 版本还没有开放网页管理，请更新后重试。";
    }
    return cause.message;
  }
  return cause instanceof Error ? cause.message : "操作失败，请重试。";
}

export function AgentsPage({
  isMobile,
  onToast,
}: {
  isMobile: boolean;
  onToast: (message: string) => void;
}) {
  const [saved, setSaved] = useState<SavedBridge | null>(() => loadBridge());
  const [machine, setMachine] = useState<BridgeMachine | null>(null);
  const [agents, setAgents] = useState<BridgeAgent[]>([]);
  const [state, setState] = useState<PageState>("idle");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [probe, setProbe] = useState<boolean | null>(null);
  const [code, setCode] = useState("");
  const [pairing, setPairing] = useState(false);
  const [pairError, setPairError] = useState<string | null>(null);
  const [dialog, setDialog] = useState<"add" | BridgeAgent | null>(null);
  const [confirmRevoke, setConfirmRevoke] = useState<string | null>(null);
  const [disconnectBusy, setDisconnectBusy] = useState(false);
  const [directoryOpen, setDirectoryOpen] = useState(false);
  const [directory, setDirectory] = useState<BridgeDirectory | null>(null);
  const [directoryLoading, setDirectoryLoading] = useState(false);
  const [directoryError, setDirectoryError] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  const load = useCallback(async (current: SavedBridge) => {
    setState("loading");
    setError(null);
    try {
      const result = await loadSession(current.token);
      setMachine(result.machine);
      if (result.machine.locked) {
        setState("locked");
        return;
      }
      const [machineResult, agentResult] = await Promise.all([
        loadMachine(current.token),
        listAgents(current.token),
      ]);
      setMachine(machineResult);
      setAgents(agentResult.agents);
      setState("ready");
    } catch (cause) {
      if (cause instanceof BridgeError) {
        if (cause.status === 401 || cause.status === 403) {
          forgetBridge();
          setSaved(null);
          setMachine(null);
          setAgents([]);
          setState("idle");
          setNotice("原来的连接已经失效，请重新配对这台电脑。");
          return;
        }
        if (cause.status === 423) {
          setState("locked");
          return;
        }
        if (cause.status === 0) {
          setState("offline");
          setError(describeBridgeError(cause));
          return;
        }
      }
      setState("error");
      setError(describeBridgeError(cause));
    }
  }, []);

  useEffect(() => {
    if (!saved) {
      setMachine(null);
      setAgents([]);
      setState("idle");
      return;
    }
    let cancelled = false;
    void (async () => {
      if (cancelled) return;
      await load(saved);
    })();
    return () => {
      cancelled = true;
    };
  }, [saved, load]);

  useEffect(() => {
    if (saved) {
      return;
    }
    let alive = true;
    const check = async () => {
      const found = await probeLocal();
      if (alive) {
        setProbe(found);
      }
    };
    void check();
    const timer = window.setInterval(() => void check(), 5000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [saved]);

  useEffect(() => {
    if (!confirmRevoke) {
      return;
    }
    const timer = window.setTimeout(() => setConfirmRevoke(null), 4000);
    return () => window.clearTimeout(timer);
  }, [confirmRevoke]);

  const copy = async (key: string, value: string) => {
    if (await copyText(value)) {
      setCopied(key);
      window.setTimeout(() => setCopied(null), 1800);
    }
  };

  const pair = async (event: FormEvent) => {
    event.preventDefault();
    const value = code.replace(/\D/g, "").slice(0, 6);
    if (value.length !== 6) {
      setPairError("请输入本机显示的 6 位配对码。");
      return;
    }
    setPairing(true);
    setPairError(null);
    try {
      const result = await pairLocal(value);
      setSaved(result);
      setMachine(result.machine);
      setCode("");
      setNotice(null);
      onToast(`已连接「${result.machine.name}」`);
    } catch (cause) {
      setPairError(
        cause instanceof BridgeError && cause.status === 401
          ? "配对码不对或已经过期，请在电脑上重新生成。"
          : describeBridgeError(cause),
      );
    } finally {
      setPairing(false);
    }
  };

  const retry = () => {
    if (saved) {
      void load(saved);
    }
  };

  const disconnect = async () => {
    if (!saved) {
      return;
    }
    setDisconnectBusy(true);
    try {
      await unpairLocal(saved.token);
      setSaved(null);
      setMachine(null);
      setAgents([]);
      setState("idle");
      setDirectory(null);
      setDirectoryOpen(false);
      onToast("已断开这台电脑");
    } finally {
      setDisconnectBusy(false);
    }
  };

  const refreshAgents = async () => {
    if (!saved) {
      return;
    }
    try {
      const [machineResult, agentResult] = await Promise.all([
        loadMachine(saved.token),
        listAgents(saved.token),
      ]);
      setMachine(machineResult);
      setAgents(agentResult.agents);
    } catch (cause) {
      const message = describeBridgeError(cause);
      if (cause instanceof BridgeError && cause.status === 423) {
        setState("locked");
      } else {
        onToast(message);
      }
    }
  };

  const openDirectory = async (path?: string) => {
    if (!saved) {
      return;
    }
    setDirectoryOpen(true);
    setDirectoryLoading(true);
    setDirectoryError(null);
    try {
      setDirectory(await listDirectory(saved.token, path));
    } catch (cause) {
      setDirectoryError(describeBridgeError(cause));
    } finally {
      setDirectoryLoading(false);
    }
  };

  const saveAgent = async (
    name: string,
    runtime: string,
    permissions: Record<string, boolean>,
  ) => {
    if (!saved) {
      return false;
    }
    try {
      if (dialog && dialog !== "add") {
        await updateAgent(saved.token, dialog.id, name, runtime, permissions);
        onToast("Agent 设置已保存");
      } else {
        await createAgent(saved.token, name, runtime, permissions);
        onToast("Agent 已添加");
      }
      setDialog(null);
      await refreshAgents();
      return true;
    } catch (cause) {
      throw new Error(describeBridgeError(cause));
    }
  };

  const revoke = async (agent: BridgeAgent) => {
    if (!saved || confirmRevoke !== agent.id) {
      setConfirmRevoke(agent.id);
      return;
    }
    setConfirmRevoke(null);
    try {
      await revokeAgent(saved.token, agent.id);
      onToast(`已撤销「${agent.name}」`);
      await refreshAgents();
    } catch (cause) {
      onToast(describeBridgeError(cause));
    }
  };

  const installCommand = /Windows/i.test(navigator.userAgent)
    ? 'powershell -c "irm https://toget.vu/install.ps1 | iex"'
    : INSTALL_COMMAND;

  const machineName = machine?.name || saved?.machine.name || "这台电脑";

  return (
    <div className={`page agents-page ${isMobile ? "mobile-page" : ""}`}>
      {isMobile ? (
        <div className="mobile-head">
          <div className="mobile-heading">
            <Icon name="sparkle" size={24} />
            <h1>Agents</h1>
          </div>
        </div>
      ) : (
        <div className="desktop-page-head">
          <h1>Agents</h1>
          <p className="desktop-page-sub">
            Agent 跑在你自己的电脑上，由你授权和管理
          </p>
        </div>
      )}

      <div className="page-scroll">
        <div className="page-inner agents-inner">
          {notice && (
            <div className="agent-notice" role="status">
              <Icon name="info" size={16} />
              <span>{notice}</span>
              <button type="button" onClick={() => setNotice(null)} aria-label="关闭">
                <Icon name="close" size={14} />
              </button>
            </div>
          )}

          {!saved && (
            <>
              <section className="agent-hero">
                <span className="agent-hero-mark">
                  <Icon name="sparkle" size={26} />
                </span>
                <div className="agent-hero-copy">
                  <h2>让 Agent 在自己的电脑上工作</h2>
                  <p>
                    Toget 的 Agent 不在云端运行。它住在这台电脑里，使用你的文件、
                    你的模型，也只在你允许的范围内行动。
                  </p>
                </div>
                <span className={`agent-probe ${probe ? "found" : probe === false ? "" : "checking"}`}>
                  <span className="agent-probe-dot" />
                  {probe === null
                    ? "正在检查本机…"
                    : probe
                      ? "已检测到本机 Toget"
                      : "未检测到本机 Toget"}
                </span>
              </section>

              <section className="agent-steps">
                <article className="agent-step">
                  <span className="agent-step-index">1</span>
                  <div className="agent-step-copy">
                    <h3>安装 Toget</h3>
                    <p>在电脑上打开终端，运行这一行。</p>
                    <div className="agent-command">
                      <code>{installCommand}</code>
                      <button
                        type="button"
                        className="agent-copy"
                        aria-label="复制安装命令"
                        onClick={() => void copy("install", installCommand)}
                      >
                        <Icon name={copied === "install" ? "check" : "copy"} size={15} />
                      </button>
                    </div>
                    <p className="agent-fine">支持 macOS、Linux；Windows 使用 PowerShell。</p>
                  </div>
                </article>

                <article className="agent-step">
                  <span className="agent-step-index">2</span>
                  <div className="agent-step-copy">
                    <h3>启动本机节点</h3>
                    <p>安装完成后运行下面这行，Toget 会打开本地页面。</p>
                    <div className="agent-command">
                      <code>toget serve</code>
                      <button
                        type="button"
                        className="agent-copy"
                        aria-label="复制启动命令"
                        onClick={() => void copy("serve", "toget serve")}
                      >
                        <Icon name={copied === "serve" ? "check" : "copy"} size={15} />
                      </button>
                    </div>
                    <p className="agent-fine">
                      之后也可以直接访问{" "}
                      <a href={LOCAL_UI_URL} target="_blank" rel="noreferrer">
                        toget.localhost:7718
                      </a>
                    </p>
                  </div>
                </article>

                <article className="agent-step">
                  <span className="agent-step-index">3</span>
                  <div className="agent-step-copy">
                    <h3>把网页和电脑配对</h3>
                    <p>
                      在本地页面打开「本机」，生成一个 6 位配对码，然后填在这里。
                      配对码 5 分钟内有效，只能使用一次。
                    </p>
                  </div>
                </article>
              </section>

              <section className="agent-pair-card">
                <div className="agent-pair-head">
                  <div>
                    <h3>输入配对码</h3>
                    <p>配对后，这个网页就能管理那台电脑上的 Agent。</p>
                  </div>
                  <Icon name="link" size={20} />
                </div>
                <form className="agent-pair-form" onSubmit={pair}>
                  <input
                    className="agent-code-input"
                    value={code}
                    onChange={(event) =>
                      setCode(event.target.value.replace(/\D/g, "").slice(0, 6))
                    }
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    placeholder="000000"
                    aria-label="6 位配对码"
                    maxLength={6}
                  />
                  <button
                    type="submit"
                    className="primary-button"
                    disabled={pairing || code.length !== 6}
                  >
                    {pairing ? "连接中…" : "连接这台电脑"}
                  </button>
                </form>
                {pairError && <FormError message={pairError} />}
                <p className="agent-fine">
                  网页只能看到文件夹名称，不会读取文件内容。所有 Agent 操作都需要本机解锁。
                </p>
              </section>
            </>
          )}

          {saved && (
            <>
              {machine && (
                <section className="agent-machine-card">
                  <span className="agent-machine-mark">
                    <Icon name="device" size={24} />
                  </span>
                  <div className="agent-machine-copy">
                    <div className="agent-machine-title">
                      <h2>{machineName}</h2>
                      <span
                        className={`agent-connection ${
                          state === "ready" ? "online" : state === "locked" ? "locked" : ""
                        }`}
                      >
                        <span className="agent-probe-dot" />
                        {state === "ready"
                          ? "已连接"
                          : state === "locked"
                            ? "已锁定"
                            : state === "offline"
                              ? "离线"
                              : "连接中"}
                      </span>
                    </div>
                    <p>
                      {osLabel(machine.os)} · {machine.arch} · Toget {machine.version}
                    </p>
                    <a href={machine.local_url || LOCAL_UI_URL} target="_blank" rel="noreferrer">
                      {machine.local_url || LOCAL_UI_URL}
                      <Icon name="link" size={12} />
                    </a>
                  </div>
                  <button
                    type="button"
                    className="text-button"
                    onClick={() => void disconnect()}
                    disabled={disconnectBusy}
                  >
                    {disconnectBusy ? "断开中…" : "断开"}
                  </button>
                </section>
              )}

              {machine && state !== "locked" && (
                <div className="agent-machine-stats">
                  <div>
                    <span>本机账号</span>
                    <strong>{machine.account?.name ?? "未创建"}</strong>
                  </div>
                  <div>
                    <span>Agents</span>
                    <strong>{agents.length}</strong>
                  </div>
                  <div>
                    <span>系统</span>
                    <strong>{osLabel(machine.os)}</strong>
                  </div>
                </div>
              )}

              {state === "loading" && (
                <div className="agent-loading">
                  <span className="spinner" />
                  <span>正在连接这台电脑…</span>
                </div>
              )}

              {state === "offline" && (
                <section className="agent-state-card">
                  <Icon name="wifiOff" size={21} />
                  <div>
                    <h3>这台电脑现在联系不上</h3>
                    <p>{error || "请确认电脑上仍在运行 `toget serve`。"}</p>
                  </div>
                  <button type="button" className="secondary-button small" onClick={retry}>
                    重试
                  </button>
                </section>
              )}

              {state === "error" && (
                <section className="agent-state-card error">
                  <Icon name="info" size={21} />
                  <div>
                    <h3>还不能管理这台电脑</h3>
                    <p>{error}</p>
                  </div>
                  <button type="button" className="secondary-button small" onClick={retry}>
                    重试
                  </button>
                </section>
              )}

              {state === "locked" && (
                <section className="agent-state-card locked">
                  <Icon name="lock" size={21} />
                  <div>
                    <h3>本机已锁定</h3>
                    <p>
                      为了安全，管理 Agent 需要先在本机解锁。
                      在电脑上打开 Toget，输入密码后再回来。
                    </p>
                  </div>
                  <a
                    className="secondary-button small"
                    href={machine?.local_url || LOCAL_UI_URL}
                    target="_blank"
                    rel="noreferrer"
                  >
                    打开本机 Toget
                  </a>
                  <button type="button" className="text-button" onClick={retry}>
                    已解锁，刷新
                  </button>
                </section>
              )}

              {(state === "ready" || state === "loading") && (
                <>
                  <div className="agent-section-head">
                    <div>
                      <h3>这台电脑上的 Agent</h3>
                      <p>Agent 参与房间，每次越权动作都会先征求你的同意。</p>
                    </div>
                    <button
                      type="button"
                      className="primary-button small"
                      onClick={() => setDialog("add")}
                      disabled={state !== "ready"}
                    >
                      <Icon name="plus" size={15} />
                      添加 Agent
                    </button>
                  </div>

                  {state === "ready" && agents.length === 0 && (
                    <div className="agent-empty">
                      <Icon name="sparkle" size={24} />
                      <strong>还没有 Agent</strong>
                      <p>添加一个 Claude Code 或 Codex，让它加入你的房间。</p>
                      <button type="button" className="secondary-button small" onClick={() => setDialog("add")}>
                        添加第一个 Agent
                      </button>
                    </div>
                  )}

                  <div className="agent-list">
                    {agents.map((agent) => (
                      <article className="agent-card" key={agent.id}>
                        <div className="agent-card-top">
                          <span className="agent-avatar">
                            {agent.name.slice(0, 1).toUpperCase()}
                          </span>
                          <div className="agent-card-name">
                            <h4>{agent.name}</h4>
                            <p>
                              {RUNTIMES.find((item) => item.value === agent.runtime)?.label ??
                                agent.runtime}
                              {agent.revoked_at ? " · 已撤销" : ""}
                            </p>
                          </div>
                          <span className={`agent-status status-${agent.status}`}>
                            {statusLabel(agent.status)}
                          </span>
                        </div>
                        <div className="agent-perm-chips">
                          {PERMISSIONS.filter((item) => agent.permissions?.[item.key]).map((item) => (
                            <span key={item.key}>{item.label}</span>
                          ))}
                          {!PERMISSIONS.some((item) => agent.permissions?.[item.key]) && (
                            <span className="muted">仅在你批准后行动</span>
                          )}
                        </div>
                        {agent.rooms && agent.rooms.length > 0 && (
                          <p className="agent-rooms-line">
                            房间：
                            {agent.rooms.map((room) => room.name || room.id).join("、")}
                          </p>
                        )}
                        {!agent.revoked_at && (
                          <div className="agent-card-actions">
                            <button
                              type="button"
                              className="secondary-button small"
                              onClick={() => setDialog(agent)}
                            >
                              设置
                            </button>
                            <button
                              type="button"
                              className={`${
                                confirmRevoke === agent.id ? "danger-button solid" : "danger-button"
                              } small`}
                              onClick={() => void revoke(agent)}
                            >
                              {confirmRevoke === agent.id ? "确认撤销" : "撤销"}
                            </button>
                          </div>
                        )}
                      </article>
                    ))}
                  </div>

                  <section className="agent-folder-card">
                    <button
                      type="button"
                      className="agent-folder-head"
                      onClick={() =>
                        directoryOpen ? setDirectoryOpen(false) : void openDirectory()
                      }
                      aria-expanded={directoryOpen}
                    >
                      <span className="agent-folder-icon">
                        <Icon name="file" size={18} />
                      </span>
                      <span className="agent-folder-heading">
                        <strong>工作目录</strong>
                        <small>看看这台电脑上 Agent 可以使用的文件夹</small>
                      </span>
                      <Icon
                        name="chevronRight"
                        size={16}
                        className={directoryOpen ? "agent-chevron open" : "agent-chevron"}
                      />
                    </button>

                    {directoryOpen && (
                      <div className="agent-folder-body">
                        {directoryLoading && (
                          <div className="agent-loading small">
                            <span className="spinner" />
                            <span>读取文件夹…</span>
                          </div>
                        )}
                        {directoryError && <p className="agent-folder-error">{directoryError}</p>}
                        {directory && !directoryLoading && (
                          <>
                            <div className="agent-path-row">
                              {directory.parent && (
                                <button
                                  type="button"
                                  className="agent-up"
                                  aria-label="上一级"
                                  onClick={() => void openDirectory(directory.parent)}
                                >
                                  <Icon name="back" size={14} />
                                </button>
                              )}
                              <code>{directory.relative || "~"}</code>
                              <button
                                type="button"
                                className="agent-copy path"
                                aria-label="复制文件夹路径"
                                onClick={() => void copy("path", directory.path)}
                              >
                                <Icon name={copied === "path" ? "check" : "copy"} size={14} />
                              </button>
                            </div>
                            <div className="agent-shortcuts">
                              <button type="button" onClick={() => void openDirectory(directory.home)}>
                                ~
                              </button>
                              {directory.roots.map((root) => (
                                <button
                                  type="button"
                                  key={root.path}
                                  onClick={() => void openDirectory(root.path)}
                                >
                                  {root.name}
                                </button>
                              ))}
                            </div>
                            <div className="agent-folder-list">
                              {directory.entries.map((entry) => (
                                <button
                                  type="button"
                                  key={entry.path}
                                  onClick={() => void openDirectory(entry.path)}
                                >
                                  <span className="agent-folder-dot" />
                                  <span>{entry.name}</span>
                                </button>
                              ))}
                              {directory.entries.length === 0 && (
                                <p className="agent-folder-empty">这里没有子文件夹</p>
                              )}
                            </div>
                            {directory.truncated && (
                              <p className="agent-fine">文件夹太多，只显示前 500 个。</p>
                            )}
                            <p className="agent-fine">
                              只显示文件夹名称，不读取文件内容。隐藏文件夹不会出现在这里。
                            </p>
                          </>
                        )}
                      </div>
                    )}
                  </section>
                </>
              )}
            </>
          )}
        </div>
      </div>

      {dialog && saved && (
        <AgentDialog
          isMobile={isMobile}
          agent={dialog === "add" ? null : dialog}
          onClose={() => setDialog(null)}
          onSubmit={saveAgent}
        />
      )}
    </div>
  );
}

function AgentDialog({
  isMobile,
  agent,
  onClose,
  onSubmit,
}: {
  isMobile: boolean;
  agent: BridgeAgent | null;
  onClose: () => void;
  onSubmit: (
    name: string,
    runtime: string,
    permissions: Record<string, boolean>,
  ) => Promise<boolean>;
}) {
  const [name, setName] = useState(agent?.name ?? "");
  const [runtime, setRuntime] = useState(agent?.runtime ?? "claude-code");
  const [permissions, setPermissions] = useState<Record<string, boolean>>(() => ({
    write: Boolean(agent?.permissions?.write),
    shell: Boolean(agent?.permissions?.shell),
    external: Boolean(agent?.permissions?.external),
  }));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    input.current?.focus();
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) {
      setError("给 Agent 取一个名字。");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await onSubmit(name.trim(), runtime, permissions);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "保存失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      isMobile={isMobile}
      title={agent ? "Agent 设置" : "添加 Agent"}
      subtitle={agent ? undefined : "Agent 会在这台电脑上运行"}
      onClose={busy ? undefined : onClose}
      dismissible={!busy}
      footer={
        <div className="dialog-actions">
          <button type="button" className="secondary-button" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="submit"
            form="agent-settings-form"
            className="primary-button"
            disabled={busy || !name.trim()}
          >
            {busy ? "保存中…" : agent ? "保存" : "添加"}
          </button>
        </div>
      }
    >
      <form id="agent-settings-form" className="dialog-form" onSubmit={submit}>
        <label className="field">
          <span>名字</span>
          <input
            ref={input}
            value={name}
            maxLength={64}
            placeholder="例如 Claude"
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <label className="field">
          <span>运行时</span>
          <select value={runtime} onChange={(event) => setRuntime(event.target.value)}>
            {RUNTIMES.map((item) => (
              <option value={item.value} key={item.value}>
                {item.label}
              </option>
            ))}
          </select>
        </label>
        <fieldset className="agent-permissions-field">
          <legend>允许它做什么</legend>
          {PERMISSIONS.map((item) => (
            <label className="agent-permission" key={item.key}>
              <span>
                <strong>{item.label}</strong>
                <small>{item.hint}</small>
              </span>
              <input
                type="checkbox"
                checked={Boolean(permissions[item.key])}
                onChange={(event) =>
                  setPermissions((current) => ({
                    ...current,
                    [item.key]: event.target.checked,
                  }))
                }
              />
            </label>
          ))}
        </fieldset>
        <p className="agent-fine">
          即使勾选，超出授权范围的动作仍会在房间里等你审批。修改设置只影响之后的动作。
        </p>
        <FormError message={error} />
      </form>
    </Modal>
  );
}
