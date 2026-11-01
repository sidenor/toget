import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  FormEvent,
  KeyboardEvent,
  PointerEvent as ReactPointerEvent,
  ReactNode,
} from "react";
import { contacts, initialChats, me, people } from "./data";
import type { Chat, Message, Person, Tab, Theme } from "./types";
import { Avatar, AvatarStack, BrandMark, Icon } from "./ui";
import type { IconName } from "./ui";

const MOBILE_QUERY = "(max-width: 900px)";
const CHATS_KEY = "toget.site.chats.v1";
const THEME_KEY = "toget.site.theme";
const EMOJIS = ["😊", "👍", "✨", "❤️", "😂", "🎉", "🙏", "🔥"];
const REACTIONS = ["❤️", "👍", "✨"];

function isChat(value: unknown): value is Chat {
  if (!value || typeof value !== "object") {
    return false;
  }
  const chat = value as Partial<Chat>;
  return (
    typeof chat.id === "string" &&
    typeof chat.name === "string" &&
    (chat.kind === "group" || chat.kind === "dm" || chat.kind === "agent") &&
    Array.isArray(chat.messages) &&
    !!chat.avatar
  );
}

function loadChats(): Chat[] {
  try {
    const raw = localStorage.getItem(CHATS_KEY);
    if (!raw) {
      return initialChats;
    }
    const parsed: unknown = JSON.parse(raw);
    if (Array.isArray(parsed) && parsed.every(isChat)) {
      return parsed as Chat[];
    }
  } catch {
    // A blocked or malformed localStorage should never keep the app from opening.
  }
  return initialChats;
}

