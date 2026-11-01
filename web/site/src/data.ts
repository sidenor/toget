import type { Chat, Person, Profile } from "./types";

function minutesAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function daysAgoAt(days: number, hour: number, minute: number): string {
  const date = new Date();
  date.setDate(date.getDate() - days);
  date.setHours(hour, minute, 0, 0);
  return date.toISOString();
}

export const people: Record<string, Person> = {
  me: {
    id: "me",
    name: "Aiden",
    handle: "@aiden",
    avatar: { label: "A", tone: "jade" },
    status: "在线",
  },
  mira: {
    id: "mira",
    name: "Mira",
    handle: "@mira",
    avatar: { label: "M", tone: "sky" },
    status: "在线",
  },
  kai: {
    id: "kai",
    name: "Kai",
    handle: "@kai",
    avatar: { label: "K", tone: "peach" },
    status: "2 小时前",
  },
  lin: {
    id: "lin",
    name: "林岸",
    handle: "@lin",
    avatar: { label: "林", tone: "lilac" },
    status: "昨天",
  },
  agent: {
    id: "agent",
    name: "小助手",
    handle: "@agent",
    avatar: { label: "✦", tone: "slate", agent: true },
    status: "随时在",
    agent: true,
  },
  zhou: {
    id: "zhou",
    name: "周予",
    handle: "@zhouyu",
    avatar: { label: "周", tone: "sand" },
    status: "3 天前",
  },
  song: {
    id: "song",
    name: "宋一",
    handle: "@songyi",
    avatar: { label: "宋", tone: "rose" },
    status: "离线",
  },
  zhe: {
    id: "zhe",
    name: "阿哲",
    handle: "@azhe",
    avatar: { label: "哲", tone: "jade" },
    status: "在线",
  },
};

export const me: Profile = people.me;

export const contacts: Person[] = [
  people.agent,
  people.mira,
  people.kai,
  people.lin,
  people.zhou,
  people.song,
  people.zhe,
];

