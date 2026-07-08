// core/api/client — the console's API core (absorbed from src/api.ts at the
// docs/22 P8 swap; the parallel-entry shim is gone). Transport primitives +
// domain wrappers; features re-export their slice via features/*/api.ts.
// Shared API layer, originally ported from the Phase 1 Console — the behavioral
// contract is unchanged; only the packaging moved to modules.

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
  sessions_running:
    "この作業コピーでは稼働中のセッションがあります。切り替えると足元の作業ツリーが入れ替わり壊れるため、ここでは切り替えできません。ブランチは別の作業コピーとして開いてください。",
  sessions_running_delete:
    "この作業コピーでは稼働中のセッションがあります。削除すると足元の作業ディレクトリが消えて壊れるため、先にそれらのセッションを停止してください。",
  worktree_dirty:
    "この worktree には未コミット/未pushの変更があります。強制削除すると失われます。",
  has_worktrees:
    "この作業コピーには派生した worktree がぶら下がっています。先に worktree 側を削除してください。",
  worktree_remove_failed: "worktree の削除に失敗しました。",
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

// Upload one pasted image to an assistant chat (multipart, field "file"). Mirrors
// pasteImage but keyed by conversation id: the chat's claude opens the returned absolute
// path with its Read tool. Preview via GET api/chat/conversations/{id}/pasted/{name}.
export function chatPasteImage(
  convId: string,
  file: File,
): Promise<{ status: number; path?: string; name?: string; error?: unknown }> {
  const fd = new FormData();
  fd.append("file", file, file.name || "pasted.png");
  return fetch(rel(`api/chat/conversations/${encodeURIComponent(convId)}/paste-image`), {
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

// --- assistant chat (docs/19) ---
// Headless-CLI LLM chat/translation. Thin wrappers over the /api/chat/* endpoints;
// callers own the response shape (Conversation / ConversationMeta in types/chat).
import type { Conversation, ConversationMeta } from "../../types/chat.ts";
import type { Assistant, AssistantInput } from "../../types/assistant.ts";

export const chatList = (): Promise<{ conversations: ConversationMeta[] }> =>
  api("api/chat/conversations");
// Create a conversation from an assistant template (docs/19 Q2): the Agent snapshots
// the assistant's agent/model/persona/tools/knowledge onto the new thread. Optionally
// attach a file/dir (docs/19 Phase C): its dir is added to knowledge and the response
// carries a `seed` prompt (composed with the absolute path) to prefill the composer.
export interface ChatCreateOpts {
  attachPath?: string; // browse-root-relative file/dir to hand to the assistant
  seedVerb?: "translate" | "summarize" | ""; // shapes the seed prompt
}
export const chatCreate = (
  assistantId: string,
  title?: string,
  opts?: ChatCreateOpts,
): Promise<Conversation> =>
  apiJSON("api/chat/conversations", "POST", {
    assistant_id: assistantId,
    title,
    attach_path: opts?.attachPath,
    seed_verb: opts?.seedVerb,
  });
export const chatGet = (id: string): Promise<Conversation> =>
  api(`api/chat/conversations/${encodeURIComponent(id)}`);
// Rename a conversation's display title (docs/19).
export const chatRename = (id: string, title: string): Promise<Conversation> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}`, "PATCH", { title });
export const chatDelete = (id: string): Promise<Response> =>
  raw(`api/chat/conversations/${encodeURIComponent(id)}`, { method: "DELETE" });
// Send returns the assistant message + the updated conversation, or {error} on failure.
export const chatSend = (
  id: string,
  content: string,
): Promise<{ conversation?: Conversation; error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/messages`, "POST", { content });

// Streaming send (Phase B): POST the message and read a Server-Sent Events stream
// of token deltas. Frames are `data: {json}` separated by blank lines; json is one
// of {delta}, {error}, or {done, conversation}. onDelta fires per chunk; onDone with
// the final saved conversation; onError with a message. Resolves when the stream ends.
export interface ChatStreamHandlers {
  onDelta?: (text: string) => void;
  onDone?: (conv: Conversation | undefined) => void;
  onError?: (msg: string) => void;
}
export async function chatStream(
  id: string,
  content: string,
  h: ChatStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response;
  try {
    res = await fetch(rel(`api/chat/conversations/${encodeURIComponent(id)}/stream`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content }),
      signal,
    });
  } catch {
    if (signal?.aborted) return; // user stopped the turn — not an error
    h.onError?.("送信に失敗しました");
    return;
  }
  if (!res.ok || !res.body) {
    let msg = "送信に失敗しました";
    try {
      const j = await res.json();
      msg = errText(j?.error) || msg;
    } catch {
      /* non-JSON error body */
    }
    h.onError?.(msg);
    return;
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  const drain = () => {
    let idx: number;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const line = frame.startsWith("data:") ? frame.slice(5).trim() : frame.trim();
      if (!line) continue;
      let obj: { delta?: string; error?: unknown; done?: boolean; conversation?: Conversation };
      try {
        obj = JSON.parse(line);
      } catch {
        continue;
      }
      if (typeof obj.delta === "string") h.onDelta?.(obj.delta);
      else if (obj.error != null) h.onError?.(errText(obj.error as ApiError) || String(obj.error));
      else if (obj.done) h.onDone?.(obj.conversation);
    }
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      drain();
    }
    buf += dec.decode();
    drain();
  } catch {
    if (signal?.aborted) return; // user stopped mid-stream — silent, keep the partial
    // otherwise the connection dropped; leave it (onDone just won't fire)
  }
}

