// The browser keeps its Toget state in IndexedDB, encrypted with a
// non-extractable AES-GCM key. The key never becomes readable JavaScript: the
// browser only ever hands it back to SubtleCrypto.

const DB_NAME = "toget-site-vault";
const DB_VERSION = 1;
const STORE = "vault";
const KEY_ID = "wrap-key";
const STATE_ID = "state";
const AAD = new TextEncoder().encode("toget:site:vault:v1");

export type VaultFailure = "unavailable" | "corrupt" | "blocked";

export class VaultError extends Error {
  readonly kind: VaultFailure;

  constructor(kind: VaultFailure, message: string) {
    super(message);
    this.name = "VaultError";
    this.kind = kind;
  }
}

interface StoredState {
  version: 1;
  iv: ArrayBuffer;
  data: ArrayBuffer;
}

let databasePromise: Promise<IDBDatabase> | null = null;
let keyPromise: Promise<CryptoKey | null> | null = null;

function supported(): boolean {
  return (
    typeof indexedDB !== "undefined" &&
    typeof crypto !== "undefined" &&
    !!crypto.subtle &&
    window.isSecureContext
  );
}

function openDatabase(): Promise<IDBDatabase> {
  if (!supported()) {
    return Promise.reject(
      new VaultError("unavailable", "这个浏览器无法安全保存登录状态。"),
    );
  }
  if (!databasePromise) {
    const pending = new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, DB_VERSION);
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(STORE)) {
          db.createObjectStore(STORE);
        }
      };
      request.onerror = () => {
        reject(
          new VaultError(
            "unavailable",
            "浏览器拒绝了本地存储，可能是隐私模式或存储空间不足。",
          ),
        );
      };
      request.onblocked = () => {
        reject(
          new VaultError(
            "blocked",
            "另一个 Toget 页面正在升级本地存储，请关闭其他标签页后重试。",
          ),
        );
      };
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => db.close();
        resolve(db);
      };
    }).catch((error: unknown) => {
      databasePromise = null;
      throw error;
    });
    databasePromise = pending;
    return pending;
  }
  return databasePromise;
}

async function transact<T>(
  mode: IDBTransactionMode,
  operation: (store: IDBObjectStore) => IDBRequest<T> | void,
): Promise<T | undefined> {
  const db = await openDatabase();
  return new Promise<T | undefined>((resolve, reject) => {
    let request: IDBRequest<T> | undefined;
    const transaction = db.transaction(STORE, mode);
    const store = transaction.objectStore(STORE);
    try {
      request = operation(store) ?? undefined;
    } catch (error) {
      reject(error);
      transaction.abort();
      return;
    }
    transaction.oncomplete = () => resolve(request?.result);
    transaction.onerror = () =>
      reject(
        new VaultError("unavailable", "无法读写本地存储，请检查浏览器设置。"),
      );
    transaction.onabort = () =>
      reject(
        new VaultError("unavailable", "无法读写本地存储，请检查浏览器设置。"),
      );
  });
}

async function withKeyLock<T>(run: () => Promise<T>): Promise<T> {
  const locks = navigator.locks;
  if (!locks?.request) {
    return run();
  }
  return locks.request("toget-site-vault-key", run);
}

async function getStoredKey(): Promise<CryptoKey | null> {
  const value = await transact<CryptoKey>("readonly", (store) =>
    store.get(KEY_ID),
  );
  return value instanceof CryptoKey ? value : null;
}

async function getKey(): Promise<CryptoKey | null> {
  if (!supported()) {
    return null;
  }
  if (!keyPromise) {
    keyPromise = withKeyLock(async () => {
      const existing = await getStoredKey();
      if (existing) {
        return existing;
      }
      const key = await crypto.subtle.generateKey(
        { name: "AES-GCM", length: 256 },
        false,
        ["encrypt", "decrypt"],
      );
      await transact("readwrite", (store) => {
        store.put(key, KEY_ID);
      });
      return key;
    }).catch((error: unknown) => {
      keyPromise = null;
      throw error;
    });
  }
  return keyPromise;
}

export interface LoadedVault {
  state: string | null;
  unavailable?: string;
  // The stored state existed but could not be used. The user needs to sign in
  // again, and should be told why.
  corrupt?: boolean;
}

export async function loadVault(): Promise<LoadedVault> {
  try {
    const key = await getKey();
    if (!key) {
      return { state: null };
    }
    const stored = await transact<StoredState>("readonly", (store) =>
      store.get(STATE_ID),
    );
    if (!stored) {
      return { state: null };
    }
    if (stored.version !== 1 || !stored.iv || !stored.data) {
      throw new VaultError("corrupt", "本地登录状态格式已更新。");
    }
    let plain: ArrayBuffer;
    try {
      plain = await crypto.subtle.decrypt(
        { name: "AES-GCM", iv: new Uint8Array(stored.iv), additionalData: AAD },
        key,
        stored.data,
      );
    } catch {
      throw new VaultError(
        "corrupt",
        "无法解密本地登录状态。浏览器数据可能已被修改。",
      );
    }
    return { state: new TextDecoder().decode(plain) };
  } catch (error) {
    if (error instanceof VaultError) {
      if (error.kind === "unavailable" || error.kind === "blocked") {
        return { state: null, unavailable: error.message };
      }
      // A state that cannot be decrypted is useless; drop it rather than
      // trapping the page in a boot loop.
      await clearVault().catch(() => undefined);
      return { state: null, unavailable: error.message, corrupt: true };
    }
    return {
      state: null,
      unavailable: "无法读取本地登录状态，刷新后需要重新登录。",
    };
  }
}

export async function saveVault(state: string): Promise<void> {
  const key = await getKey();
  if (!key) {
    throw new VaultError("unavailable", "这个浏览器无法安全保存登录状态。");
  }
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const encoded = new TextEncoder().encode(state);
  const data = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, additionalData: AAD },
    key,
    encoded,
  );
  const record: StoredState = {
    version: 1,
    iv: iv.buffer.slice(iv.byteOffset, iv.byteOffset + iv.byteLength),
    data,
  };
  await transact("readwrite", (store) => {
    store.put(record, STATE_ID);
  });
}

export async function clearVault(): Promise<void> {
  if (!supported()) {
    return;
  }
  await transact("readwrite", (store) => {
    store.delete(STATE_ID);
  });
}
