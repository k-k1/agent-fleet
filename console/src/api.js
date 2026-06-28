// Shared API layer. Ported from the Phase 1 Console (console/legacy-phase1/app.js)
// — the behavioral contract is unchanged; only the packaging moved to modules.

// Resolve URLs relative to where the Console is mounted, so it works both at the
// host root (http://localhost:8099/) and behind a path-stripping proxy (Tailscale
// Funnel + Caddy at /agent-fleet/). Uses the trailing-slash baseURI.
export const rel = (p) => new URL(p, document.baseURI).toString();

// --- tenant selection (P3-2) ---
// The active tenant is sent on every request as X-AF-Tenant so the Control Plane
// resolves the right per-membership workspace. We inject the header globally by
// wrapping window.fetch so every request — including ones we don't author here —
// carries it. The terminal WebSocket can't send headers, so attach() appends
// &tenant=<slug> to its URL instead (see term.js).
let selectedTenant = localStorage.getItem("af-tenant") || "";

export const getTenant = () => selectedTenant;
export function setTenant(slug) {
  selectedTenant = slug || "";
  localStorage.setItem("af-tenant", selectedTenant);
}

const _fetch = window.fetch.bind(window);
window.fetch = (input, init = {}) => {
  if (selectedTenant) {
    const h = new Headers(init.headers || {});
    if (!h.has("X-AF-Tenant")) h.set("X-AF-Tenant", selectedTenant);
    init = { ...init, headers: h };
  }
  return _fetch(input, init);
};

// api() resolves the path against baseURI and parses JSON. Mirrors the legacy
// helper: callers handle `res.error` shapes themselves.
export const api = (path, opts) => fetch(rel(path), opts).then((r) => r.json());

// apiJSON is a convenience for the common "POST/PUT JSON body" shape.
export const apiJSON = (path, method, body) =>
  api(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

// raw() returns the Response (not parsed) for callers that need r.ok / status.
export const raw = (path, opts) => fetch(rel(path), opts);
export const rawJSON = (path, method, body) =>
  raw(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

// Build the preview URL for a port the user started a service on inside the
// container (Spring Boot, dev server, ...). Opened as a top-level navigation, so
// the tenant rides as a query param — a new tab can't carry the X-AF-Tenant
// header that fetch() injects. The CP resolves tenant from this fallback.
export function previewURL(port) {
  const u = new URL(rel(`preview/${encodeURIComponent(port)}/`));
  if (selectedTenant) u.searchParams.set("tenant", selectedTenant);
  return u.toString();
}

// Build the terminal WebSocket URL for a session under the current mount, with
// the tenant carried as a query param (headers aren't available on WS).
export function wsURL(session) {
  const u = new URL(rel("ws/terminal"));
  u.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  u.search =
    `?session=${encodeURIComponent(session)}` +
    (selectedTenant ? `&tenant=${encodeURIComponent(selectedTenant)}` : "");
  return u;
}
