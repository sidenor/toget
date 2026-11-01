import { useState } from "react";
import { memberAvatar } from "./avatars";
import { copyText } from "./helpers";
import type { Profile, RoomView, StoredMember } from "./types";
import { Avatar, BrandMark, Icon, SearchBox } from "./ui";

export function aggregateMembers(rooms: RoomView[]): StoredMember[] {
  const byID = new Map<string, StoredMember>();
  for (const room of rooms) {
    for (const member of room.members) {
      if (member.status !== "active") {
        continue;
      }
      let entry = byID.get(member.account_id);
      if (!entry) {
        entry = {
          accountID: member.account_id,
          name: member.display_name,
          rooms: [],
        };
        byID.set(member.account_id, entry);
      }
      if (!entry.rooms.some((item) => item.id === room.summary.id)) {
        entry.rooms.push({
          id: room.summary.id,
          name: room.summary.name,
          role: member.role,
        });
      }
    }
  }
  return Array.from(byID.values()).sort((a, b) =>
    a.name.localeCompare(b.name, "zh-CN"),
  );
}

export function MemberRow({
  member,
  isSelf,
  active,
  onOpen,
  compact = false,
}: {
  member: StoredMember;
  isSelf: boolean;
  active: boolean;
  onOpen: () => void;
  compact?: boolean;
}) {
  const owner = member.rooms.some((room) => room.role === "owner");
  return (
    <button
      type="button"
      className={`person-row member-person-row ${active ? "active" : ""}`}
      onClick={onOpen}
    >
      <Avatar
        spec={memberAvatar(member.accountID, member.name)}
        size={compact ? 36 : 42}
      />
      <span className="person-copy">
        <span className="person-name">
          {member.name}
          {isSelf && <span className="member-you">我</span>}
          {owner && <span className="person-spark owner-mark">所有者</span>}
        </span>
        <span className="person-status">
          {member.rooms.length} 个共同房间
        </span>
      </span>
      <Icon name="chevronRight" size={15} className="person-chevron" />
    </button>
  );
}

export function MembersList({
  members,
  profile,
  selectedID,
  onSelect,
  compact = false,
}: {
  members: StoredMember[];
  profile: Profile;
  selectedID: string | null;
  onSelect: (member: StoredMember) => void;
  compact?: boolean;
}) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  const visible = (needle
    ? members.filter((member) =>
        member.name.toLocaleLowerCase("zh-CN").includes(needle),
      )
    : members
  )
    .slice()
    .sort((a, b) => {
      if (a.accountID === profile.account_id) {
        return -1;
      }
      if (b.accountID === profile.account_id) {
        return 1;
      }
      return a.name.localeCompare(b.name, "zh-CN");
    });

  return (
    <div className={`list-wrap ${compact ? "" : "mobile-list"}`}>
      <SearchBox
        value={query}
        onChange={setQuery}
        placeholder="搜索成员"
        autoFocus={false}
      />
      <div className="contact-list">
        {visible.map((member) => (
          <MemberRow
            key={member.accountID}
            member={member}
            isSelf={member.accountID === profile.account_id}
            active={selectedID === member.accountID}
            compact={compact}
            onOpen={() => onSelect(member)}
          />
        ))}
        {visible.length === 0 && (
          <div className="list-empty">
            <Icon name="search" size={20} />
            <span>{needle ? "没有找到" : "还没有共同房间里的人"}</span>
          </div>
        )}
      </div>
    </div>
  );
}

export function MembersPage({
  members,
  profile,
  roomCount,
  selectedID,
  onSelect,
}: {
  members: StoredMember[];
  profile: Profile;
  roomCount: number;
  selectedID: string | null;
  onSelect: (member: StoredMember) => void;
}) {
  return (
    <div className="page mobile-page members-page">
      <div className="mobile-head">
        <div className="mobile-heading">
          <BrandMark size={25} />
          <h1>成员</h1>
        </div>
      </div>
      <div className="member-overview-line">
        {members.length} 位成员 · {roomCount} 个房间
      </div>
      <MembersList
        members={members}
        profile={profile}
        selectedID={selectedID}
        onSelect={onSelect}
      />
    </div>
  );
}