function loadTheme(): Theme {
  try {
    const stored = localStorage.getItem(THEME_KEY);
    if (stored === "light" || stored === "dark" || stored === "system") {
      return stored;
    }
  } catch {
    // Ignore storage failures and follow the system theme.
  }
  return "system";
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

function uid(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

function dayKey(iso: string): string {
  const date = new Date(iso);
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
}

function clock(iso: string): string {
  const date = new Date(iso);
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

function minutesBetween(later: string, earlier: string): number {
  return (new Date(later).getTime() - new Date(earlier).getTime()) / 60_000;
}

function sameDate(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function dayLabel(iso: string): string {
  const date = new Date(iso);
  const now = new Date();
  if (sameDate(date, now)) {
    return "今天";
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDate(date, yesterday)) {
    return "昨天";
  }
  if (date.getFullYear() !== now.getFullYear()) {
    return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
  }
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}

function listTime(iso?: string): string {
  if (!iso) {
    return "";
  }
  const date = new Date(iso);
  const now = new Date();
  if (sameDate(date, now)) {
    return clock(iso);
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDate(date, yesterday)) {
    return "昨天";
  }
  const elapsed = now.getTime() - date.getTime();
  if (elapsed >= 0 && elapsed < 6 * 24 * 60 * 60 * 1000) {
    return ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][date.getDay()];
  }
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

function lastMessage(chat: Chat): Message | undefined {
  return chat.messages[chat.messages.length - 1];
}

function chatTimestamp(chat: Chat): string | undefined {
  return lastMessage(chat)?.at ?? chat.createdAt;
}

function chatTime(chat: Chat): number {
  const stamp = chatTimestamp(chat);
  return stamp ? new Date(stamp).getTime() : 0;
}

function chatPreview(chat: Chat): string {
  const message = lastMessage(chat);
  if (!message) {
    return "还没有消息";
  }
  let text = message.text ?? "";
  if (message.kind === "file") {
    text = message.file ? `[文件] ${message.file.name}` : "[文件]";
  }
  if (message.kind === "event" || message.from === "me" || chat.kind === "dm") {
    return text;
  }
  const sender = people[message.from];
  return sender ? `${sender.name}: ${text}` : text;
}

function chatSubtitle(chat: Chat): string {
  if (chat.kind === "group") {
    return `${chat.members.length} 位成员`;
  }
  if (chat.kind === "agent") {
    return "随时在";
  }
  const other = chat.members.find((id) => id !== "me");
  return other ? people[other]?.status ?? "在线" : "在线";
}

function sortChats(chats: Chat[]): Chat[] {
  return [...chats].sort((a, b) => {
    if (!!a.pinned !== !!b.pinned) {
      return a.pinned ? -1 : 1;
    }
    return chatTime(b) - chatTime(a);
  });
}

export default function App() {
  const isMobile = useMediaQuery(MOBILE_QUERY);
  const [chats, setChats] = useState<Chat[]>(loadChats);
  const [activeId, setActiveId] = useState<string | null>(() => {
    if (typeof window === "undefined" || window.matchMedia(MOBILE_QUERY).matches) {
      return null;
    }
    return (chats.find((chat) => chat.pinned) ?? chats[0])?.id ?? null;
  });
  const [tab, setTab] = useState<Tab>("chats");
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [newChatOpen, setNewChatOpen] = useState(false);

  const activeChat = useMemo(
    () => chats.find((chat) => chat.id === activeId) ?? null,
    [chats, activeId],
  );
  const sorted = useMemo(() => sortChats(chats), [chats]);
  const unread = useMemo(
    () => chats.reduce((total, chat) => total + Math.max(0, chat.unread), 0),
    [chats],
  );

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      const dark = theme === "dark" || (theme === "system" && media.matches);
      document.documentElement.dataset.theme = dark ? "dark" : "light";
      document.documentElement.style.colorScheme = dark ? "dark" : "light";
    };
    apply();
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      // The chosen theme still applies for this visit.
    }
    if (theme !== "system") {
      return;
    }
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [theme]);

  useEffect(() => {
    try {
      localStorage.setItem(CHATS_KEY, JSON.stringify(chats));
    } catch {
      // Keep the session usable even when persistence is unavailable.
    }
  }, [chats]);

  useEffect(() => {
    if (!isMobile && activeId === null && sorted.length > 0) {
      setActiveId(sorted[0].id);
    }
  }, [activeId, isMobile, sorted]);

  useEffect(() => {
    if (isMobile && tab !== "chats") {
      setActiveId(null);
    }
  }, [isMobile, tab]);

  useEffect(() => {
    let title = "Toget";
    if (activeChat && (!isMobile || tab === "chats")) {
      title = `${activeChat.name} · Toget`;
    } else if (tab === "me") {
      title = "我 · Toget";
    } else if (tab === "contacts") {
      title = "通讯录 · Toget";
    } else if (tab === "chats") {
      title = "消息 · Toget";
    }
    document.title = title;
  }, [activeChat, isMobile, tab]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setChats((previous) =>
        previous.map((chat) => {
          if (!chat.messages.some((message) => message.from === "me" && !message.read)) {
            return chat;
          }
          return {
            ...chat,
            messages: chat.messages.map((message) =>
              message.from === "me" && !message.read ? { ...message, read: true } : message,
            ),
          };
        }),
      );
    }, 2200);
    return () => window.clearTimeout(timer);
  }, []);

  const openChat = useCallback((id: string) => {
    setActiveId(id);
    setTab("chats");
    setNewChatOpen(false);
    setChats((previous) =>
      previous.map((chat) =>
        chat.id === id && chat.unread > 0 ? { ...chat, unread: 0 } : chat,
      ),
    );
  }, []);

  const openPerson = useCallback(
    (personId: string) => {
      const existing = chats.find(
        (chat) =>
          chat.kind === "dm" &&
          chat.members.length === 2 &&
          chat.members.includes(personId),
      );
      if (existing) {
        openChat(existing.id);
        return;
      }
      const person = people[personId];
      if (!person) {
        return;
      }
      const chat: Chat = {
        id: `dm-${personId}`,
        name: person.name,
        avatar: person.avatar,
        kind: "dm",
        members: ["me", personId],
        unread: 0,
        messages: [],
        createdAt: new Date().toISOString(),
      };
      setChats((previous) => [chat, ...previous]);
      openChat(chat.id);
    },
    [chats, openChat],
  );

  const selectTab = useCallback(
    (next: Tab) => {
      setTab(next);
      setNewChatOpen(false);
      if (isMobile) {
        setActiveId(null);
      }
    },
    [isMobile],
  );

  const sendMessage = useCallback((chatId: string, text: string) => {
    const body = text.trim();
    if (!body) {
      return;
    }
    const id = uid("m");
    const at = new Date().toISOString();
    setChats((previous) =>
      previous.map((chat) =>
        chat.id === chatId
          ? {
              ...chat,
              unread: 0,
              messages: [
                ...chat.messages,
                { id, from: "me", text: body, at, read: false },
              ],
            }
          : chat,
      ),
    );
    window.setTimeout(() => {
      setChats((previous) =>
        previous.map((chat) =>
          chat.id === chatId
            ? {
                ...chat,
                messages: chat.messages.map((message) =>
                  message.id === id ? { ...message, read: true } : message,
                ),
              }
            : chat,
        ),
      );
    }, 1500);
  }, []);

  const toggleReaction = useCallback((chatId: string, messageId: string, emoji: string) => {
    setChats((previous) =>
      previous.map((chat) => {
        if (chat.id !== chatId) {
          return chat;
        }
        return {
          ...chat,
          messages: chat.messages.map((message) => {
            if (message.id !== messageId) {
              return message;
            }
            const reactions = { ...(message.reactions ?? {}) };
            const count = reactions[emoji] ?? 0;
            if (count > 1) {
              reactions[emoji] = count - 1;
            } else if (count === 1) {
              delete reactions[emoji];
            } else {
              reactions[emoji] = 1;
            }
            return { ...message, reactions };
          }),
        };
      }),
    );
  }, []);

  const closeNewChat = useCallback(() => setNewChatOpen(false), []);

  let content: ReactNode;
  if (isMobile) {
    if (activeChat) {
      content = (
        <ChatView
          chat={activeChat}
          isMobile
          onBack={() => {
            setActiveId(null);
            setTab("chats");
          }}
          onSend={sendMessage}
          onReact={toggleReaction}
        />
      );
    } else if (tab === "contacts") {
      content = <MobileContactsPage onOpen={openPerson} />;
    } else if (tab === "me") {
      content = <MePage isMobile theme={theme} onTheme={setTheme} />;
    } else {
      content = (
        <MobileChatsPage
          chats={sorted}
          onOpen={openChat}
          onCompose={() => setNewChatOpen(true)}
        />
      );
    }
  } else if (tab === "me") {
    content = <MePage isMobile={false} theme={theme} onTheme={setTheme} />;
  } else if (activeChat) {
    content = (
      <ChatView
        chat={activeChat}
        isMobile={false}
        onSend={sendMessage}
        onReact={toggleReaction}
      />
    );
  } else {
    content = <EmptyChat />;
  }

  return (
    <div className={`app-shell ${isMobile ? "is-mobile" : "is-desktop"}`}>
      {!isMobile && (
        <Sidebar
          chats={sorted}
          activeId={activeId}
          tab={tab}
          onTab={selectTab}
          onOpenChat={openChat}
          onOpenPerson={openPerson}
          onCompose={() => setNewChatOpen(true)}
          onMe={() => selectTab("me")}
        />
      )}
      <main className="main">{content}</main>
      {isMobile && !activeChat && (
        <TabBar tab={tab} unread={unread} onTab={selectTab} />
      )}
      <NewChatSheet
        isMobile={isMobile}
        open={newChatOpen}
        onClose={closeNewChat}
        onPick={openPerson}
      />
    </div>
  );
}

