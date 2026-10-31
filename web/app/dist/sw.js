// Toget app-shell service worker. The node itself is local, so the shell is
// cached for offline launch; API responses are never cached.
const CACHE = "toget-shell-v2";
const SHELL = ["/", "/manifest.webmanifest", "/icons/icon-192.png", "/icons/icon-512.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/healthz")) return;

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then((response) => {
          if (response.ok) {
            const copy = response.clone();
            caches.open(CACHE).then((cache) => cache.put("/", copy));
          }
          return response;
        })
        .catch(() => caches.match("/").then((hit) => hit || new Response("Offline", { status: 503 }))),
    );
    return;
  }

  event.respondWith(
    caches.match(request).then((hit) => {
      if (hit) return hit;
      return fetch(request).then((response) => {
        if (response.ok) {
          const copy = response.clone();
          caches.open(CACHE).then((cache) => cache.put(request, copy));
        }
        return response;
      });
    }),
  );
});

// Push payloads are deliberately content-free: {v, type, lang}. Never add
// message text, senders, room names, or approval descriptions here.
function notificationCopy(payload) {
  const type = payload && payload.type === "approval" ? "approval" : "message";
  const zh = payload && String(payload.lang || "").toLowerCase().startsWith("zh");
  if (type === "approval") {
    return {
      title: zh ? "Toget · 有新的审批" : "Toget · Approval needed",
      body: zh ? "打开 Toget 查看并处理。" : "Open Toget to review it.",
      url: "/?view=approvals",
      tag: "toget-approval",
    };
  }
  return {
    title: zh ? "Toget · 新消息" : "Toget · New message",
    body: zh ? "打开 Toget 查看。" : "Open Toget to catch up.",
    url: "/?view=chat",
    tag: "toget-message",
  };
}

self.addEventListener("push", (event) => {
  let payload = null;
  try {
    payload = event.data ? event.data.json() : null;
  } catch {
    payload = null;
  }
  const copy = notificationCopy(payload);
  event.waitUntil(
    self.registration.showNotification(copy.title, {
      body: copy.body,
      icon: "/icons/icon-192.png",
      badge: "/icons/icon-192.png",
      tag: copy.tag,
      data: { url: copy.url },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = new URL(
    (event.notification.data && event.notification.data.url) || "/",
    self.location.origin,
  );
  if (target.origin !== self.location.origin) return;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(async (windows) => {
      for (const client of windows) {
        if (new URL(client.url).origin !== self.location.origin) continue;
        await client.navigate(target.href);
        return client.focus();
      }
      return self.clients.openWindow(target.href);
    }),
  );
});
