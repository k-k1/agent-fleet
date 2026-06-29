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
// When AUTH=oauth the Control Plane gates every request on a verified Google
// session and answers an expired/absent one with 401 on XHR. Bounce the whole
// page to the login landing so the user can re-authenticate (a top-level nav,
// which the CP turns into the Google redirect). Guarded so we redirect once.
let _authRedirecting = false;
window.fetch = (input, init = {}) => {
  if (selectedTenant) {
    const h = new Headers(init.headers || {});
    if (!h.has("X-AF-Tenant")) h.set("X-AF-Tenant", selectedTenant);
    init = { ...init, headers: h };
  }
  return _fetch(input, init).then((res) => {
    if (res.status === 401 && !_authRedirecting) {
      _authRedirecting = true;
      const next = encodeURIComponent(location.pathname + location.search);
      location.assign(rel("login") + "?next=" + next);
    }
    return res;
  });
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

// Build a download URL for a home-relative file. Opened as a top-level
// navigation (anchor), so — like preview/terminal — the tenant rides as a query
// param (a download click can't carry the X-AF-Tenant header fetch() injects).
export function downloadURL(path) {
  const u = new URL(rel("api/fs/download"));
  u.searchParams.set("path", path);
  if (selectedTenant) u.searchParams.set("tenant", selectedTenant);
  return u.toString();
}

// Upload files into a home-relative directory (multipart, field "file"). The
// global fetch wrapper adds X-AF-Tenant. Don't set Content-Type — the browser
// sets the multipart boundary. With overwrite=false a name collision returns
// HTTP 409 + {conflicts:[…]}; the caller then confirms and resends overwrite.
export function uploadFiles(dir, files, { overwrite = false } = {}) {
  const fd = new FormData();
  for (const f of files) fd.append("file", f, f.name);
  const qs = new URLSearchParams({ path: dir });
  if (overwrite) qs.set("overwrite", "1");
  return fetch(rel(`api/fs/upload?${qs.toString()}`), { method: "POST", body: fd }).then((r) =>
    r.json().then((j) => ({ status: r.status, ...j })).catch(() => ({ status: r.status })),
  );
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