function Sidebar({
  chats,
  activeId,
  tab,
  onTab,
  onOpenChat,
  onOpenPerson,
  onCompose,
  onMe,
}: {
  chats: Chat[];
  activeId: string | null;
  tab: Tab;
  onTab: (tab: Tab) => void;
  onOpenChat: (id: string) => void;
  onOpenPerson: (id: string) => void;
  onCompose: () => void;
  onMe: () => void;
}) {
  return (
    <aside className="sidebar">
      <div className="brand-row">
        <div className="brand">
          <BrandMark size={30} />
          <span>Toget</span>
        </div>
        <button className="icon-button" type="button" aria-label="新对话" onClick={onCompose}>
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
          className={tab === "contacts" ? "active" : ""}
          aria-pressed={tab === "contacts"}
          onClick={() => onTab("contacts")}
        >
          通讯录
        </button>
      </div>

      {tab === "contacts" ? (
        <ContactList onOpen={onOpenPerson} />
      ) : (
        <ChatList chats={chats} activeId={activeId} onOpen={onOpenChat} />
      )}

      <button
        type="button"
        className={`side-profile ${tab === "me" ? "active" : ""}`}
        onClick={onMe}
      >
        <Avatar spec={me.avatar} size={32} presence />
        <span className="side-profile-copy">
          <strong>{me.name}</strong>
          <small>{me.handle}</small>
        </span>
        <Icon name="chevronRight" size={16} className="side-profile-chevron" />
      </button>
    </aside>
  );
}

