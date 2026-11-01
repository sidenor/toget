import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AuthScreen } from "./Auth";
import type { AuthValues } from "./Auth";
import { ChatView } from "./ChatView";
import { MePage } from "./MePage";
import {
  MembersList,
  MembersOverview,
  MembersPage,
  MemberDetail,
  aggregateMembers,
} from "./MembersPage";
import {
  ConfirmDialog,
  CreateRoomDialog,
  JoinRoomDialog,
  LogoutDialog,
  RecoverAccountDialog,
  RecoveryDialog,
  RoomInfoDialog,
} from "./dialogs";
import { profileAvatar, roomAvatar } from "./avatars";
import { listTime, roomPreview } from "./helpers";
import type {
  AuthResult,
  InviteResult,
  Profile,
  RecoveryFile,
  RefreshResult,
  RoomSummary,
  RoomView,
  StoredMember,
  Tab,
  Theme,
} from "./types";
import { Avatar, BrandMark, Icon, SearchBox } from "./ui";
import type { IconName } from "./ui";
import { clearVault, loadVault, saveVault } from "./vault";
import {
  isOfflineError,
  loadSite,
  SiteError,
} from "./wasm";
import type { SiteClient } from "./wasm";

const MOBILE_QUERY = "(max-width: 900px)";
const THEME_KEY = "toget.site.theme";
const LAST_ACCOUNT_KEY = "toget.site.lastAccount";
const ACTIVE_ROOM_KEY = "toget.site.activeRoom";
const PENDING_INVITE_KEY = "toget.site.pendingInvite";

type Phase = "booting" | "auth" | "ready" | "fatal";
type AuthMode = "login" | "register";
type ModalKind =
  | "create"
  | "join"
  | "roomInfo"
  | "logout"
  | "rotate"
  | "recover"
  | null;

interface RecoveryState {
  file: RecoveryFile;
  rotated: boolean;
}

function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(THEME_KEY);
    if (stored === "light" || stored === "dark" || stored === "system") {
      return stored;
    }
  } catch {
    // Private browsing can block storage; follow the system theme.
  }
  return "system";
}

function readLastAccount(): string {
  try {
    return localStorage.getItem(LAST_ACCOUNT_KEY) || "";
  } catch {
    return "";
  }
}

function rememberAccount(accountID: string): void {
  if (!accountID) {
    return;
  }
  try {
    localStorage.setItem(LAST_ACCOUNT_KEY, accountID);
  } catch {
    // A blocked storage area only means the next visit will not prefill.
  }
}

function readActiveRoom(): string {
  try {
    return localStorage.getItem(ACTIVE_ROOM_KEY) || "";
  } catch {
    return "";
  }
}

function rememberActiveRoom(roomID: string | null): void {
  try {
    if (roomID) {
      localStorage.setItem(ACTIVE_ROOM_KEY, roomID);
    } else {
      localStorage.removeItem(ACTIVE_ROOM_KEY);
    }
  } catch {
    // Non-critical preference.
  }
}

function inviteFromLocation(): string {
  const hash = window.location.hash;
  if (hash.startsWith("#invite=")) {
    try {
      const code = decodeURIComponent(hash.slice("#invite=".length)).trim();
      if (code) {
        return code;
      }
    } catch {
      // Fall through to the query form.
    }
  }
  try {
    return (new URLSearchParams(window.location.search).get("invite") || "").trim();
  } catch {
    return "";
  }
}

function readPendingInvite(): string {
  const fromLocation = inviteFromLocation();
  if (fromLocation) {
    rememberPendingInvite(fromLocation);
    return fromLocation;
  }
  try {
    return (sessionStorage.getItem(PENDING_INVITE_KEY) || "").trim();
  } catch {
    return "";
  }
}

function rememberPendingInvite(code: string): void {
  if (!code) {
    return;
  }
  try {
    sessionStorage.setItem(PENDING_INVITE_KEY, code);
  } catch {
    // The in-memory invite still works for this page.
  }
}

function forgetPendingInvite(): void {
  try {
    sessionStorage.removeItem(PENDING_INVITE_KEY);
  } catch {
    // Nothing else to clear.
  }
}

function toSiteError(error: unknown): SiteError {
  if (error instanceof SiteError) {
    return error;
  }
  if (error instanceof Error) {
    return new SiteError("internal", error.message || "操作失败，请重试。");
  }
  return new SiteError("internal", "操作失败，请重试。");
}

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() =>
    typeof window === "undefined" ? false : window.matchMedia(query).matches,
  );

  useEffect(() => {
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [query]);

  return matches;
}