export const initialChats: Chat[] = [
  {
    id: "product",
    name: "产品讨论",
    avatar: { label: "产", tone: "jade" },
    kind: "group",
    members: ["me", "mira", "kai", "agent"],
    pinned: true,
    unread: 0,
    messages: [
      {
        id: "product-1",
        from: "mira",
        kind: "event",
        text: "Mira 创建了群聊",
        at: daysAgoAt(3, 10, 12),
      },
      {
        id: "product-2",
        from: "mira",
        text: "新版首页我看了，整体轻了很多。",
        at: minutesAgo(52),
      },
      {
        id: "product-3",
        from: "kai",
        text: "对，尤其是空状态那屏，删掉说明之后反而清楚了。",
        at: minutesAgo(50),
      },
      {
        id: "product-4",
        from: "mira",
        kind: "file",
        file: { name: "home-empty.png", size: "1.8 MB", kind: "PNG" },
        at: minutesAgo(48),
      },
      {
        id: "product-5",
        from: "me",
        text: "收到，我把这段留白再收紧一点。今晚补一版。",
        at: minutesAgo(44),
        read: true,
      },
      {
        id: "product-6",
        from: "agent",
        text: "已生成三个间距方案，放在设计稿第 12 页。",
        at: minutesAgo(40),
        reactions: { "👍": 1, "✨": 1 },
      },
      {
        id: "product-7",
        from: "mira",
        text: "第二个，呼吸感刚好。",
        at: minutesAgo(37),
        reactions: { "❤️": 2 },
      },
      {
        id: "product-8",
        from: "kai",
        text: "那今晚就按第二版走。",
        at: minutesAgo(34),
      },
      {
        id: "product-9",
        from: "me",
        text: "好，我发完这版就收工。",
        at: minutesAgo(31),
        read: false,
      },
    ],
  },
  {
    id: "agent",
    name: "小助手",
    avatar: { label: "✦", tone: "slate", agent: true },
    kind: "agent",
    members: ["me", "agent"],
    unread: 0,
    messages: [
      {
        id: "agent-1",
        from: "me",
        text: "帮我把今天的会议纪要整理一下。",
        at: minutesAgo(196),
        read: true,
      },
      {
        id: "agent-2",
        from: "agent",
        text: "整理好了。三条结论、两个待办，待办已经按截止时间排好。",
        at: minutesAgo(192),
      },
      {
        id: "agent-3",
        from: "agent",
        kind: "file",
        file: { name: "会议纪要 · 11月1日.md", size: "12 KB", kind: "MD" },
        at: minutesAgo(191),
      },
      {
        id: "agent-4",
        from: "me",
        text: "谢谢，顺手把明天上午的提醒也加上。",
        at: minutesAgo(186),
        read: true,
      },
    ],
  },
  {
    id: "hike",
    name: "周末徒步",
    avatar: { label: "山", tone: "sky" },
    kind: "group",
    members: ["me", "mira", "kai"],
    unread: 2,
    messages: [
      {
        id: "hike-1",
        from: "mira",
        kind: "event",
        text: "Mira 创建了群聊",
        at: daysAgoAt(6, 9, 0),
      },
      {
        id: "hike-2",
        from: "mira",
        text: "周六早上七点，老地方集合？",
        at: daysAgoAt(1, 20, 12),
      },
      {
        id: "hike-3",
        from: "kai",
        text: "可以，我带咖啡。",
        at: daysAgoAt(1, 20, 18),
      },
      {
        id: "hike-4",
        from: "me",
        text: "我带登山杖，天气预报说下午有雨。",
        at: daysAgoAt(1, 20, 24),
        read: true,
      },
      {
        id: "hike-5",
        from: "mira",
        text: "路线还是走上次那条，避开施工。",
        at: daysAgoAt(1, 20, 31),
      },
      {
        id: "hike-6",
        from: "kai",
        text: "收到。",
        at: daysAgoAt(1, 20, 33),
      },
    ],
  },
  {
    id: "book",
    name: "读书会",
    avatar: { label: "读", tone: "sand" },
    kind: "group",
    members: ["me", "lin", "mira"],
    unread: 0,
    messages: [
      {
        id: "book-1",
        from: "lin",
        text: "这周读《城市与狗》，周四晚上聊？",
        at: daysAgoAt(2, 19, 5),
      },
      {
        id: "book-2",
        from: "me",
        text: "周四可以。",
        at: daysAgoAt(2, 19, 12),
        read: true,
      },
      {
        id: "book-3",
        from: "lin",
        text: "那我把笔记整理一下发你。",
        at: daysAgoAt(2, 19, 20),
      },
    ],
  },
  {
    id: "mira",
    name: "Mira",
    avatar: { label: "M", tone: "rose" },
    kind: "dm",
    members: ["me", "mira"],
    unread: 1,
    messages: [
      {
        id: "mira-1",
        from: "mira",
        text: "你上次说的那家面包店，叫什么来着？",
        at: daysAgoAt(3, 9, 12),
      },
      {
        id: "mira-2",
        from: "me",
        text: "Bake Shop，在安福路那个路口。",
        at: daysAgoAt(3, 9, 18),
        read: true,
      },
      {
        id: "mira-3",
        from: "mira",
        text: "找到了，谢谢。",
        at: daysAgoAt(3, 9, 20),
      },
    ],
  },
  {
    id: "design",
    name: "设计评审",
    avatar: { label: "设", tone: "lilac" },
    kind: "group",
    members: ["me", "lin", "zhou"],
    unread: 0,
    messages: [
      {
        id: "design-1",
        from: "me",
        kind: "event",
        text: "你加入了群聊",
        at: daysAgoAt(7, 14, 0),
      },
      {
        id: "design-2",
        from: "lin",
        text: "评审改到明天下午三点。",
        at: daysAgoAt(7, 14, 5),
      },
      {
        id: "design-3",
        from: "me",
        text: "收到。",
        at: daysAgoAt(7, 14, 8),
        read: true,
      },
    ],
  },
];
