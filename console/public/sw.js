// Agent Fleet Console service worker — deliberately minimal (docs/log/21 image attachments).
//
// Its ONLY job is the Web Share Target: receive a POST share from Android's 共有シート,
// stash the payload in CacheStorage, and redirect into the app so the memo composer can
// pick it up (src/features/memo/share.ts reads the stash directly and uploads the
// images). It does NOT cache the app shell or intercept ordinary requests — anything
// that isn't the share POST falls through to the network untouched, so SSE/streaming,
// WebSockets and the build's version-manifest cache-busting keep working exactly as
// before the SW existed.
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