function SearchBox({
  value,
  onChange,
  placeholder = "搜索",
  autoFocus = false,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  autoFocus?: boolean;
}) {
  return (
    <label className="search-box">
      <Icon name="search" size={17} />
      <input
        autoFocus={autoFocus}
        type="text"
        inputMode="search"
        enterKeyHint="search"
        value={value}
        placeholder={placeholder}
        aria-label={placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
      {value && (
        <button
          type="button"
          className="search-clear"
          aria-label="清除搜索"
          onClick={() => onChange("")}
        >
          <Icon name="close" size={13} />
        </button>
      )}
    </label>
  );
}

function ChatList({
  chats,
  activeId,
  onOpen,
  mobile = false,
}: {
  chats: Chat[];
  activeId: string | null;
  onOpen: (id: string) => void;
  mobile?: boolean;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? chats.filter((chat) => {
        const haystack = `${chat.name} ${chatPreview(chat)}`.toLocaleLowerCase("zh-CN");
        return haystack.includes(needle);
      })
    : chats;

  return (
    <div className={`list-wrap ${mobile ? "mobile-list" : ""}`}>
      <SearchBox value={query} onChange={setQuery} />
      <div className="chat-list">
        {visible.map((chat) => (
          <button
            type="button"
            key={chat.id}
            className={`chat-row ${activeId === chat.id ? "active" : ""}`}
            onClick={() => onOpen(chat.id)}
          >
            <Avatar spec={chat.avatar} size={mobile ? 50 : 46} />
            <span className="chat-row-copy">
              <span className="chat-row-top">
                <strong>{chat.name}</strong>
                <span className="chat-row-time">{listTime(chatTimestamp(chat))}</span>
              </span>
              <span className="chat-row-bottom">
                <span className="chat-row-preview">{chatPreview(chat)}</span>
                {chat.unread > 0 && (
                  <span className="unread" aria-label={`${chat.unread} 条未读`}>
                    {chat.unread > 99 ? "99+" : chat.unread}
                  </span>
                )}
              </span>
            </span>
          </button>
        ))}
        {visible.length === 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>没有找到</span>
          </div>
        )}
      </div>
    </div>
  );
}

function PersonRow({
  person,
  onOpen,
}: {
  person: Person;
  onOpen: (id: string) => void;
}) {
  return (
    <button type="button" className="person-row" onClick={() => onOpen(person.id)}>
      <Avatar spec={person.avatar} size={40} />
      <span className="person-copy">
        <span className="person-name">
          {person.name}
          {person.agent && <Icon name="sparkle" size={11} className="person-spark" />}
        </span>
        <span className="person-status">{person.status}</span>
      </span>
      <Icon name="chevronRight" size={15} className="person-chevron" />
    </button>
  );
}

function ContactList({
  onOpen,
  mobile = false,
}: {
  onOpen: (id: string) => void;
  mobile?: boolean;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? contacts.filter((person) =>
        `${person.name} ${person.handle}`
          .toLocaleLowerCase("zh-CN")
          .includes(needle),
      )
    : contacts;
  const assistants = visible.filter((person) => person.agent);
  const friends = visible.filter((person) => !person.agent);

  return (
    <div className={`list-wrap ${mobile ? "mobile-list" : ""}`}>
      <SearchBox value={query} onChange={setQuery} placeholder="搜索联系人" />
      <div className="contact-list">
        {assistants.length > 0 && (
          <div className="contact-section">
            <div className="section-caption">助手</div>
            {assistants.map((person) => (
              <PersonRow key={person.id} person={person} onOpen={onOpen} />
            ))}
          </div>
        )}
        {friends.length > 0 && (
          <div className="contact-section">
            <div className="section-caption">朋友</div>
            {friends.map((person) => (
              <PersonRow key={person.id} person={person} onOpen={onOpen} />
            ))}
          </div>
        )}
        {visible.length === 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>没有找到</span>
          </div>
        )}
      </div>
    </div>
  );
}

function MobileHeading({
  title,
  action,
}: {
  title: string;
  action?: ReactNode;
}) {
  return (
    <div className="mobile-head">
      <div className="mobile-heading">
        <BrandMark size={25} />
        <h1>{title}</h1>
      </div>
      {action}
    </div>
  );
}

function MobileChatsPage({
  chats,
  onOpen,
  onCompose,
}: {
  chats: Chat[];
  onOpen: (id: string) => void;
  onCompose: () => void;
}) {
  return (
    <div className="page mobile-page">
      <MobileHeading
        title="消息"
        action={
          <button className="icon-button" type="button" aria-label="新对话" onClick={onCompose}>
            <Icon name="compose" size={19} />
          </button>
        }
      />
      <ChatList chats={chats} activeId={null} onOpen={onOpen} mobile />
    </div>
  );
}

function MobileContactsPage({ onOpen }: { onOpen: (id: string) => void }) {
  return (
    <div className="page mobile-page">
      <MobileHeading title="通讯录" />
      <ContactList onOpen={onOpen} mobile />
    </div>
  );
}

function ChatView({
  chat,
  isMobile,
  onBack,
  onSend,
  onReact,
}: {
  chat: Chat;
  isMobile: boolean;
  onBack?: () => void;
  onSend: (chatId: string, text: string) => void;
  onReact: (chatId: string, messageId: string, emoji: string) => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const press = useRef<{ timer: number; x: number; y: number } | null>(null);
  const [pickerFor, setPickerFor] = useState<string | null>(null);
  const memberAvatars = chat.members
    .filter((id) => id !== "me")
    .flatMap((id) => (people[id] ? [people[id].avatar] : []));
  const last = lastMessage(chat);
  const messages = chat.messages;

  useEffect(() => {
    const list = listRef.current;
    if (list) {
      list.scrollTop = list.scrollHeight;
    }
  }, [chat.id, chat.messages.length]);

  useEffect(() => {
    if (!isMobile || !pickerFor) {
      return;
    }
    const close = (event: globalThis.PointerEvent) => {
      const target = event.target;
      if (target instanceof Element && target.closest(".reaction-picker")) {
        return;
      }
      setPickerFor(null);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [isMobile, pickerFor]);

  useEffect(() => {
    setPickerFor(null);
  }, [chat.id]);

  useEffect(
    () => () => {
      if (press.current) {
        window.clearTimeout(press.current.timer);
      }
    },
    [],
  );

  const cancelPress = () => {
    if (press.current) {
      window.clearTimeout(press.current.timer);
      press.current = null;
    }
  };

  const startPress = (messageId: string, event: ReactPointerEvent<HTMLDivElement>) => {
    if (!isMobile || event.button !== 0) {
      return;
    }
    cancelPress();
    const { clientX: x, clientY: y } = event;
    const timer = window.setTimeout(() => {
      press.current = null;
      setPickerFor(messageId);
    }, 460);
    press.current = { timer, x, y };
  };

  const movePress = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!press.current) {
      return;
    }
    if (
      Math.abs(event.clientX - press.current.x) > 8 ||
      Math.abs(event.clientY - press.current.y) > 8
    ) {
      cancelPress();
    }
  };

  const react = (messageId: string, emoji: string) => {
    onReact(chat.id, messageId, emoji);
    setPickerFor(null);
  };

  return (
    <section className="chat">
      <header className="chat-head">
        {isMobile && onBack && (
          <button className="chat-back" type="button" aria-label="返回" onClick={onBack}>
            <Icon name="back" size={23} />
          </button>
        )}
        <Avatar spec={chat.avatar} size={isMobile ? 36 : 38} />
        <div className="chat-head-copy">
          <h2>{chat.name}</h2>
          <p>{chatSubtitle(chat)}</p>
        </div>
        {!isMobile && chat.kind === "group" && (
          <AvatarStack avatars={memberAvatars} />
        )}
      </header>

      <div className="messages" ref={listRef} role="log" aria-live="polite">
        <div className="messages-inner">
          {messages.length === 0 && (
            <div className="chat-empty">
              <Avatar spec={chat.avatar} size={68} />
              <h2>{chat.name}</h2>
              <p>
                {chat.kind === "dm"
                  ? people[chat.members.find((id) => id !== "me") ?? ""]?.handle ?? ""
                  : chatSubtitle(chat)}
              </p>
            </div>
          )}
          {messages.map((message, index) => {
            const previous = messages[index - 1];
            const next = messages[index + 1];
            const showDay = !previous || dayKey(previous.at) !== dayKey(message.at);
            const samePrevious =
              !!previous &&
              previous.kind !== "event" &&
              message.kind !== "event" &&
              previous.from === message.from &&
              dayKey(previous.at) === dayKey(message.at);
            const sameNext =
              !!next &&
              next.kind !== "event" &&
              message.kind !== "event" &&
              next.from === message.from &&
              dayKey(next.at) === dayKey(message.at);

            if (message.kind === "event") {
              return (
                <div key={message.id}>
                  {showDay && <DayDivider iso={message.at} />}
                  <div className="event-row">{message.text}</div>
                </div>
              );
            }

            const sender = people[message.from] ?? people.me;
            const mine = message.from === "me";
            const showRead =
              mine && !!message.read && last?.id === message.id;
            const showName = !mine && chat.kind === "group" && !samePrevious;
            const showMeta =
              !sameNext &&
              (last?.id === message.id ||
                (next ? minutesBetween(next.at, message.at) >= 12 : true));
            const reactions = Object.entries(message.reactions ?? {}).filter(
              ([, count]) => count > 0,
            );

            return (
              <div key={message.id}>
                {showDay && <DayDivider iso={message.at} />}
                <div
                  className={[
                    "msg-row",
                    mine ? "mine" : "theirs",
                    samePrevious ? "run-continued" : "run-start",
                    sameNext ? "" : "run-end",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                >
                  {!mine &&
                    (samePrevious ? (
                      <span className="msg-avatar-spacer" />
                    ) : (
                      <Avatar spec={sender.avatar} size={28} className="msg-avatar" />
                    ))}
                  <div className="msg-body">
                    {showName && <div className="msg-name">{sender.name}</div>}
                    <div
                      className={`bubble ${message.kind === "file" ? "file-bubble" : ""}`}
                      onPointerDown={
                        isMobile ? (event) => startPress(message.id, event) : undefined
                      }
                      onPointerMove={isMobile ? movePress : undefined}
                      onPointerUp={isMobile ? cancelPress : undefined}
                      onPointerLeave={isMobile ? cancelPress : undefined}
                      onPointerCancel={isMobile ? cancelPress : undefined}
                      onContextMenu={
                        isMobile ? (event) => event.preventDefault() : undefined
                      }
                      onDoubleClick={
                        isMobile ? () => onReact(chat.id, message.id, "❤️") : undefined
                      }
                    >
                      {message.kind === "file" ? (
                        <div className="file-card">
                          <span className="file-glyph">
                            <Icon name="file" size={19} />
                          </span>
                          <span className="file-copy">
                            <strong>{message.file?.name}</strong>
                            <small>
                              {[message.file?.kind, message.file?.size]
                                .filter(Boolean)
                                .join(" · ")}
                            </small>
                          </span>
                        </div>
                      ) : (
                        message.text
                      )}
                    </div>
                    {reactions.length > 0 && (
                      <div className="reactions">
                        {reactions.map(([emoji, count]) => (
                          <button
                            type="button"
                            key={emoji}
                            className="reaction"
                            aria-label={`${emoji} ${count}`}
                            onClick={() => onReact(chat.id, message.id, emoji)}
                          >
                            <span>{emoji}</span>
                            {count > 1 && <b>{count}</b>}
                          </button>
                        ))}
                      </div>
                    )}
                    {showMeta && (
                      <div className="msg-meta">
                        <span>{clock(message.at)}</span>
                        {showRead && (
                          <span className="read-receipt">
                            <Icon name="check" size={12} />
                            已读
                          </span>
                        )}
                      </div>
                    )}
                    <div
                      className={`reaction-picker ${
                        pickerFor === message.id ? "is-open" : ""
                      }`}
                      aria-label="回应"
                    >
                      {REACTIONS.map((emoji) => (
                        <button
                          type="button"
                          key={emoji}
                          aria-label={`回应 ${emoji}`}
                          onClick={() => react(message.id, emoji)}
                        >
                          {emoji}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      </div>

      <Composer
        key={chat.id}
        chatId={chat.id}
        isMobile={isMobile}
        onSend={onSend}
      />
    </section>
  );
}

function DayDivider({ iso }: { iso: string }) {
  return (
    <div className="day-divider" role="separator">
      <span>{dayLabel(iso)}</span>
    </div>
  );
}

function Composer({
  chatId,
  isMobile,
  onSend,
}: {
  chatId: string;
  isMobile: boolean;
  onSend: (chatId: string, text: string) => void;
}) {
  const [draft, setDraft] = useState("");
  const [emojiOpen, setEmojiOpen] = useState(false);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!isMobile) {
      inputRef.current?.focus();
    }
  }, [isMobile]);

  useEffect(() => {
    const input = inputRef.current;
    if (!input) {
      return;
    }
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, 132)}px`;
  }, [draft]);

  useEffect(() => {
    if (!emojiOpen) {
      return;
    }
    const close = (event: MouseEvent) => {
      if (composerRef.current && !composerRef.current.contains(event.target as Node)) {
        setEmojiOpen(false);
      }
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [emojiOpen]);

  const submit = () => {
    const value = draft.trim();
    if (!value) {
      return;
    }
    onSend(chatId, value);
    setDraft("");
    setEmojiOpen(false);
    window.requestAnimationFrame(() => inputRef.current?.focus());
  };

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    submit();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !isMobile) {
      event.preventDefault();
      submit();
    }
    if (event.key === "Escape") {
      setEmojiOpen(false);
    }
  };

  return (
    <div className="composer" ref={composerRef}>
      {emojiOpen && (
        <div className="emoji-popover" role="dialog" aria-label="表情">
          {EMOJIS.map((emoji) => (
            <button
              type="button"
              key={emoji}
              onClick={() => {
                setDraft((current) => current + emoji);
                setEmojiOpen(false);
                window.requestAnimationFrame(() => inputRef.current?.focus());
              }}
            >
              {emoji}
            </button>
          ))}
        </div>
      )}
      <form className="composer-inner" onSubmit={handleSubmit}>
        <div className="composer-field">
          <button
            type="button"
            className={`composer-icon ${emojiOpen ? "active" : ""}`}
            aria-label="表情"
            aria-expanded={emojiOpen}
            onClick={() => setEmojiOpen((open) => !open)}
          >
            <Icon name="smile" size={21} />
          </button>
          <textarea
            ref={inputRef}
            rows={1}
            value={draft}
            maxLength={4000}
            placeholder="发消息"
            aria-label="消息内容"
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={handleKeyDown}
          />
          <button
            type="submit"
            className="send-button"
            aria-label="发送"
            disabled={!draft.trim()}
          >
            <Icon name="send" size={17} />
          </button>
        </div>
      </form>
    </div>
  );
}

function MePage({
  isMobile,
  theme,
  onTheme,
}: {
  isMobile: boolean;
  theme: Theme;
  onTheme: (theme: Theme) => void;
}) {
  const [health, setHealth] = useState<"checking" | "ok" | "down">("checking");

  useEffect(() => {
    const controller = new AbortController();
    fetch("/host/v1/health", { cache: "no-store", signal: controller.signal })
      .then((response) => {
        if (!response.ok) {
          throw new Error("unhealthy");
        }
        return response.json() as Promise<{ status?: string }>;
      })
      .then((payload) => {
        setHealth(payload.status === "ok" ? "ok" : "down");
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setHealth("down");
        }
      });
    return () => controller.abort();
  }, []);

  const options: { value: Theme; label: string; icon: IconName }[] = [
    { value: "light", label: "浅色", icon: "sun" },
    { value: "dark", label: "深色", icon: "moon" },
    { value: "system", label: "自动", icon: "auto" },
  ];

  return (
    <div className={`page me-page ${isMobile ? "mobile-page" : ""}`}>
      {isMobile ? (
        <MobileHeading title="我" />
      ) : (
        <div className="desktop-page-head">
          <h1>我</h1>
        </div>
      )}
      <div className="page-scroll">
        <div className="page-inner">
          <div className="profile-hero">
            <Avatar spec={me.avatar} size={isMobile ? 76 : 84} presence />
            <div className="profile-copy">
              <h2>{me.name}</h2>
              <p>{me.handle}</p>
            </div>
          </div>

          <div className="section-caption">外观</div>
          <div className="card">
            <div className="theme-options" role="radiogroup" aria-label="外观">
              {options.map((option) => (
                <button
                  type="button"
                  key={option.value}
                  role="radio"
                  aria-checked={theme === option.value}
                  className={`theme-option ${theme === option.value ? "active" : ""}`}
                  onClick={() => onTheme(option.value)}
                >
                  <Icon name={option.icon} size={21} />
                  <span>{option.label}</span>
                </button>
              ))}
            </div>
          </div>

          <div className="section-caption">关于</div>
          <div className="card about-card">
            <div className="about-row">
              <span>服务器</span>
              <span>{window.location.host}</span>
            </div>
            <div className="about-row">
              <span>服务</span>
              <span className={`service-state ${health}`}>
                <i />
                {health === "ok" ? "正常" : health === "down" ? "连接异常" : "检查中"}
              </span>
            </div>
            <div className="about-row">
              <span>版本</span>
              <span>0.1.0 · 预览版</span>
            </div>
          </div>

          <div className="me-footer">
            <BrandMark size={18} />
            <span>Toget</span>
          </div>
        </div>
      </div>
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
    { id: "contacts", label: "通讯录", icon: "contacts" },
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
              <span className="tabbar-badge">{unread > 99 ? "99+" : unread}</span>
            )}
          </span>
          <span>{item.label}</span>
        </button>
      ))}
    </nav>
  );
}

function NewChatSheet({
  open,
  isMobile,
  onClose,
  onPick,
}: {
  open: boolean;
  isMobile: boolean;
  onClose: () => void;
  onPick: (id: string) => void;
}) {
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (!open) {
      setQuery("");
    }
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const close = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [open, onClose]);

  if (!open) {
    return null;
  }

  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = needle
    ? contacts.filter((person) =>
        `${person.name} ${person.handle}`
          .toLocaleLowerCase("zh-CN")
          .includes(needle),
      )
    : contacts;

  return (
    <div
      className={`sheet-backdrop ${isMobile ? "sheet-mobile-backdrop" : ""}`}
      onClick={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className={`sheet ${isMobile ? "sheet-mobile" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label="新对话"
      >
        {isMobile && <span className="sheet-grabber" />}
        <div className="sheet-head">
          <h2>新对话</h2>
          <button type="button" className="sheet-close" aria-label="关闭" onClick={onClose}>
            <Icon name="close" size={18} />
          </button>
        </div>
        <SearchBox
          value={query}
          onChange={setQuery}
          placeholder="搜索联系人"
          autoFocus={!isMobile}
        />
        <div className="sheet-list">
          <div className="section-caption">联系人</div>
          {visible.map((person) => (
            <PersonRow key={person.id} person={person} onOpen={onPick} />
          ))}
          {visible.length === 0 && (
            <div className="list-empty">
              <Icon name="search" size={20} />
              <span>没有找到</span>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function EmptyChat() {
  return (
    <div className="empty-chat">
      <BrandMark size={54} />
      <p>选择一个对话</p>
    </div>
  );
}