export function MembersOverview({
  members,
  profile,
  roomCount,
  onSelect,
}: {
  members: StoredMember[];
  profile: Profile;
  roomCount: number;
  onSelect: (member: StoredMember) => void;
}) {
  return (
    <div className="page members-overview-page">
      <div className="desktop-page-head">
        <h1>成员</h1>
        <p className="desktop-page-sub">
          {members.length} 位成员，分布在 {roomCount} 个房间里
        </p>
      </div>
      <div className="page-scroll">
        <div className="page-inner members-inner">
          {members.length === 0 ? (
            <div className="empty-chat members-empty">
              <Icon name="contacts" size={30} />
              <p>创建或加入房间后，成员会出现在这里</p>
            </div>
          ) : (
            <div className="member-grid">
              {members.map((member) => (
                <button
                  type="button"
                  className="member-card"
                  key={member.accountID}
                  onClick={() => onSelect(member)}
                >
                  <Avatar
                    spec={memberAvatar(member.accountID, member.name)}
                    size={46}
                  />
                  <span className="member-card-copy">
                    <strong>
                      {member.name}
                      {member.accountID === profile.account_id && (
                        <span className="member-you">我</span>
                      )}
                    </strong>
                    <small>{member.rooms.length} 个共同房间</small>
                  </span>
                  <Icon name="chevronRight" size={15} />
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export function MemberDetail({
  member,
  profile,
  isMobile,
  onBack,
  onOpenRoom,
}: {
  member: StoredMember;
  profile: Profile;
  isMobile: boolean;
  onBack?: () => void;
  onOpenRoom: (roomID: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const isSelf = member.accountID === profile.account_id;

  const copy = async () => {
    if (await copyText(member.accountID)) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    }
  };

  return (
    <div className={`page member-detail-page ${isMobile ? "mobile-page" : ""}`}>
      {isMobile ? (
        <div className="mobile-detail-head">
          <button
            type="button"
            className="chat-back"
            aria-label="返回成员"
            onClick={onBack}
          >
            <Icon name="back" size={23} />
          </button>
          <span>成员</span>
          <span className="head-spacer" />
        </div>
      ) : (
        <div className="desktop-page-head">
          {onBack && (
            <button type="button" className="desktop-back" onClick={onBack}>
              <Icon name="back" size={15} />
              全部成员
            </button>
          )}
          <h1>成员</h1>
        </div>
      )}
      <div className="page-scroll">
        <div className="page-inner">
          <div className="member-detail-hero">
            <Avatar
              spec={memberAvatar(member.accountID, member.name)}
              size={82}
            />
            <div>
              <h2>
                {member.name}
                {isSelf && <span className="member-you">我</span>}
              </h2>
              <p>{member.rooms.length} 个共同房间</p>
            </div>
          </div>

          <div className="section-caption">账号</div>
          <div className="card about-card">
            <div className="about-row">
              <span>成员编号</span>
              <button
                type="button"
                className="copy-value"
                onClick={() => void copy()}
                title="复制成员编号"
              >
                <span className="mono">{member.accountID}</span>
                <Icon name={copied ? "check" : "copy"} size={14} />
              </button>
            </div>
            <div className="about-row">
              <span>身份</span>
              <span>{isSelf ? "我自己" : "房间成员"}</span>
            </div>
          </div>

          <div className="section-caption">共同房间</div>
          <div className="card shared-room-list">
            {member.rooms.map((room) => (
              <button
                type="button"
                className="shared-room-row"
                key={room.id}
                onClick={() => onOpenRoom(room.id)}
              >
                <span className="shared-room-dot">
                  <Icon name="chats" size={17} />
                </span>
                <span className="shared-room-copy">
                  <strong>{room.name}</strong>
                  <small>{room.role === "owner" ? "所有者" : "成员"}</small>
                </span>
                <Icon name="chevronRight" size={15} />
              </button>
            ))}
          </div>

          {!isSelf && (
            <p className="member-note">
              消息只能在共同房间里发送。成员离开共同房间后，这里就不会再显示。
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
