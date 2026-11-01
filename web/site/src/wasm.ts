import type {
  Profile,
  RecoveryFile,
  RoomSummary,
  RoomView,
} from "./types";

export class SiteError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "SiteError";
    this.code = code;
  }
}

interface RawError {
  code?: unknown;
  message?: unknown;
}

interface RestoreResult {
  ok?: boolean;
  code?: string;
  message?: string;
}

interface RawBridge {
  fatal?: RawError;
  restore(encoded: string): RestoreResult;
  exportState(): string;
  profile(): unknown;
  rooms(): unknown;
  room(id: string): unknown;
  consumeRecovery(): unknown;
  logout(): RestoreResult;
  call(op: string, payload?: string): Promise<unknown>;
}

interface GoRuntime {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}

declare global {
  interface Window {
    Go?: new () => GoRuntime;
    togetSite?: RawBridge;
  }
}

const WASM_URL = "/wasm/toget-site.wasm";
const WASM_EXEC_URL = "/wasm/wasm_exec.js";
const BRIDGE_TIMEOUT = 30_000;

function errorFrom(value: unknown): SiteError {
  if (value instanceof SiteError) {
    return value;
  }
  if (typeof value === "object" && value !== null) {
    const raw = value as RawError;
    const code = typeof raw.code === "string" && raw.code ? raw.code : "internal";
    const message =
      typeof raw.message === "string" && raw.message
        ? raw.message
        : "操作失败，请重试。";
    return new SiteError(code, message);
  }
  if (value instanceof Error) {
    return new SiteError("internal", value.message || "操作失败，请重试。");
  }
  return new SiteError("internal", "操作失败，请重试。");
}

function assertOk(result: RestoreResult): void {
  if (!result || result.ok) {
    return;
  }
  throw new SiteError(result.code || "internal", result.message || "操作失败。");
}

async function ensureWasmExec(): Promise<void> {
  if (window.Go) {
    return;
  }
  // The script tag normally loads before the module. If it was blocked or
  // removed, load it once more so a stale cache cannot strand the page.
  await new Promise<void>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(
      `script[src="${WASM_EXEC_URL}"]`,
    );
    if (existing) {
      existing.addEventListener("load", () => resolve(), { once: true });
      existing.addEventListener(
        "error",
        () => reject(new SiteError("unavailable", "安全模块加载失败。")),
        { once: true },
      );
      return;
    }
    const script = document.createElement("script");
    script.src = WASM_EXEC_URL;
    script.async = true;
    script.onload = () => resolve();
    script.onerror = () =>
      reject(new SiteError("unavailable", "安全模块加载失败。"));
    document.head.appendChild(script);
  });
  if (!window.Go) {
    throw new SiteError("unavailable", "这个浏览器无法运行 Toget 的安全模块。");
  }
}

async function fetchWasm(
  onProgress?: (loaded: number, total: number) => void,
): Promise<Uint8Array> {
  let response: Response;
  try {
    response = await fetch(WASM_URL, { cache: "no-cache" });
  } catch {
    throw new SiteError("offline", "连不上服务器，请检查网络后重试。");
  }
  if (!response.ok) {
    throw new SiteError("unavailable", "安全模块下载失败。");
  }
  const total = Number(response.headers.get("content-length") || 0);
  const reader = response.body?.getReader();
  if (!reader) {
    const bytes = new Uint8Array(await response.arrayBuffer());
    onProgress?.(bytes.byteLength, bytes.byteLength);
    return bytes;
  }
  const chunks: Uint8Array[] = [];
  let loaded = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    if (value) {
      chunks.push(value);
      loaded += value.byteLength;
      onProgress?.(loaded, total);
    }
  }
  const bytes = new Uint8Array(loaded);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  onProgress?.(loaded, total || loaded);
  return bytes;
}

async function waitForBridge(): Promise<RawBridge> {
  const deadline = Date.now() + BRIDGE_TIMEOUT;
  while (Date.now() < deadline) {
    const bridge = window.togetSite;
    if (bridge) {
      if (bridge.fatal) {
        throw errorFrom(bridge.fatal);
      }
      return bridge;
    }
    await new Promise((resolve) => window.setTimeout(resolve, 25));
  }
  throw new SiteError("unavailable", "安全模块启动超时，请刷新页面重试。");
}

export interface SiteClient {
  restore(encoded: string): void;
  exportState(): string;
  profile(): Profile | null;
  rooms(): RoomSummary[];
  room(id: string): RoomView | null;
  consumeRecovery(): RecoveryFile | null;
  logout(): void;
  call<T>(op: string, payload?: unknown): Promise<T>;
}

let loading: Promise<SiteClient> | null = null;

export function loadSite(
  onProgress?: (loaded: number, total: number) => void,
): Promise<SiteClient> {
  if (loading) {
    return loading;
  }
  loading = (async () => {
    await ensureWasmExec();
    const bytes = await fetchWasm(onProgress);
    const Runtime = window.Go;
    if (!Runtime) {
      throw new SiteError("unavailable", "这个浏览器无法运行 Toget 的安全模块。");
    }
    const go = new Runtime();
    let instance: WebAssembly.Instance;
    try {
      const source = await WebAssembly.instantiate(
        bytes.buffer as ArrayBuffer,
        go.importObject,
      );
      instance = source.instance;
    } catch {
      throw new SiteError("unavailable", "安全模块无法启动，请刷新页面重试。");
    }
    void go.run(instance).catch((error: unknown) => {
      console.error("toget wasm stopped", error);
    });
    const bridge = await waitForBridge();
    const client: SiteClient = {
      restore(encoded) {
        assertOk(bridge.restore(encoded));
      },
      exportState() {
        const encoded = bridge.exportState();
        if (typeof encoded !== "string") {
          throw errorFrom(encoded);
        }
        return encoded;
      },
      profile() {
        const value = bridge.profile();
        return value && typeof value === "object" ? (value as Profile) : null;
      },
      rooms() {
        const value = bridge.rooms();
        return Array.isArray(value) ? (value as RoomSummary[]) : [];
      },
      room(id) {
        const value = bridge.room(id);
        return value && typeof value === "object" ? (value as RoomView) : null;
      },
      consumeRecovery() {
        const value = bridge.consumeRecovery();
        return value && typeof value === "object"
          ? (value as RecoveryFile)
          : null;
      },
      logout() {
        assertOk(bridge.logout());
      },
      async call<T>(op: string, payload?: unknown): Promise<T> {
        // Start every operation from a clean task. A Go callback that just
        // settled a promise may still be unwinding when the page reacts.
        await new Promise<void>((resolve) => window.setTimeout(resolve, 0));
        let value: unknown;
        let failure: unknown;
        let failed = false;
        try {
          value = await bridge.call(
            op,
            payload === undefined ? "" : JSON.stringify(payload),
          );
        } catch (error) {
          failure = error;
          failed = true;
        }
        if (failed) {
          throw errorFrom(failure);
        }
        return value as T;
      },
    };
    return client;
  })().catch((error: unknown) => {
    loading = null;
    throw errorFrom(error);
  });
  return loading;
}

export function isOfflineError(error: unknown): boolean {
  const code = errorFrom(error).code;
  return (
    code === "offline" ||
    code === "network" ||
    code === "host_unavailable" ||
    code === "unavailable"
  );
}
