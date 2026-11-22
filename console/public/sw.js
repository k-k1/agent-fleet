// Agent Fleet Console service worker — deliberately minimal (docs/log/21 image attachments).
//
// Its jobs are the Web Share Target and Web Push (the handlers at the bottom). The share part: receive a POST share from Android's 共有シート,
// stash the payload in CacheStorage, and redirect into the app so the memo composer can
// pick it up (src/features/memo/share.ts reads the stash directly and uploads the
// images). It does NOT cache the app shell or intercept ordinary requests — anything
// that isn't the share POST falls through to the network untouched, so SSE/streaming,
// WebSockets and the build's version-manifest cache-busting keep working exactly as
// before the SW existed.
//
// Web Push (#4750): the `push` and `notificationclick` handlers below only show a notification and
// open the Console. They add no fetch handling, so the no-caching rule above still holds.
//
// The stash keys are origin-absolute and identical in the SW and the window (both read
// self/location.origin), so the app reads them straight from CacheStorage without
// needing the SW to serve them back.

const SHARE_CACHE = "af-share-v1";

const dataKey = (id) => `${self.location.origin}/__afshare__/${id}/data`;
const fileKey = (id, n) => `${self.location.origin}/__afshare__/${id}/f${n}`;

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (event) => {
  const req = event.request;
  // Match the share action regardless of the mount prefix (a path-stripping proxy may
  // serve the app under e.g. /agent-fleet/). Only the share POST is intercepted.
  if (req.method === "POST" && new URL(req.url).pathname.endsWith("/share-target")) {
    event.respondWith(isCrossSite(req) ? Promise.resolve(Response.redirect(self.registration.scope, 303)) : handleShare(req));
  }
  // Everything else: no respondWith → default network handling.
});

// isCrossSite: a share is handed over by the OS share sheet (no referrer, Fetch Metadata
// "none") or by the app itself. A page that auto-submits a form to share-target is neither.
// Sec-Fetch-Site / Origin are not guaranteed to be readable on a service worker request, so the
// referrer is the fallback; no signal at all is treated as the OS share sheet. A refused share
// stashes nothing and lands on the app home.
function isCrossSite(req) {
  const site = req.headers.get("Sec-Fetch-Site");
  if (site && site !== "same-origin" && site !== "none") return true;
  const origin = req.headers.get("Origin");
  if (origin && origin !== self.location.origin) return true;
  if (req.referrer && req.referrer !== "about:client") {
    try {
      if (new URL(req.referrer).origin !== self.location.origin) return true;
    } catch {
      return true;
    }
  }
  return false;
}

async function handleShare(req) {
  // scope root, with trailing slash — where the app lives; redirect back here.
  const home = self.registration.scope;
  try {
    const form = await req.formData();
    const meta = {
      title: form.get("title") || "",
      text: form.get("text") || "",
      url: form.get("url") || "",
      names: [],
    };
    const files = form.getAll("images").filter((f) => f && typeof f === "object" && f.size);
    const cache = await caches.open(SHARE_CACHE);
    // A monotonic-ish id without Date.now dependence pitfalls: time is fine here (SW,
    // not a resumable workflow). Kept simple; collisions only matter within a session.
    const id = String(Date.now());
    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      meta.names.push(f.name || `shared-${i}.png`);
      await cache.put(
        fileKey(id, i),
        new Response(f, { headers: { "Content-Type": f.type || "application/octet-stream" } }),
      );
    }
    await cache.put(
      dataKey(id),
      new Response(JSON.stringify(meta), { headers: { "Content-Type": "application/json" } }),
    );
    return Response.redirect(home + "?share=" + id, 303);
  } catch {
    return Response.redirect(home, 303);
  }
}