export default function App() {
  const isMobile = useMediaQuery(MOBILE_QUERY);
  const [phase, setPhase] = useState<Phase>("booting");
  const [bootError, setBootError] = useState<string | null>(null);
  const [bootProgress, setBootProgress] = useState(0);
  const [bootNonce, setBootNonce] = useState(0);
  const [client, setClient] = useState<SiteClient | null>(null);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [rooms, setRooms] = useState<RoomSummary[]>([]);
  const [activeRoomID, setActiveRoomID] = useState<string | null>(null);
  const [roomView, setRoomView] = useState<RoomView | null>(null);
  const [tab, setTab] = useState<Tab>("chats");
  const [selectedMemberID, setSelectedMemberID] = useState<string | null>(null);
  const [theme, setTheme] = useState<Theme>(readTheme);
  const [vaultWarning, setVaultWarning] = useState<string | null>(null);
  const [online, setOnline] = useState(() => navigator.onLine);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [syncing, setSyncing] = useState(false);
  const [sendingRoom, setSendingRoom] = useState<string | null>(null);
  const [modal, setModal] = useState<ModalKind>(null);
  const [recovery, setRecovery] = useState<RecoveryState | null>(null);
  const [inviteCode, setInviteCode] = useState("");
  const [hasPendingInvite, setHasPendingInvite] = useState(
    () => !!readPendingInvite(),
  );
  const [authMode, setAuthMode] = useState<AuthMode>("login");
  const [authBusy, setAuthBusy] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);
  const [authNotice, setAuthNotice] = useState<string | null>(null);
  const [authExpired, setAuthExpired] = useState(false);
  const [lastAccountID, setLastAccountID] = useState(readLastAccount);
  const [rotateBusy, setRotateBusy] = useState(false);
  const [rotateError, setRotateError] = useState<string | null>(null);
  const [logoutBusy, setLogoutBusy] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);

  const clientRef = useRef<SiteClient | null>(null);
  const profileRef = useRef<Profile | null>(null);
  const activeRoomRef = useRef<string | null>(null);
  const tabRef = useRef<Tab>("chats");
  const refreshingRef = useRef(false);
  const syncDepth = useRef(0);
  const lastWarningRef = useRef<string | null>(null);
  const pendingInviteRef = useRef(readPendingInvite());
  const refreshRef = useRef<() => void>(() => {});

  const persistState = useCallback(async (target: SiteClient) => {
    try {
      await saveVault(target.exportState());
      setVaultWarning(null);
    } catch (cause) {
      setVaultWarning(
        cause instanceof Error
          ? cause.message
          : "无法在这台浏览器上保存登录状态。",
      );
    }
  }, []);

  const expireSession = useCallback(async (message: string) => {
    const current = clientRef.current;
    const accountID =
      profileRef.current?.account_id || readLastAccount() || "";
    if (accountID) {
      rememberAccount(accountID);
      setLastAccountID(accountID);
    }
    profileRef.current = null;
    activeRoomRef.current = null;
    try {
      current?.logout();
    } catch {
      // The wasm state is already unusable; still clear the page vault.
    }
    try {
      await clearVault();
    } catch {
      // Clearing an unavailable vault is already a no-op.
    }
    setProfile(null);
    setRooms([]);
    setRoomView(null);
    setActiveRoomID(null);
    setSelectedMemberID(null);
    setModal(null);
    setRecovery(null);
    setRotateError(null);
    setAuthMode("login");
    setAuthBusy(false);
    setAuthExpired(true);
    setAuthNotice(null);
    setAuthError(
      message === "登录已过期，请重新登录。" ? null : message,
    );
    setTab("chats");
    tabRef.current = "chats";
    setPhase("auth");
  }, []);

  const handleBackgroundFailure = useCallback(
    (cause: unknown) => {
      const error = toSiteError(cause);
      if (
        error.code === "session_expired" ||
        error.code === "key_conflict" ||
        error.code === "invalid_state"
      ) {
        void expireSession(
          error.code === "session_expired"
            ? "登录已过期，请重新登录。"
            : error.message,
        );
        return;
      }
      setSyncError(error.message);
      if (isOfflineError(error)) {
        setOnline(false);
      }
    },
    [expireSession],
  );

  const failAction = useCallback(
    (cause: unknown): never => {
      const error = toSiteError(cause);
      if (
        error.code === "session_expired" ||
        error.code === "key_conflict" ||
        error.code === "invalid_state"
      ) {
        void expireSession(
          error.code === "session_expired"
            ? "登录已过期，请重新登录。"
            : error.message,
        );
      } else if (isOfflineError(error)) {
        setOnline(false);
        setSyncError(error.message);
      }
      throw error;
    },
    [expireSession],
  );

  const beginSync = useCallback(() => {
    syncDepth.current += 1;
    setSyncing(true);
  }, []);

  const endSync = useCallback(() => {
    syncDepth.current = Math.max(0, syncDepth.current - 1);
    if (syncDepth.current === 0) {
      setSyncing(false);
    }
  }, []);

  const markReadFor = useCallback(
    async (target: SiteClient, view: RoomView) => {
      if (
        document.hidden ||
        tabRef.current !== "chats" ||
        activeRoomRef.current !== view.summary.id
      ) {
        return;
      }
      const latest = target.room(view.summary.id);
      if (!latest) {
        return;
      }
      const seq = latest.summary.last_seq || 0;
      if (seq <= 0 || seq <= (latest.summary.read_seq || 0)) {
        return;
      }
      try {
        await target.call("markRead", {
          room_id: latest.summary.id,
          seq,
        });
        const updated = target.room(latest.summary.id);
        if (updated && activeRoomRef.current === latest.summary.id) {
          setRoomView(updated);
        }
        setRooms(target.rooms());
        setRevision((value) => value + 1);
        await persistState(target);
      } catch (cause) {
        const error = toSiteError(cause);
        if (
          error.code === "session_expired" ||
          error.code === "key_conflict"
        ) {
          void expireSession("登录已过期，请重新登录。");
        }
      }
    },
    [expireSession, persistState],
  );

  const refreshFromHost = useCallback(async () => {
    const target = clientRef.current;
    if (!target || !profileRef.current || refreshingRef.current) {
      return;
    }
    if (!navigator.onLine) {
      setOnline(false);
      setSyncError("网络未连接，消息会在恢复连接后同步。");
      return;
    }
    refreshingRef.current = true;
    beginSync();
    try {
      const result = await target.call<RefreshResult>("refreshRooms");
      const nextRooms = Array.isArray(result.rooms) ? result.rooms : [];
      setRooms(nextRooms);
      setRevision((value) => value + 1);
      // Flags such as email verification can change in another tab or via
      // the verification link; keep the visible profile in step with the
      // client state refreshed above.
      const nextProfile = target.profile();
      if (nextProfile) {
        profileRef.current = nextProfile;
        setProfile(nextProfile);
      }
      const active = activeRoomRef.current;
      if (active) {
        const nextView = target.room(active);
        if (!nextView) {
          activeRoomRef.current = null;
          setActiveRoomID(null);
          setRoomView(null);
          rememberActiveRoom(null);
        } else {
          setRoomView(nextView);
          await markReadFor(target, nextView);
        }
      }
      await persistState(target);
      setSyncError(null);
      setOnline(true);
      const warning = result.warnings?.[0];
      if (warning) {
        if (warning !== lastWarningRef.current) {
          lastWarningRef.current = warning;
          setToast(warning);
        }
      } else {
        lastWarningRef.current = null;
      }
    } catch (cause) {
      handleBackgroundFailure(cause);
    } finally {
      refreshingRef.current = false;
      endSync();
    }
  }, [
    beginSync,
    endSync,
    handleBackgroundFailure,
    markReadFor,
    persistState,
  ]);

  const applySession = useCallback((target: SiteClient, next: Profile) => {
    profileRef.current = next;
    setProfile(next);
    rememberAccount(next.account_id);
    setLastAccountID(next.account_id);
    const summaries = Array.isArray(target.rooms()) ? target.rooms() : [];
    setRooms(summaries);
    setRevision((value) => value + 1);
    const mobile = window.matchMedia(MOBILE_QUERY).matches;
    let pick: string | null = null;
    if (!mobile) {
      const remembered = readActiveRoom();
      pick = summaries.some((room) => room.id === remembered)
        ? remembered
        : summaries[0]?.id ?? null;
    }
    setActiveRoomID(pick);
    activeRoomRef.current = pick;
    setRoomView(pick ? target.room(pick) : null);
    setTab("chats");
    tabRef.current = "chats";
    setSelectedMemberID(null);
  }, []);

  const offerPendingInvite = useCallback(() => {
    const code = pendingInviteRef.current;
    if (!code) {
      return false;
    }
    pendingInviteRef.current = "";
    setHasPendingInvite(false);
    forgetPendingInvite();
    setInviteCode(code);
    setModal("join");
    return true;
  }, []);

  refreshRef.current = () => {
    void refreshFromHost();
  };

  useEffect(() => {
    profileRef.current = profile;
  }, [profile]);

  useEffect(() => {
    activeRoomRef.current = activeRoomID;
  }, [activeRoomID]);

  useEffect(() => {
    tabRef.current = tab;
  }, [tab]);

  useEffect(() => {
    // Remove the invite from the visible URL once it is safely remembered for
    // this tab. Reloading the sign-in page still resumes the invite.
    const hasHashInvite = window.location.hash.startsWith("#invite=");
    let hasQueryInvite = false;
    try {
      hasQueryInvite = new URLSearchParams(window.location.search).has("invite");
    } catch {
      hasQueryInvite = false;
    }
    if (!hasHashInvite && !hasQueryInvite) {
      return;
    }
    const url = new URL(window.location.href);
    if (hasHashInvite) {
      url.hash = "";
    }
    if (hasQueryInvite) {
      url.searchParams.delete("invite");
    }
    window.history.replaceState(null, "", url.pathname + url.search + url.hash);
  }, []);

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      const dark =
        theme === "dark" || (theme === "system" && media.matches);
      document.documentElement.dataset.theme = dark ? "dark" : "light";
      document.documentElement.style.colorScheme = dark ? "dark" : "light";
      document
        .querySelector('meta[name="theme-color"]')
        ?.setAttribute("content", dark ? "#09090b" : "#f8f8fa");
    };
    apply();
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [theme]);

  useEffect(() => {
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      // Theme is a convenience, not a requirement.
    }
  }, [theme]);

  useEffect(() => {
    if (!toast) {
      return;
    }
    const timer = window.setTimeout(() => setToast(null), 4200);
    return () => window.clearTimeout(timer);
  }, [toast]);

  useEffect(() => {
    let cancelled = false;
    setPhase("booting");
    setBootError(null);
    setBootProgress(0);

    void (async () => {
      try {
        const target = await loadSite((loaded, total) => {
          if (!cancelled) {
            setBootProgress(total > 0 ? Math.min(1, loaded / total) : 0);
          }
        });
        if (cancelled) {
          return;
        }
        clientRef.current = target;
        setClient(target);
        const vault = await loadVault();
        if (cancelled) {
          return;
        }
        if (vault.unavailable) {
          setVaultWarning(vault.unavailable);
          if (vault.corrupt) {
            // Explain why an apparently signed-in browser is asking for the
            // password again instead of dropping the user on a blank form.
            setAuthNotice(vault.unavailable);
          }
        }
        if (!vault.state) {
          setPhase("auth");
          return;
        }
        try {
          target.restore(vault.state);
        } catch (cause) {
          if (cancelled) {
            return;
          }
          const error = toSiteError(cause);
          try {
            target.logout();
            await clearVault();
          } catch {
            // An invalid vault is discarded on the next write anyway.
          }
          setAuthError(error.message);
          setPhase("auth");
          return;
        }
        try {
          const resumed = await target.call<Profile>("resume");
          if (cancelled) {
            return;
          }
          if (!resumed) {
            setPhase("auth");
            return;
          }
          applySession(target, resumed);
          setPhase("ready");
          offerPendingInvite();
          refreshRef.current();
        } catch (cause) {
          if (cancelled) {
            return;
          }
          const error = toSiteError(cause);
          if (
            error.code === "session_expired" ||
            error.code === "key_conflict" ||
            error.code === "invalid_state"
          ) {
            const accountID =
              target.profile()?.account_id || readLastAccount();
            if (accountID) {
              rememberAccount(accountID);
              setLastAccountID(accountID);
            }
            try {
              target.logout();
              await clearVault();
            } catch {
              // Sign-in already failed; the vault is stale.
            }
            setAuthExpired(true);
            setAuthError(
              error.code === "session_expired" ? null : error.message,
            );
            setPhase("auth");
            return;
          }
          const cached = target.profile();
          if (cached) {
            applySession(target, cached);
            setPhase("ready");
            setSyncError(error.message);
            if (isOfflineError(error)) {
              setOnline(false);
            }
            offerPendingInvite();
            return;
          }
          setAuthError(error.message);
          setPhase("auth");
        }
      } catch (cause) {
        if (!cancelled) {
          setBootError(toSiteError(cause).message);
          setPhase("fatal");
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    bootNonce,
    applySession,
    offerPendingInvite,
  ]);

  useEffect(() => {
    const onOnline = () => {
      setOnline(true);
      if (profileRef.current) {
        refreshRef.current();
      }
    };
    const onOffline = () => {
      setOnline(false);
      setSyncError("网络未连接，消息会在恢复连接后同步。");
    };
    const onVisible = () => {
      if (
        document.visibilityState === "visible" &&
        profileRef.current &&
        navigator.onLine
      ) {
        refreshRef.current();
      }
    };
    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    document.addEventListener("visibilitychange", onVisible);
    const timer = window.setInterval(() => {
      if (
        document.visibilityState === "visible" &&
        profileRef.current &&
        navigator.onLine
      ) {
        refreshRef.current();
      }
    }, 20_000);
    return () => {
      window.removeEventListener("online", onOnline);
      window.removeEventListener("offline", onOffline);
      document.removeEventListener("visibilitychange", onVisible);
      window.clearInterval(timer);
    };
  }, []);

  useEffect(() => {
    if (!selectedMemberID) {
      return;
    }
    const exists = rooms.some((room) => {
      const view = client?.room(room.id);
      return view?.members.some(
        (member) =>
          member.account_id === selectedMemberID &&
          member.status === "active",
      );
    });
    if (!exists) {
      setSelectedMemberID(null);
    }
  }, [client, rooms, revision, selectedMemberID]);

  const allRoomViews = useMemo(() => {
    if (!client) {
      return [];
    }
    return rooms
      .map((room) => client.room(room.id))
      .filter((view): view is RoomView => view !== null);
  }, [client, rooms, revision]);

  const members = useMemo(
    () => aggregateMembers(allRoomViews),
    [allRoomViews],
  );
  const selectedMember = useMemo(
    () =>
      selectedMemberID
        ? members.find((member) => member.accountID === selectedMemberID) ??
          null
        : null,
    [members, selectedMemberID],
  );
  const unread = useMemo(
    () => rooms.reduce((total, room) => total + Math.max(0, room.unread), 0),
    [rooms],
  );

  const selectTab = (next: Tab) => {
    setTab(next);
    tabRef.current = next;
    if (next === "members") {
      setSelectedMemberID(null);
    }
    if (next === "chats" && isMobile) {
      setActiveRoomID(null);
      activeRoomRef.current = null;
      setRoomView(null);
    }
  };

  const closeRoom = () => {
    setActiveRoomID(null);
    activeRoomRef.current = null;
    setRoomView(null);
  };

  const selectMember = (member: StoredMember) => {
    setTab("members");
    tabRef.current = "members";
    setSelectedMemberID(member.accountID);
  };

  const openRoom = useCallback(
    async (roomID: string) => {
      const target = clientRef.current;
      if (!target) {
        return;
      }
      setTab("chats");
      tabRef.current = "chats";
      setSelectedMemberID(null);
      setActiveRoomID(roomID);
      activeRoomRef.current = roomID;
      rememberActiveRoom(roomID);
      const cached = target.room(roomID);
      setRoomView(cached);
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      if (!navigator.onLine) {
        setOnline(false);
        setSyncError("网络未连接，正在显示这台设备上的消息。");
        return;
      }
      beginSync();
      try {
        const view = await target.call<RoomView>("syncRoom", {
          room_id: roomID,
        });
        setRooms(target.rooms());
        setRevision((value) => value + 1);
        if (activeRoomRef.current === roomID) {
          setRoomView(view);
          await markReadFor(target, view);
        }
        await persistState(target);
        setSyncError(null);
        setOnline(true);
      } catch (cause) {
        handleBackgroundFailure(cause);
      } finally {
        endSync();
      }
    },
    [
      beginSync,
      endSync,
      handleBackgroundFailure,
      markReadFor,
      persistState,
    ],
  );

  const requestRecovery = async (user: string) => {
    const target = clientRef.current;
    if (!target) {
      throw new SiteError("unavailable", "安全模块还没有准备好，请刷新页面。");
    }
    await target.call("requestRecovery", { user });
  };

  const handleAuth = async (values: AuthValues) => {
    const target = clientRef.current;
    if (!target) {
      setAuthError("安全模块还没有准备好，请刷新页面。");
      return;
    }
    setAuthBusy(true);
    setAuthError(null);
    setAuthNotice(null);
    try {
      let result: AuthResult;
      if (values.mode === "login") {
        result = await target.call<AuthResult>("login", {
          account_id: values.accountID,
          password: values.password,
          device_name: values.deviceName,
        });
      } else {
        result = await target.call<AuthResult>("register", {
          name: values.name,
          email: values.email,
          password: values.password,
          device_name: values.deviceName,
        });
      }
      if (!result.profile) {
        throw new SiteError("internal", "登录结果不完整，请重试。");
      }
      applySession(target, result.profile);
      setAuthExpired(false);
      setAuthError(null);
      setAuthNotice(null);
      setPhase("ready");
      await persistState(target);
      if (result.recovery) {
        setRecovery({ file: result.recovery, rotated: false });
      } else if (!offerPendingInvite() && result.email_sent) {
        setToast("验证邮件已发送，请查收邮箱。");
      }
      if (result.vault_error) {
        setToast(result.vault_error);
      }
      if (!result.recovery) {
        void refreshFromHost();
      }
    } catch (cause) {
      setAuthError(toSiteError(cause).message);
    } finally {
      setAuthBusy(false);
    }
  };

  const finishRecovery = () => {
    setRecovery(null);
    offerPendingInvite();
  };

  const sendMessage = async (text: string) => {
    const target = clientRef.current;
    const roomID = activeRoomRef.current;
    if (!target || !roomID) {
      return false;
    }
    setSendingRoom(roomID);
    try {
      await target.call("sendMessage", { room_id: roomID, text });
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      if (activeRoomRef.current === roomID) {
        setRoomView(target.room(roomID));
      }
      await persistState(target);
      setOnline(true);
      setSyncError(null);
      return true;
    } catch (cause) {
      const error = toSiteError(cause);
      if (
        error.code === "session_expired" ||
        error.code === "key_conflict" ||
        error.code === "invalid_state"
      ) {
        void expireSession(
          error.code === "session_expired"
            ? "登录已过期，请重新登录。"
            : error.message,
        );
      } else {
        setToast(error.message);
        if (isOfflineError(error)) {
          setOnline(false);
          setSyncError(error.message);
        }
      }
      return false;
    } finally {
      setSendingRoom((current) =>
        current === roomID ? null : current,
      );
    }
  };

  const createRoom = async (name: string, topic: string) => {
    const target = clientRef.current;
    if (!target) {
      throw new SiteError("internal", "安全模块还没有准备好。");
    }
    try {
      const view = await target.call<RoomView>("createRoom", {
        name,
        topic,
      });
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      setActiveRoomID(view.summary.id);
      activeRoomRef.current = view.summary.id;
      setRoomView(view);
      rememberActiveRoom(view.summary.id);
      setTab("chats");
      tabRef.current = "chats";
      setSelectedMemberID(null);
      await persistState(target);
      setModal(null);
      setToast("房间已创建。");
      setOnline(true);
      setSyncError(null);
      return true;
    } catch (cause) {
      return failAction(cause);
    }
  };

  const joinRoom = async (code: string) => {
    const target = clientRef.current;
    if (!target) {
      throw new SiteError("internal", "安全模块还没有准备好。");
    }
    try {
      const view = await target.call<RoomView>("joinInvite", { code });
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      setActiveRoomID(view.summary.id);
      activeRoomRef.current = view.summary.id;
      setRoomView(view);
      rememberActiveRoom(view.summary.id);
      setTab("chats");
      tabRef.current = "chats";
      setSelectedMemberID(null);
      await persistState(target);
      setModal(null);
      setInviteCode("");
      setToast(`已加入「${view.summary.name}」。`);
      setOnline(true);
      setSyncError(null);
      return true;
    } catch (cause) {
      return failAction(cause);
    }
  };

  const renameRoom = async (name: string, topic: string) => {
    const target = clientRef.current;
    const roomID = activeRoomRef.current;
    if (!target || !roomID) {
      throw new SiteError("room_not_found", "找不到这个房间。");
    }
    try {
      const view = await target.call<RoomView>("renameRoom", {
        room_id: roomID,
        name,
        topic,
      });
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      if (activeRoomRef.current === roomID) {
        setRoomView(view);
      }
      await persistState(target);
      setToast("房间信息已更新。");
      return true;
    } catch (cause) {
      return failAction(cause);
    }
  };

  const createInvite = async (
    history: "recent" | "all" | "none",
    ttlHours: number,
  ): Promise<InviteResult> => {
    const target = clientRef.current;
    const roomID = activeRoomRef.current;
    if (!target || !roomID) {
      throw new SiteError("room_not_found", "找不到这个房间。");
    }
    try {
      const result = await target.call<InviteResult>("createInvite", {
        room_id: roomID,
        history,
        ttl_hours: ttlHours,
      });
      setRevision((value) => value + 1);
      await persistState(target);
      return result;
    } catch (cause) {
      return failAction(cause);
    }
  };

  const leaveRoom = async () => {
    const target = clientRef.current;
    const roomID = activeRoomRef.current;
    if (!target || !roomID) {
      throw new SiteError("room_not_found", "找不到这个房间。");
    }
    try {
      await target.call("leaveRoom", { room_id: roomID });
      setRooms(target.rooms());
      setRevision((value) => value + 1);
      if (activeRoomRef.current === roomID) {
        activeRoomRef.current = null;
        setActiveRoomID(null);
        setRoomView(null);
        rememberActiveRoom(null);
      }
      await persistState(target);
      setModal(null);
      setToast("已离开房间。");
      return true;
    } catch (cause) {
      return failAction(cause);
    }
  };

  const rotateRecovery = async () => {
    const target = clientRef.current;
    if (!target) {
      return;
    }
    setRotateBusy(true);
    setRotateError(null);
    try {
      const file = await target.call<RecoveryFile>("rotateRecovery");
      setModal(null);
      setRecovery({ file, rotated: true });
      await persistState(target);
    } catch (cause) {
      const error = toSiteError(cause);
      if (
        error.code === "session_expired" ||
        error.code === "key_conflict" ||
        error.code === "invalid_state"
      ) {
        void expireSession(
          error.code === "session_expired"
            ? "登录已过期，请重新登录。"
            : error.message,
        );
      } else {
        setRotateError(error.message);
      }
    } finally {
      setRotateBusy(false);
    }
  };

  const logout = async () => {
    const target = clientRef.current;
    const accountID =
      profileRef.current?.account_id || readLastAccount();
    setLogoutBusy(true);
    try {
      target?.logout();
    } catch {
      // State is being discarded either way.
    }
    try {
      await clearVault();
    } catch {
      // Nothing more can be done for an unavailable vault.
    }
    if (accountID) {
      rememberAccount(accountID);
      setLastAccountID(accountID);
    }
    profileRef.current = null;
    activeRoomRef.current = null;
    setProfile(null);
    setRooms([]);
    setRoomView(null);
    setActiveRoomID(null);
    setSelectedMemberID(null);
    setTab("chats");
    tabRef.current = "chats";
    setModal(null);
    setRecovery(null);
    setSyncError(null);
    setAuthMode("login");
    setAuthExpired(false);
    setAuthError(null);
    setAuthNotice("已退出登录。");
    setLogoutBusy(false);
    setPhase("auth");
  };

  const resendVerification = async () => {
    const target = clientRef.current;
    if (!target) {
      throw new SiteError("internal", "安全模块还没有准备好。");
    }
    try {
      await target.call("resendVerification");
    } catch (cause) {
      return failAction(cause);
    }
  };

  if (phase === "booting") {
    return <BootScreen progress={bootProgress} />;
  }

  if (phase === "fatal") {
    return (
      <FatalScreen
        message={bootError || "页面加载失败。"}
        onRetry={() => setBootNonce((value) => value + 1)}
      />
    );
  }

  if (phase === "auth" || !profile) {
    return (
      <>
        <AuthScreen
          mode={authMode}
          initialAccountID={lastAccountID}
          busy={authBusy}
          error={authError}
          notice={
            hasPendingInvite
              ? "收到一个房间邀请，登录后会继续。"
              : authNotice
          }
          expired={authExpired}
          onMode={(mode) => {
            setAuthMode(mode);
            setAuthError(null);
            setAuthNotice(null);
            if (mode === "register") {
              setAuthExpired(false);
            }
          }}
          onSubmit={(values) => void handleAuth(values)}
          onForgotPassword={() => setModal("recover")}
          onUseOtherAccount={() => {
            try {
              localStorage.removeItem(LAST_ACCOUNT_KEY);
            } catch {
              // Ignore unavailable storage.
            }
            setLastAccountID("");
            setAuthExpired(false);
            setAuthError(null);
            setAuthNotice(null);
          }}
        />
        {modal === "recover" && (
          <RecoverAccountDialog
            isMobile={isMobile}
            initialUser={lastAccountID}
            onClose={() => setModal(null)}
            onRequest={requestRecovery}
          />
        )}
        {recovery && (
          <RecoveryDialog
            isMobile={isMobile}
            file={recovery.file}
            rotated={recovery.rotated}
            onDone={finishRecovery}
          />
        )}
      </>
    );
  }

  const showChat = tab === "chats";
  const showMembers = tab === "members";
  const hideMobileNav =
    isMobile &&
    ((showChat && !!roomView) || (showMembers && !!selectedMember));

  let content: JSX.Element;
  if (showChat) {
    if (isMobile) {
      content = roomView ? (
        <ChatView
          room={roomView}
          profile={profile}
          isMobile
          sending={sendingRoom === roomView.summary.id}
          syncing={syncing}
          onBack={closeRoom}
          onSend={sendMessage}
          onOpenInfo={() => setModal("roomInfo")}
          onRetrySync={() => void refreshFromHost()}
        />
      ) : (
        <MobileChatsPage
          rooms={rooms}
          unread={unread}
          onCreate={() => setModal("create")}
          onJoin={() => {
            setInviteCode("");
            setModal("join");
          }}
          onOpen={openRoom}
        />
      );
    } else {
      content = roomView ? (
        <ChatView
          room={roomView}
          profile={profile}
          isMobile={false}
          sending={sendingRoom === roomView.summary.id}
          syncing={syncing}
          onSend={sendMessage}
          onOpenInfo={() => setModal("roomInfo")}
          onRetrySync={() => void refreshFromHost()}
        />
      ) : (
        <EmptyChat
          hasRooms={rooms.length > 0}
          onCreate={() => setModal("create")}
          onJoin={() => {
            setInviteCode("");
            setModal("join");
          }}
        />
      );
    }
  } else if (showMembers) {
    if (isMobile) {
      content = selectedMember ? (
        <MemberDetail
          member={selectedMember}
          profile={profile}
          isMobile
          onBack={() => setSelectedMemberID(null)}
          onOpenRoom={openRoom}
        />
      ) : (
        <MembersPage
          members={members}
          profile={profile}
          roomCount={rooms.length}
          selectedID={selectedMemberID}
          onSelect={selectMember}
        />
      );
    } else {
      content = selectedMember ? (
        <MemberDetail
          member={selectedMember}
          profile={profile}
          isMobile={false}
          onBack={() => setSelectedMemberID(null)}
          onOpenRoom={openRoom}
        />
      ) : (
        <MembersOverview
          members={members}
          profile={profile}
          roomCount={rooms.length}
          onSelect={selectMember}
        />
      );
    }
  } else {
    content = (
      <MePage
        profile={profile}
        theme={theme}
        isMobile={isMobile}
        online={online}
        syncing={syncing}
        vaultWarning={vaultWarning}
        onTheme={setTheme}
        onResendVerification={resendVerification}
        onRotateRecovery={() => {
          setRotateError(null);
          setModal("rotate");
        }}
        onRefresh={() => void refreshFromHost()}
        onLogout={() => setModal("logout")}
      />
    );
  }

  const banner = !online
    ? "网络未连接，消息会在恢复连接后同步。"
    : syncError || vaultWarning;

  return (
    <div className={`app-shell ${isMobile ? "is-mobile" : "is-desktop"}`}>
      {!isMobile && (
        <Sidebar
          profile={profile}
          rooms={rooms}
          members={members}
          tab={tab}
          activeRoomID={activeRoomID}
          selectedMemberID={selectedMemberID}
          onTab={selectTab}
          onOpenRoom={openRoom}
          onSelectMember={selectMember}
          onCreate={() => setModal("create")}
          onJoin={() => {
            setInviteCode("");
            setModal("join");
          }}
          onMe={() => selectTab("me")}
        />
      )}

      <main className="main">
        {banner && (
          <div
            className={`connection-banner ${
              online && !syncError ? "warning" : ""
            }`}
            role="status"
          >
            <Icon name={online ? "info" : "wifiOff"} size={15} />
            <span>{banner}</span>
            {(!online || syncError) && (
              <button
                type="button"
                onClick={() => void refreshFromHost()}
                disabled={syncing}
              >
                {syncing ? "同步中" : "重试"}
              </button>
            )}
          </div>
        )}
        {content}
      </main>

      {isMobile && !hideMobileNav && (
        <TabBar tab={tab} unread={unread} onTab={selectTab} />
      )}

      {toast && (
        <div className="toast" role="status">
          {toast}
        </div>
      )}

      {modal === "create" && (
        <CreateRoomDialog
          isMobile={isMobile}
          onClose={() => setModal(null)}
          onCreate={createRoom}
        />
      )}

      {modal === "join" && (
        <JoinRoomDialog
          isMobile={isMobile}
          initialCode={inviteCode}
          onClose={() => {
            setModal(null);
            setInviteCode("");
          }}
          onJoin={joinRoom}
        />
      )}

      {modal === "roomInfo" && roomView && (
        <RoomInfoDialog
          isMobile={isMobile}
          room={roomView}
          profile={profile}
          onClose={() => setModal(null)}
          onRename={renameRoom}
          onLeave={leaveRoom}
          onCreateInvite={createInvite}
        />
      )}

      {modal === "logout" && (
        <LogoutDialog
          isMobile={isMobile}
          busy={logoutBusy}
          onClose={() => setModal(null)}
          onLogout={() => void logout()}
        />
      )}

      {modal === "rotate" && (
        <ConfirmDialog
          isMobile={isMobile}
          title="生成新的恢复文件"
          message={
            <>
              现在有效的恢复文件会<strong>立即失效</strong>，新的文件只会显示一次。
              请准备好保存它。
            </>
          }
          confirmLabel="生成"
          busy={rotateBusy}
          error={rotateError}
          onClose={() => {
            if (!rotateBusy) {
              setModal(null);
              setRotateError(null);
            }
          }}
          onConfirm={() => void rotateRecovery()}
        />
      )}

      {recovery && (
        <RecoveryDialog
          isMobile={isMobile}
          file={recovery.file}
          rotated={recovery.rotated}
          onDone={finishRecovery}
        />
      )}
    </div>
  );
}

function BootScreen({ progress }: { progress: number }) {
  const percent = Math.round(progress * 100);
  return (
    <div className="boot-screen">
      <div className="boot-glow" />
      <div className="boot-brand">
        <BrandMark size={58} />
      </div>
      <h1>Toget</h1>
      <p>正在准备端到端加密</p>
      <div className="boot-progress" aria-hidden="true">
        <i style={{ width: `${percent}%` }} />
      </div>
      <span className="boot-hint">
        {percent > 0 ? `${percent}%` : "首次打开需要下载安全模块"}
      </span>
    </div>
  );
}

function FatalScreen({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <div className="boot-screen fatal-screen">
      <div className="boot-brand error">
        <BrandMark size={52} />
      </div>
      <h1>打不开页面</h1>
      <p>{message}</p>
      <button type="button" className="primary-button" onClick={onRetry}>
        <Icon name="refresh" size={16} />
        重试
      </button>
    </div>
  );
}

function Sidebar({
  profile,
  rooms,
  members,
  tab,
  activeRoomID,
  selectedMemberID,
  onTab,
  onOpenRoom,
  onSelectMember,
  onCreate,
  onJoin,
  onMe,
}: {
  profile: Profile;
  rooms: RoomSummary[];
  members: StoredMember[];
  tab: Tab;
  activeRoomID: string | null;
  selectedMemberID: string | null;
  onTab: (tab: Tab) => void;
  onOpenRoom: (roomID: string) => void;
  onSelectMember: (member: StoredMember) => void;
  onCreate: () => void;
  onJoin: () => void;
  onMe: () => void;
}) {
  return (
    <aside className="sidebar">
      <div className="brand-row">
        <div className="brand">
          <BrandMark size={30} />
          <span>Toget</span>
        </div>
        <button
          className="icon-button"
          type="button"
          aria-label="新房间"
          onClick={onCreate}
        >
          <Icon name="compose" size={19} />
        </button>
      </div>

      <div className="side-tabs" aria-label="侧边栏导航">
        <button
          type="button"
          className={tab === "chats" ? "active" : ""}
          aria-pressed={tab === "chats"}
          onClick={() => onTab("chats")}
        >
          消息
        </button>
        <button
          type="button"
          className={tab === "members" ? "active" : ""}
          aria-pressed={tab === "members"}
          onClick={() => onTab("members")}
        >
          成员
        </button>
      </div>

      {tab === "members" ? (
        <MembersList
          members={members}
          profile={profile}
          selectedID={selectedMemberID}
          onSelect={onSelectMember}
          compact
        />
      ) : (
        <RoomList
          rooms={rooms}
          activeRoomID={activeRoomID}
          onOpen={onOpenRoom}
          onCreate={onCreate}
          onJoin={onJoin}
        />
      )}

      <button
        type="button"
        className={`side-profile ${tab === "me" ? "active" : ""}`}
        onClick={onMe}
      >
        <Avatar
          spec={profileAvatar(profile.account_id, profile.name)}
          size={32}
        />
        <span className="side-profile-copy">
          <strong>{profile.name}</strong>
          <small>{profile.device_name}</small>
        </span>
        <Icon name="chevronRight" size={16} className="side-profile-chevron" />
      </button>
    </aside>
  );
}

function RoomList({
  rooms,
  activeRoomID,
  onOpen,
  onCreate,
  onJoin,
  mobile = false,
}: {
  rooms: RoomSummary[];
  activeRoomID: string | null;
  onOpen: (roomID: string) => void;
  onCreate: () => void;
  onJoin: () => void;
  mobile?: boolean;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? rooms.filter((room) =>
        `${room.name} ${room.topic ?? ""} ${room.last_text ?? ""}`
          .toLocaleLowerCase("zh-CN")
          .includes(needle),
      )
    : rooms;

  return (
    <div className={`list-wrap ${mobile ? "mobile-list" : ""}`}>
      <SearchBox
        value={query}
        onChange={setQuery}
        placeholder="搜索房间"
      />
      {!mobile && (
        <div className="list-tools">
          <button type="button" onClick={onCreate}>
            <Icon name="plus" size={14} />
            新房间
          </button>
          <button type="button" onClick={onJoin}>
            <Icon name="link" size={14} />
            加入
          </button>
        </div>
      )}
      <div className="chat-list">
        {visible.map((room) => (
          <button
            type="button"
            key={room.id}
            className={`chat-row ${
              activeRoomID === room.id ? "active" : ""
            }`}
            onClick={() => onOpen(room.id)}
          >
            <Avatar
              spec={roomAvatar(room.id, room.name)}
              size={mobile ? 50 : 46}
            />
            <span className="chat-row-copy">
              <span className="chat-row-top">
                <strong>{room.name}</strong>
                <span className="chat-row-time">
                  {listTime(room.last_at ?? room.joined_at)}
                </span>
              </span>
              <span className="chat-row-bottom">
                <span className="chat-row-preview">
                  {roomPreview(room)}
                </span>
                {room.unread > 0 && (
                  <span
                    className="unread"
                    aria-label={`${room.unread} 条未读`}
                  >
                    {room.unread > 99 ? "99+" : room.unread}
                  </span>
                )}
              </span>
            </span>
          </button>
        ))}
        {visible.length === 0 && rooms.length > 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>没有找到</span>
          </div>
        )}
        {rooms.length === 0 && (
          <div className="room-empty">
            <BrandMark size={44} />
            <strong>还没有房间</strong>
            <p>创建一个房间，或者用邀请码加入。</p>
            <div className="empty-actions">
              <button
                type="button"
                className="primary-button small"
                onClick={onCreate}
              >
                创建房间
              </button>
              <button
                type="button"
                className="secondary-button small"
                onClick={onJoin}
              >
                加入房间
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function MobileChatsPage({
  rooms,
  unread,
  onOpen,
  onCreate,
  onJoin,
}: {
  rooms: RoomSummary[];
  unread: number;
  onOpen: (roomID: string) => void;
  onCreate: () => void;
  onJoin: () => void;
}) {
  return (
    <div className="page mobile-page">
      <div className="mobile-head">
        <div className="mobile-heading">
          <BrandMark size={25} />
          <h1>消息</h1>
        </div>
        <div className="mobile-head-actions">
          <button
            className="icon-button"
            type="button"
            aria-label="加入房间"
            onClick={onJoin}
          >
            <Icon name="link" size={18} />
          </button>
          <button
            className="icon-button"
            type="button"
            aria-label="新房间"
            onClick={onCreate}
          >
            <Icon name="plus" size={19} />
          </button>
        </div>
      </div>
      {unread > 0 && (
        <div className="mobile-unread-line">{unread} 条未读消息</div>
      )}
      <RoomList
        rooms={rooms}
        activeRoomID={null}
        onOpen={onOpen}
        onCreate={onCreate}
        onJoin={onJoin}
        mobile
      />
    </div>
  );
}

function EmptyChat({
  hasRooms,
  onCreate,
  onJoin,
}: {
  hasRooms: boolean;
  onCreate: () => void;
  onJoin: () => void;
}) {
  return (
    <div className="empty-chat">
      <BrandMark size={54} />
      <p>{hasRooms ? "选择一个房间" : "还没有房间"}</p>
      {!hasRooms && (
        <div className="empty-actions">
          <button
            type="button"
            className="primary-button small"
            onClick={onCreate}
          >
            创建房间
          </button>
          <button
            type="button"
            className="secondary-button small"
            onClick={onJoin}
          >
            加入房间
          </button>
        </div>
      )}
    </div>
  );
}

function TabBar({
  tab,
  unread,
  onTab,
}: {
  tab: Tab;
  unread: number;
  onTab: (tab: Tab) => void;
}) {
  const items: { id: Tab; label: string; icon: IconName }[] = [
    { id: "chats", label: "消息", icon: "chats" },
    { id: "members", label: "成员", icon: "contacts" },
    { id: "me", label: "我", icon: "person" },
  ];
  return (
    <nav className="tabbar" aria-label="主导航">
      {items.map((item) => (
        <button
          type="button"
          key={item.id}
          className={`tabbar-item ${tab === item.id ? "active" : ""}`}
          aria-current={tab === item.id ? "page" : undefined}
          aria-label={
            item.id === "chats" && unread > 0
              ? `${item.label}，${unread > 99 ? "99+" : unread} 条未读`
              : item.label
          }
          onClick={() => onTab(item.id)}
        >
          <span className="tabbar-icon">
            <Icon name={item.icon} size={23} />
            {item.id === "chats" && unread > 0 && (
              <span className="tabbar-badge">
                {unread > 99 ? "99+" : unread}
              </span>
            )}
          </span>
          <span>{item.label}</span>
        </button>
      ))}
    </nav>
  );
}
