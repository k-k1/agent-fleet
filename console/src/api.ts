// Shared API layer. Ported from the Phase 1 Console (console/legacy-phase1/app.js)
// — the behavioral contract is unchanged; only the packaging moved to modules.

// Resolve URLs relative to where the Console is mounted, so it works both at the
// host root (http://localhost:8099/) and behind a path-stripping proxy (Tailscale
// Funnel + Caddy at /agent-fleet/). Uses the trailing-slash baseURI.
export const rel = (p: string): string => new URL(p, document.baseURI).toString();

// --- tenant selection (P3-2) ---
// The active tenant is sent on every request as X-AF-Tenant so the Control Plane
// resolves the right per-membership workspace. We inject the header globally by
// wrapping window.fetch so every request — including ones we don't author here —
// carries it. The terminal WebSocket can't send headers, so attach() appends
// &tenant=<slug> to its URL instead (see term.js).
let selectedTenant = localStorage.getItem("af-tenant") || "";

export const getTenant = (): string => selectedTenant;
export function setTenant(slug: string | null | undefined): void {
  selectedTenant = slug || "";
  localStorage.setItem("af-tenant", selectedTenant);
}

const _fetch = window.fetch.bind(window);
// When AUTH=oauth the Control Plane gates every request on a verified Google
// session and answers an expired/absent one with 401 on XHR. Bounce the whole
// page to the login landing so the user can re-authenticate (a top-level nav,
// which the CP turns into the Google redirect). Guarded so we redirect once.
let _authRedirecting = false;
window.fetch = (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
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

// A server error payload: a stable machine `code` plus a developer-facing message.
export interface ApiError {
  code?: string;
  message?: string;
}

// The common file-management result: an HTTP status plus whatever JSON the
// endpoint returned (conflicts, error, etc.). Callers branch on `status`.
export type FsResult = { status: number } & Record<string, unknown>;

// Server error messages are language-neutral developer fallbacks. The user-facing
// text is localized here, keyed by the stable error `code`. Add a locale layer over
// this map when i18n lands; unknown codes fall back to the server's message.
const ERR_TEXT: Record<string, string> = {
  quota_sessions:
    "同時に稼働できるセッション数の上限に達しています。稼働中のセッションをどれか停止してから作成してください。",
};

// errText turns a `res.error` ({code, message}) into a user-facing string.
export const errText = (error: ApiError | string | null | undefined): string =>
  (error && typeof error === "object" && error.code && ERR_TEXT[error.code]) ||
  (error && typeof error === "object" && error.message) ||
  String(error ?? "");

// api() resolves the path against baseURI and parses JSON. Mirrors the legacy
// helper: callers handle `res.error` shapes themselves. Returns `any` on purpose —
// the response shape is per-endpoint and validated at the call site.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const api = (path: string, opts?: RequestInit): Promise<any> =>
  fetch(rel(path), opts).then((r) => r.json());

// apiJSON is a convenience for the common "POST/PUT JSON body" shape.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const apiJSON = (path: string, method: string, body?: unknown): Promise<any> =>
  api(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

// raw() returns the Response (not parsed) for callers that need r.ok / status.
export const raw = (path: string, opts?: RequestInit): Promise<Response> => fetch(rel(path), opts);
export const rawJSON = (path: string, method: string, body?: unknown): Promise<Response> =>
  raw(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

// Build the preview URL for a port the user started a service on inside the
// container (Spring Boot, dev server, ...). Opened as a top-level navigation, so
// the tenant rides as a query param — a new tab can't carry the X-AF-Tenant
// header that fetch() injects. The CP resolves tenant from this fallback.
export function previewURL(port: string | number): string {
  const u = new URL(rel(`preview/${encodeURIComponent(port)}/`));
  if (selectedTenant) u.searchParams.set("tenant", selectedTenant);
  return u.toString();
}

// Build the opencode web UI URL (the per-workspace pk-webui served under /ocweb/,
// not /api). Opened as a top-level navigation in a new tab, so the tenant rides as
// a query param — the CP resolves it from this fallback (a new tab can't carry the
// X-AF-Tenant header fetch() injects).
export function ocwebURL(): string {
  const u = new URL(rel("ocweb/"));
  if (selectedTenant) u.searchParams.set("tenant", selectedTenant);
  return u.toString();
}

// Build a download URL for a home-relative file. Opened as a top-level
// navigation (anchor), so — like preview/terminal — the tenant rides as a query
// param (a download click can't carry the X-AF-Tenant header fetch() injects).
export function downloadURL(path: string): string {
  const u = new URL(rel("api/fs/download"));
  u.searchParams.set("path", path);
  if (selectedTenant) u.searchParams.set("tenant", selectedTenant);
  return u.toString();
}

// Upload files into a home-relative directory (multipart, field "file"). The
// global fetch wrapper adds X-AF-Tenant. Don't set Content-Type — the browser
// sets the multipart boundary. With overwrite=false a name collision returns
// HTTP 409 + {conflicts:[…]}; the caller then confirms and resends overwrite.
export function uploadFiles(
  dir: string,
  files: Iterable<File>,
  { overwrite = false }: { overwrite?: boolean } = {},
): Promise<FsResult> {
  const fd = new FormData();
  for (const f of files) fd.append("file", f, f.name);
  const qs = new URLSearchParams({ path: dir });
  if (overwrite) qs.set("overwrite", "1");
  return fetch(rel(`api/fs/upload?${qs.toString()}`), { method: "POST", body: fd }).then((r) =>
    r
      .json()
      .then((j) => ({ status: r.status, ...j }))
      .catch(() => ({ status: r.status })),
  );
}

// Upload one pasted image to a session (multipart, field "file"). Returns the saved
// absolute path + basename so the composer can reference it in the prompt (claude reads
// it with the Read tool) and preview it via GET api/sessions/{name}/pasted/{name}.
export function pasteImage(
  session: string,
  file: File,
): Promise<{ status: number; path?: string; name?: string; error?: unknown }> {
  const fd = new FormData();
  fd.append("file", file, file.name || "pasted.png");
  return fetch(rel(`api/sessions/${encodeURIComponent(session)}/paste-image`), {
    method: "POST",
    body: fd,
  }).then((r) =>
    r
      .json()
      .then((j) => ({ status: r.status, ...j }))
      .catch(() => ({ status: r.status })),
  );
}

// File-management ops (create / rename / delete). Each returns {status, …json}
// so callers can branch on 409 (exists) etc. The fetch wrapper adds X-AF-Tenant.
const fsWrite = (path: string, opts: RequestInit): Promise<FsResult> =>
  fetch(rel(path), opts).then((r) =>
    r
      .json()
      .then((j) => ({ status: r.status, ...j }))
      .catch(() => ({ status: r.status })),
  );
const q = encodeURIComponent;
export const fsMkdir = (path: string) => fsWrite(`api/fs/mkdir?path=${q(path)}`, { method: "POST" });
export const fsNewFile = (path: string) => fsWrite(`api/fs/newfile?path=${q(path)}`, { method: "POST" });
export const fsRename = (from: string, to: string) =>
  fsWrite(`api/fs/rename?from=${q(from)}&to=${q(to)}`, { method: "POST" });
export const fsDelete = (path: string) => fsWrite(`api/fs/delete?path=${q(path)}`, { method: "DELETE" });

// Build the terminal WebSocket URL for a session under the current mount, with
// the tenant carried as a query param (headers aren't available on WS).
export function wsURL(session: string): URL {
  const u = new URL(rel("ws/terminal"));
  u.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  u.search =
    `?session=${encodeURIComponent(session)}` +
    (selectedTenant ? `&tenant=${encodeURIComponent(selectedTenant)}` : "");
  return u;
}