// --- Web Push ---------------------------------------------------------------------------------
// The payload is minimal by design (pushsrv.pushPayload): {v, kind, session, name, text?}. The
// wording is rendered here from kind, in the browser's language, so the push service never sees
// it. A payload that does not parse still shows a generic notification: a push the browser
// receives must show something (userVisibleOnly), and silence would let it revoke the permission.
const PUSH_TEXT = {
  ja: {
    question: "質問があります",
    "plan-approval": "プランの承認待ちです",
    "permission-request": "権限の許可待ちです",
    "answer-ready": "ターンが終わりました",
    other: "セッションが待っています",
  },
  en: {
    question: "A question is waiting",
    "plan-approval": "A plan is waiting for approval",
    "permission-request": "A permission request is waiting",
    "answer-ready": "A turn has finished",
    other: "A session needs you",
  },
};
// Same rule as the Agent's session.ValidName: a name from a push may hold capitals and underscores.
const SESSION_NAME = /^[A-Za-z0-9_-]{1,40}$/;
const has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);

function pushLines(data) {
  const lang = /^ja/i.test(self.navigator.language || "") ? "ja" : "en";
  const table = PUSH_TEXT[lang];
  // Own properties only: a kind such as "constructor" must not select an inherited function.
  const kind = data && typeof data.kind === "string" && has(table, data.kind) ? data.kind : "other";
  const name = data && typeof data.name === "string" ? data.name : "";
  const text = data && typeof data.text === "string" ? data.text : "";
  return { title: name || "Agent Fleet", body: text || table[kind], kind };
}

self.addEventListener("push", (event) => {
  let data = null;
  try {
    data = event.data ? event.data.json() : null;
  } catch {
    data = null;
  }
  const { title, body, kind } = pushLines(data);
  const session = data && typeof data.session === "string" && SESSION_NAME.test(data.session) ? data.session : "";
  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      // One notification per session and kind: a repeat replaces the earlier one instead of stacking.
      tag: session ? `af-${session}-${kind}` : undefined,
      data: { session },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const session = (event.notification.data && event.notification.data.session) || "";
  const scope = self.registration.scope;
  event.waitUntil(
    (async () => {
      const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      // Only a page running the Console answers the hand-off (a /login or preview page under the
      // same scope never does), so ask each and take the first that acknowledges.
      // A bounded search (five candidates, 3 s): a long one would let the tap's permission
      // to focus or open a window lapse.
      // The app's own page (the scope root) goes first, so previews or other pages under the same
      // origin cannot crowd it out of the short list; the whole search has a deadline.
      const mine = wins.filter((c) => c.url.startsWith(scope));
      mine.sort((a, b) => Number(isRoot(b.url, scope)) - Number(isRoot(a.url, scope)));
      // `over` is set when the deadline passes: the fallback window is opening then, and a search
      // that kept going would hand the session to, and focus, yet another open Console.
      let over = false;
      const search = async () => {
        for (const win of mine.slice(0, 5)) {
          if (over) return false;
          if (await handOff(win, session, () => over)) {
            if (over) return false;
            // A page that answered but cannot be focused (closing, navigating away) is not a hit.
            if (await win.focus().then(() => true, () => false)) return true;
          }
        }
        return false;
      };
      // One deadline over the whole search, focus() included: a page whose event loop stopped after
      // acknowledging would otherwise hold the tap until the permission to open a window lapses.
      let timer;
      const found = await Promise.race([search(), new Promise((r) => (timer = setTimeout(() => r(false), 3000)))]);
      clearTimeout(timer);
      over = true;
      if (found) return;
      await self.clients.openWindow(session ? `${scope}?session=${encodeURIComponent(session)}` : scope);
    })(),
  );
});

// handOff asks an open page to open the session and resolves true only when that page acknowledges
// (listenPushOpen in the app), so a tap never lands on a page that cannot act on it. With no
// session there is nothing to hand over: any Console page that answers is enough.
function isRoot(url, scope) {
  const u = new URL(url);
  return u.origin + u.pathname === scope || u.pathname === new URL(scope).pathname + "index.html";
}

function handOff(win, session, cancelled) {
  return new Promise((resolve) => {
    if (cancelled && cancelled()) return resolve(false);
    const ch = new MessageChannel();
    const timer = setTimeout(() => resolve(false), 1500);
    ch.port1.onmessage = () => {
      clearTimeout(timer);
      resolve(true);
    };
    try {
      win.postMessage({ type: "af-open-session", session }, [ch.port2]);
    } catch {
      clearTimeout(timer);
      resolve(false);
    }
  });
}