// --- assistant templates (docs/19 Q2) ---
// Configurable chat personas. Builtins are code-injected on the Agent (not editable);
// user-defined ones support create/update/delete.
export const assistantList = (): Promise<{ assistants: Assistant[] }> =>
  api("api/assistants");
export const assistantGet = (id: string): Promise<Assistant> =>
  api(`api/assistants/${encodeURIComponent(id)}`);
export const assistantCreate = (input: AssistantInput): Promise<Assistant> =>
  apiJSON("api/assistants", "POST", input);
export const assistantUpdate = (id: string, input: AssistantInput): Promise<Assistant> =>
  apiJSON(`api/assistants/${encodeURIComponent(id)}`, "PUT", input);
export const assistantDelete = (id: string): Promise<Response> =>
  raw(`api/assistants/${encodeURIComponent(id)}`, { method: "DELETE" });

// --- launch prompt templates (repo 起動 modal) ---
// Aggregated read-only from the working copy: .claude/commands, .claude/skills,
// .agent-fleet/launch-prompts.md. Bodies are verbatim; the modal does {{repo}}/
// {{branch}}/{{path}} expansion and adds a client-side 履歴 group (localStorage).
export interface PromptTemplateItem {
  id: string;
  label: string;
  body: string;
}
export interface PromptTemplateGroup {
  source: string; // command | skill | file
  label: string;
  items: PromptTemplateItem[];
}
export const repoPromptTemplates = (name: string): Promise<{ groups: PromptTemplateGroup[] }> =>
  api(`api/repos/${encodeURIComponent(name)}/prompt-templates`);

// --- memo queue (docs/21) ---
// Per-membership notes accumulated across devices, then flushed to a session as one
// message. Persisted in the Control Plane (membership-scoped), so they sync between
// devices; there is no server push, so callers refetch on open + poll while open.
import type { Memo, MemoInput, MemoPatch } from "../../types/memo.ts";

export const memoList = (): Promise<Memo[]> => api("api/memos");
export const memoCreate = (input: MemoInput): Promise<Memo> =>
  apiJSON("api/memos", "POST", input);
export const memoUpdate = (id: string, patch: MemoPatch): Promise<Memo> =>
  apiJSON(`api/memos/${encodeURIComponent(id)}`, "PATCH", patch);
export const memoDelete = (id: string): Promise<Response> =>
  raw(`api/memos/${encodeURIComponent(id)}`, { method: "DELETE" });
// Flush concatenates the selected memos (by id) into one message, sends it once to
// the session, and stamps them sent. Returns {sent, sentAt, ids} or {error}.
export const memoFlush = (
  sessionName: string,
  ids: string[],
): Promise<{ sent?: number; sentAt?: string; ids?: string[]; error?: ApiError }> =>
  apiJSON("api/memos/flush", "POST", { sessionName, ids });

// --- assistant one-shot ask (docs/21 メモ整理) ---
// Stateless advisory turn (tools off) used to tidy up + auto-categorize memos. The
// assistant is asked to return JSON; the caller parses and previews before applying.
export const askAssistant = (
  prompt: string,
  assistant?: string,
): Promise<{ assistant?: string; reply?: string; error?: ApiError }> =>
  apiJSON("api/chat/ask", "POST", { prompt, assistant });

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
