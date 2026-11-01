import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { FormEvent, KeyboardEvent } from "react";
import { memberAvatar, roomAvatar } from "./avatars";
import { clock, dayKey, dayLabel } from "./helpers";
import type { Profile, RoomView } from "./types";
import { Avatar, Icon } from "./ui";

const EMOJIS = ["😊", "👍", "✨", "❤️", "😂", "🎉", "🙏", "🔥"];

function minutesBetweenISO(later: string, earlier: string): number {
  return (new Date(later).getTime() - new Date(earlier).getTime()) / 60_000;
}

export function ChatView({
  room,
  profile,
  isMobile,
  sending,
  syncing,
  onBack,
  onSend,
  onOpenInfo,
  onRetrySync,
}: {
  room: RoomView;
  profile: Profile;
  isMobile: boolean;
  sending: boolean;
  syncing: boolean;
  onBack?: () => void;
  onSend: (text: string) => Promise<boolean>;
  onOpenInfo: () => void;
  onRetrySync: () => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [scrollPending, setScrollPending] = useState(false);

  const messages = room.messages;
  const memberNames = new Map(
    room.members.map((member) => [member.account_id, member.display_name]),
  );

  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) {
      return;
    }
    if (stick.current || scrollPending) {
      list.scrollTop = list.scrollHeight;
      stick.current = true;
      setScrollPending(false);
    }
  }, [messages.length, sending, scrollPending]);

  useEffect(() => {
    stick.current = true;
    const list = listRef.current;
    if (list) {
      list.scrollTop = list.scrollHeight;
    }
  }, [room.summary.id]);

  useEffect(() => {
    if (!sending) {
      return;
    }
    setScrollPending(true);
  }, [sending]);

  const onScroll = () => {
    const list = listRef.current;
    if (!list) {
      return;
    }
    stick.current =
      list.scrollHeight - list.scrollTop - list.clientHeight < 90;
  };

  const subtitle = `${room.members.length} 位成员 · 端到端加密`;
  const avatar = roomAvatar(room.summary.id, room.summary.name);
  const showLocked = room.locked_count > 0;

  return (
    <section className="chat">
      <header className="chat-head">
        {isMobile && onBack && (
          <button className="chat-back" type="button" aria-label="返回" onClick={onBack}>
            <Icon name="back" size={23} />
          </button>
        )}
        <Avatar spec={avatar} size={isMobile ? 36 : 38} />
        <div className="chat-head-copy">
          <h2>{room.summary.name}</h2>
          <p>
            <Icon name="lock" size={11} />
            {subtitle}
          </p>
        </div>
        <div className="chat-head-actions">
          {syncing && !isMobile && (
            <span className="sync-dot" aria-label="正在同步">
              <i className="spinner tiny" />
            </span>
          )}
          <button
            type="button"
            className="icon-button"
            aria-label="房间信息"
            onClick={onOpenInfo}
          >
            <Icon name="info" size={19} />
          </button>
        </div>
      </header>

      <div className="messages" ref={listRef} onScroll={onScroll} role="log" aria-live="polite">
        <div className="messages-inner">
          {showLocked && (
            <div className="inline-notice">
              <Icon name="lock" size={14} />
              <span>有 {room.locked_count} 条消息缺少这个房间的密钥。</span>
              <button type="button" onClick={onRetrySync} disabled={syncing}>
                {syncing ? "同步中" : "重试"}
              </button>
            </div>
          )}
          {messages.length === 0 && !sending && (
            <div className="chat-empty">
              <Avatar spec={avatar} size={68} />
              <h2>{room.summary.name}</h2>
              <p>
                {room.summary.topic?.trim() || "这个房间还没有消息"}
              </p>
            </div>
          )}
          {messages.map((message, index) => {
            const previous = messages[index - 1];
            const next = messages[index + 1];
            const showDay = !previous || dayKey(previous.at) !== dayKey(message.at);
            const mine = message.author_id === profile.account_id;

            if (message.kind === "system") {
              return (
                <div key={message.id}>
                  {showDay && <DayDivider iso={message.at} />}
                  <div className="event-row">
                    {message.author_name && (
                      <b>{message.author_name}</b>
                    )}
                    <span>{message.text}</span>
                  </div>
                </div>
              );
            }
            if (message.kind === "locked") {
              return (
                <div key={message.id}>
                  {showDay && <DayDivider iso={message.at} />}
                  <div className="event-row locked-row">
                    <Icon name="lock" size={13} />
                    <span>{message.text}</span>
                  </div>
                </div>
              );
            }

            const samePrevious =
              !!previous &&
              previous.kind === "text" &&
              previous.author_id === message.author_id &&
              previous.author_device === message.author_device &&
              dayKey(previous.at) === dayKey(message.at);
            const sameNext =
              !!next &&
              next.kind === "text" &&
              next.author_id === message.author_id &&
              next.author_device === message.author_device &&
              dayKey(next.at) === dayKey(message.at);
            const showName = !mine && !samePrevious;
            const showMeta =
              !sameNext &&
              (!next || minutesBetweenISO(next.at, message.at) >= 12);
            const authorName =
              message.author_name ||
              memberNames.get(message.author_id) ||
              "成员";
            const senderAvatar = memberAvatar(message.author_id, authorName);

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
                      <Avatar
                        spec={senderAvatar}
                        size={28}
                        className="msg-avatar"
                      />
                    ))}
                  <div className="msg-body">
                    {showName && <div className="msg-name">{authorName}</div>}
                    <div className="bubble">{message.text}</div>
                    {showMeta && (
                      <div className="msg-meta">
                        <span>{clock(message.at)}</span>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            );
          })}
          {sending && (
            <div className="msg-row mine run-start run-end">
              <div className="msg-body">
                <div className="bubble sending-bubble">
                  <span>正在发送</span>
                  <i className="spinner tiny" />
                </div>
              </div>
            </div>
          )}
        </div>
      </div>

      <Composer
        key={room.summary.id}
        isMobile={isMobile}
        sending={sending}
        disabled={!room.can_invite}
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
  isMobile,
  sending,
  disabled,
  onSend,
}: {
  isMobile: boolean;
  sending: boolean;
  disabled: boolean;
  onSend: (text: string) => Promise<boolean>;
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
    const close = (event: globalThis.PointerEvent) => {
      if (composerRef.current && !composerRef.current.contains(event.target as Node)) {
        setEmojiOpen(false);
      }
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [emojiOpen]);

  const submit = async () => {
    const value = draft.trim();
    if (!value || sending || disabled) {
      return;
    }
    const sent = await onSend(value);
    if (sent) {
      setDraft("");
      setEmojiOpen(false);
      window.requestAnimationFrame(() => inputRef.current?.focus());
    }
  };

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    void submit();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !isMobile) {
      event.preventDefault();
      void submit();
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
            maxLength={6000}
            placeholder={disabled ? "缺少房间密钥，暂时无法发送" : "发消息"}
            aria-label="消息内容"
            disabled={disabled}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={handleKeyDown}
          />
          <button
            type="submit"
            className="send-button"
            aria-label="发送"
            disabled={!draft.trim() || sending || disabled}
          >
            {sending ? (
              <i className="spinner tiny" />
            ) : (
              <Icon name="send" size={17} />
            )}
          </button>
        </div>
      </form>
    </div>
  );
}
