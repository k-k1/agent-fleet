// core/api/client — the console's API core (absorbed from src/api.ts at the
// docs/22 P8 swap; the parallel-entry shim is gone). Transport primitives +
// domain wrappers; features re-export their slice via features/*/api.ts.
// Shared API layer, originally ported from the Phase 1 Console — the behavioral
// contract is unchanged; only the packaging moved to modules.

import { signalAuthExpired } from "../auth/authExpired.ts";
import { signalProviderRequired } from "../auth/providerRequired.ts";
import { t, tMaybe } from "../../lib/i18n/index.ts";

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
// Pop-out tabs pin their tenant per-tab (sessionStorage) so the descriptor's
// membership sticks even if another tab later switches the shared selection —
// and, symmetrically, a pinned tab never writes the shared key, so it can't
// hijack the other tabs' tenant either. Unpinned tabs keep the shared
// localStorage behavior unchanged.
const TAB_TENANT_KEY = "af-tenant-tab";
const initialTabTenant = ((): string => {
  try {
    return sessionStorage.getItem(TAB_TENANT_KEY) || "";
  } catch {
    return "";
  }
})();
let tenantPinned = !!initialTabTenant;
let selectedTenant = initialTabTenant || localStorage.getItem("af-tenant") || "";

export const getTenant = (): string => selectedTenant;
export function setTenant(slug: string | null | undefined): void {
  selectedTenant = slug || "";
  if (tenantPinned) {
    try {
      sessionStorage.setItem(TAB_TENANT_KEY, selectedTenant);
    } catch {}
  } else {
    localStorage.setItem("af-tenant", selectedTenant);
  }
}
/** Pin this tab's tenant (pop-out boot). Runs before any fetch / tenant.init(). */
export function pinTenantForTab(slug: string): void {
  tenantPinned = true;
  setTenant(slug);
}

// --- signed-in user (layout scoping) ---
// The pane layout is persisted per (user, tenant) so a different account logging
// in from the same browser never restores the prior user's session panes (the
// stale right-pane-session bug). Held in memory only — re-resolved from
// GET /api/whoami on every boot (tenant.init) and never written to localStorage,
// so the identity itself cannot leak across accounts. Empty in dev/no-auth.
let currentUser = "";
export const getUser = (): string => currentUser;
export function setUser(id: string | null | undefined): void {
  currentUser = id || "";
}

// clearLocalState wipes every Console-owned localStorage entry — all keys carry an
// `af` prefix (tenant selection, per-(user,tenant) layouts, composer drafts, display
// settings, section fold states, caches, …). Called on logout so the next account on
// this browser starts clean and nothing of the prior user survives (the layout is
// already user-scoped, but this also clears the stale tenant selection, drafts and
// misc UI state). In-memory tenant/user are reset too, though logout navigates away
// immediately after.
export function clearLocalState(): void {
  try {
    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && k.startsWith("af")) keys.push(k);
    }
    for (const k of keys) localStorage.removeItem(k);
  } catch {
    /* private-mode / quota — best effort */
  }
  try {
    sessionStorage.removeItem(TAB_TENANT_KEY);
  } catch {}
  tenantPinned = false;
  selectedTenant = "";
  currentUser = "";
}

const _fetch = window.fetch.bind(window);
// When AUTH=oauth the Control Plane gates every request on a verified Google
// session and answers an expired/absent one with 401 on XHR. Rather than silently
// bouncing the whole page to /login (which also tears down live terminals), latch
// the expiry so the app surfaces a re-login dialog (AuthExpiredModal) that reassures
// the user their running sessions keep working and offers an explicit re-login. The
// latch is idempotent, so firing it on every subsequent 401 is harmless.
window.fetch = (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
  if (selectedTenant) {
    const h = new Headers(init.headers || {});
    if (!h.has("X-AF-Tenant")) h.set("X-AF-Tenant", selectedTenant);
    init = { ...init, headers: h };
  }
  return _fetch(input, init).then((res) => {
    if (res.status === 401) signalAuthExpired();
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

// Server error messages are language-neutral developer fallbacks. The user-facing text
// is localized via the i18n catalog under the "err.<code>" key (docs/28 / ADR 0016);
// an unmapped code falls back to the server's message. Go 側の対は
// control-plane/errcodes.go / workspace/agent/errcodes.go（docs/23 P0-3）— コードを増減・
// 変更するときは必ず両側同時に、対応する "err.<code>" キーを i18n カタログにも足す。
// isTransientErr reports whether an api() result is a gateway/transport failure the UI
// should RETRY rather than treat as real (empty/not-found) data. api() synthesizes an
// `http_<status>` code ONLY for a non-JSON/empty response — which is exactly what the CP
// writes ("workspace agent unreachable", plain-text 502) while the workspace agent is
// still booting after a WS start. App-level errors always carry their own JSON code
// (chat_conversation_not_found, …) and are terminal, so a 5xx `http_*` code is a precise
// "backend not ready yet" signal. A thrown fetch (network drop) is transient too, but
// that surfaces as a rejected promise and is handled at the call site's .catch.
export const isTransientErr = (d: unknown): boolean => {
  const code = (d as { error?: { code?: string } } | null | undefined)?.error?.code;
  return typeof code === "string" && /^http_5\d\d$/.test(code);
};

// errText turns a `res.error` ({code, message}) into a user-facing string.
export const errText = (error: ApiError | string | null | undefined): string => {
  if (error && typeof error === "object") {
    const localized = error.code ? tMaybe("err." + error.code) : undefined;
    // message も無いオブジェクトを String() すると "[object Object]" になる — 汎用文言へ。
    return localized ?? error.message ?? t("err.unknown");
  }
  return String(error ?? "");
};

// api() resolves the path against baseURI and parses JSON. Mirrors the legacy
// helper: callers handle `res.error` shapes themselves. Returns `any` on purpose —
// the response shape is per-endpoint and validated at the call site.
//
// The body is read as text first so an EMPTY or NON-JSON response never blows up
// as a raw `JSON.parse: unexpected end of data` SyntaxError. That happens for real:
// a slow request (a big `git clone`) can outlive an upstream proxy's timeout, which
// then answers the browser with an empty-body 502/504; and the CP itself writes a
// plain-text body on some gateway errors. We fold both into the shape callers already
// branch on — a 2xx with no body is an empty success ({}), any other empty/non-JSON
// body becomes {error:{code,message}} so the UI shows a real message.
// Conditional-GET layer: the CP tags JSON GET responses with a weak ETag and
// answers a matching If-None-Match with an empty 304, so an unchanged poll costs
// headers instead of a body (mobile). We remember the last {etag, parsed body}
// per URL and replay the parsed body on 304. Callers therefore MUST treat api()
// results as immutable — a 304 hands back the very same object as last time
// (which also lets JSON-compare stores skip work by reference).
const etagCache = new Map<string, { etag: string; json: unknown }>();
const ETAG_CACHE_MAX = 200; // mirror delta URLs (?since=N) churn; keep the map bounded

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const api = (path: string, opts?: RequestInit): Promise<any> => {
  const url = rel(path);
  const method = (opts?.method ?? "GET").toUpperCase();
  const cached = method === "GET" ? etagCache.get(url) : undefined;
  if (cached) {
    const headers = new Headers(opts?.headers);
    headers.set("If-None-Match", cached.etag);
    opts = { ...opts, headers };
  }
  return fetch(url, opts).then(async (r) => {
    if (r.status === 304 && cached) {
      etagCache.delete(url); // re-insert to refresh LRU position
      etagCache.set(url, cached);
      return cached.json;
    }
    const text = await r.text();
    if (text) {
      try {
        const parsed = JSON.parse(text);
        // The active tenant accepts a different sign-in method than this session
        // was minted with (docs/61 §61.9.4). Latch it so a dialog can offer the
        // re-sign-in link — otherwise every request for that tenant just fails and
        // the person is left with no way to reach it. Mirrors the 401 latch above.
        if (parsed?.error?.code === "provider_required" && selectedTenant) {
          signalProviderRequired({ tenant: selectedTenant, provider: "" });
        }
        if (method === "GET" && r.ok) {
          const etag = r.headers.get("ETag");
          if (etag) {
            etagCache.delete(url);
            etagCache.set(url, { etag, json: parsed });
            if (etagCache.size > ETAG_CACHE_MAX) {
              for (const oldest of etagCache.keys()) {
                etagCache.delete(oldest);
                break;
              }
            }
          }
        }
        return parsed;
      } catch {
        // Non-JSON body (plain-text proxy error, HTML error page): fall through
        // and synthesize a result from the HTTP status.
      }
    }
    if (r.ok) return {};
    return { error: { code: "http_" + r.status, message: text.trim() || r.status + " " + r.statusText } };
  });
};

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
export function previewURL(port: string | number, path = "/"): string {
  const base = new URL(rel(`preview/${encodeURIComponent(port)}/`));
  let u = base;
  if (path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/\\")) {
    const target = new URL(path, "http://127.0.0.1/");
    // Prefixing the normalized pathname with "." keeps it under /preview/{port}/
    // even when the target path itself resembles a scheme or contains dot segments.
    u = new URL("." + target.pathname, base);
    u.search = target.search;
    u.hash = target.hash;
  }
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

// statusJSON — the shared "{status, …json}" fold for multipart uploads and the
// fs write ops: parse the body as JSON and merge it onto the HTTP status; a
// non-JSON/empty body yields {status} alone. Callers branch on `status` (409
// conflicts etc.) — one implementation instead of a copy per endpoint.
const statusJSON = <T extends { status: number } = FsResult>(
  path: string,
  opts: RequestInit,
): Promise<T> =>
  fetch(rel(path), opts).then((r) =>
    r
      .json()
      .then((j) => ({ status: r.status, ...j }) as T)
      .catch(() => ({ status: r.status }) as T),
  );

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
  return statusJSON(`api/fs/upload?${qs.toString()}`, { method: "POST", body: fd });
}

// --- semantic session turn ops (docs/27 P1.5) ---
// The chat mirror's send / steer / interrupt go through POST /turn, which the
// Agent adapts to the session's driver: tui = the same tmux typing as before,
// managed = turn/start・turn/steer・turn/interrupt RPC（P2/P3）. Returns ok.
// Falls back to the legacy /input body when the Agent predates /turn（フリート
// 再ビルドのラグで新 Console↔旧 Agent の版ずれが実際に起きる）.
export type TurnOp = "start" | "steer" | "interrupt";
// TurnResult: ok=false のとき message はユーザーに見せられる却下理由（question_pending
// 等）。呼び出し側はこれをトーストし、消した楽観 echo / 下書きの復元を判断する —
// 却下を握りつぶすと「送れたように見えて消えた」になる。
export interface TurnResult {
  ok: boolean;
  message?: string;
}
export async function sessionTurn(
  session: string,
  op: TurnOp,
  prompt?: string,
  // attachments: managed セッション専用のファイル添付（保存済み絶対パス）。driver が
  // API 添付（opencode は v1 file part）へ変換する（docs/27 §10.2-3）。tui では
  // Console が従来どおりパスをプロンプト本文へ織り込むので渡さない。
  attachments?: string[],
): Promise<TurnResult> {
  const fail = (e: unknown): TurnResult => ({ ok: false, message: errText(e as ApiError) || t("err.send_failed") });
  const body: Record<string, unknown> = op === "interrupt" ? { op } : { op, prompt };
  if (op !== "interrupt" && attachments?.length) body.attachments = attachments;
  const r = await apiJSON(
    `api/sessions/${encodeURIComponent(session)}/turn`,
    "POST",
    body,
  ).catch(() => ({ error: { message: t("err.network") } }));
  const err = r?.error as ApiError | undefined;
  if (!err) return { ok: true };
  const code = String(err.code || "");
  if (code === "http_404" || code === "http_405") {
    const legacy = op === "interrupt" ? { keys: ["Escape"] } : { prompt };
    const r2 = await apiJSON(`api/sessions/${encodeURIComponent(session)}/input`, "POST", legacy).catch(
      () => ({ error: { message: t("err.network") } }),
    );
    return r2?.error ? fail(r2.error) : { ok: true };
  }
  return fail(err);
}

// sessionPlanRespond decides a pending ExitPlanMode approval (claude TUI).
//
// Why not /input: while the approval dialog is up, typed text is SWALLOWED by the
// modal and the trailing Enter confirms its highlighted row — always an approval.
// The Agent's /plan-respond instead interrupts the dialog, waits for the composer to
// come back, and only then types the feedback; `delivered` reports whether that
// second half actually happened (false = the text never reached the session, so the
// caller must not treat its comments as sent).
export interface PlanRespondResult {
  ok: boolean;
  message?: string;
  code?: string;
  delivered?: boolean;
}
export async function sessionPlanRespond(
  session: string,
  decision: "approve" | "reject",
  feedback?: string,
): Promise<PlanRespondResult> {
  const r = await apiJSON(`api/sessions/${encodeURIComponent(session)}/plan-respond`, "POST", {
    decision,
    ...(feedback ? { feedback } : {}),
  }).catch(() => ({ error: { message: t("err.network") } }));
  const err = r?.error as ApiError | undefined;
  if (err) return { ok: false, message: errText(err) || t("err.send_failed"), code: String(err.code || "") };
  // No feedback asked for ⇒ nothing to deliver, so report delivered.
  return { ok: true, delivered: feedback ? r?.feedback_delivered === true : true, message: r?.hint };
}

// 持ち越した対話（docs/75）— 停止時に画面に出ていた質問 / プラン / 許可。モーダルは
// 再開しても戻らない（未応答の tool_use は会話木から外れる）ので、これは**キー列で
// 答えるものではない**: 回答は Agent が再開後に文章として配達する。
import type { Question } from "../../features/mirror/transcript/types.ts";

export interface CarriedInteraction {
  kind: "question" | "plan" | "permission";
  capturedAt?: string;
  reason?: string;
  questions?: Question[];
  plan?: string;
  permission?: string;
  text?: string;
}

export interface CarriedAnswerInput {
  labels?: string[];
  notes?: string;
}

// sessionCarriedAnswer は持ち越しへの回答を送る。停止中の Workspace が相手になりうる
// ので、CP はこの 1 本だけ auto-start を通す（利用者の明示操作＝「今から使う」）。
export async function sessionCarriedAnswer(
  session: string,
  body: {
    decision: "answer" | "approve" | "reject" | "continue" | "discard";
    answers?: CarriedAnswerInput[];
    feedback?: string;
  },
): Promise<{ ok: boolean; message?: string }> {
  const r = await apiJSON(`api/sessions/${encodeURIComponent(session)}/carried-answer`, "POST", body).catch(
    () => ({ error: { message: t("err.network") } }),
  );
  const err = r?.error as ApiError | undefined;
  if (err) return { ok: false, message: errText(err) || t("err.send_failed") };
  return { ok: true };
}

// One question's structured answer inside an Interaction reply (docs/27 §5).
// A multi-question form replies with one entry per question, in order.
export interface InteractionAnswer {
  text?: string; // 自由入力
  options?: number[]; // 選択肢 index（複数選択は複数個）
}

// sessionRespond answers a MANAGED session's pending question by interaction id.
// TUI sessions keep the keys/seq path — their modal is driven by navigation and
// has no "answer by id" surface (the Agent answers respond_unsupported for them).
// 却下理由（id 不明・driver 未実装など）を捨てず TurnResult 形で返す — 呼び出し側が
// 汎用文言の代わりにトーストできる（sessionTurn と同じ契約）。
export const sessionRespond = (
  session: string,
  id: string,
  answers: InteractionAnswer[],
): Promise<TurnResult> =>
  apiJSON(`api/sessions/${encodeURIComponent(session)}/respond`, "POST", {
    id,
    decision: "answer",
    answers,
  }).then((r) => (r?.error ? { ok: false, message: errText(r.error as ApiError) } : { ok: true }));

// sessionSettings updates a MANAGED session's dynamic thread settings（docs/27
// §9.4-3: 稼働中セッションのモデル / effort / モード変更）。空フィールドは
// 「変更しない」。TUI セッションはそれぞれの CLI 内の設定操作を使う。
export interface ManagedThreadSettings {
  model: string;
  effort: string;
  mode: "plan" | "normal" | "";
  dynamicModel?: boolean;
  dynamicEffort?: boolean;
  dynamicMode?: boolean;
}

export const sessionSettingsGet = (session: string): Promise<ManagedThreadSettings & { error?: ApiError }> =>
  api(`api/sessions/${encodeURIComponent(session)}/settings`);

export interface SettingsResult {
  ok: boolean;
  message?: string;
  settings?: ManagedThreadSettings;
}

export const sessionSettings = async (
  session: string,
  s: {
    model?: string;
    effort?: string;
    mode?: "plan" | "normal";
    clearModel?: boolean;
    clearEffort?: boolean;
  },
): Promise<SettingsResult> => {
  const r = await apiJSON(`api/sessions/${encodeURIComponent(session)}/settings`, "POST", s).catch(() => ({
    error: { message: t("err.network") },
  }));
  if (r?.error) return { ok: false, message: errText(r.error) || t("err.settings_change_failed") };
  return {
    ok: true,
    settings: {
      model: typeof r.model === "string" ? r.model : "",
      effort: typeof r.effort === "string" ? r.effort : "",
      mode: r.mode === "plan" || r.mode === "normal" ? r.mode : "",
    },
  };
};

// Upload one pasted image to a session (multipart, field "file"). Returns the saved
// absolute path + basename so the composer can reference it in the prompt (claude reads
// it with the Read tool) and preview it via GET api/sessions/{name}/pasted/{name}.
export function pasteImage(
  session: string,
  file: File,
): Promise<{ status: number; path?: string; name?: string; error?: unknown }> {
  const fd = new FormData();
  fd.append("file", file, file.name || "pasted.png");
  return statusJSON(`api/sessions/${encodeURIComponent(session)}/paste-image`, {
    method: "POST",
    body: fd,
  });
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
  return statusJSON(`api/chat/conversations/${encodeURIComponent(convId)}/paste-image`, {
    method: "POST",
    body: fd,
  });
}

// File-management ops (create / rename / delete). Each returns {status, …json}
// so callers can branch on 409 (exists) etc. The fetch wrapper adds X-AF-Tenant.
const fsWrite = (path: string, opts: RequestInit): Promise<FsResult> => statusJSON(path, opts);
const q = encodeURIComponent;
export const fsMkdir = (path: string) => fsWrite(`api/fs/mkdir?path=${q(path)}`, { method: "POST" });
export const fsNewFile = (path: string) => fsWrite(`api/fs/newfile?path=${q(path)}`, { method: "POST" });
export const fsRename = (from: string, to: string) =>
  fsWrite(`api/fs/rename?from=${q(from)}&to=${q(to)}`, { method: "POST" });
export const fsDelete = (path: string) => fsWrite(`api/fs/delete?path=${q(path)}`, { method: "DELETE" });

// Recursive filename search under a home-relative root (rg-backed). Returns
// home-relative file paths whose path (relative to root) matches the query,
// plus `truncated` when the result cap was hit. Never rejects — a proxy/agent
// error folds into an empty result so the tree filter degrades gracefully.
export const fsSearch = (root: string, query: string): Promise<{ results: string[]; truncated: boolean }> =>
  api(`api/fs/search?path=${q(root)}&q=${q(query)}`).then((r) => ({
    results: Array.isArray(r?.results) ? (r.results as string[]) : [],
    truncated: !!r?.truncated,
  }));

// --- assistant chat (docs/19) ---
// Headless-CLI LLM chat/translation. Thin wrappers over the /api/chat/* endpoints;
// callers own the response shape (Conversation / ConversationMeta in types/chat).
import type { Conversation, ConversationMeta, ChatStep } from "../../types/chat.ts";
import type { SessionKind } from "../../types/session.ts";
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
// Switch the backend CLI of an existing conversation (docs/19). The agent-priority setting
// only applies to NEW conversations, so this is the way to move a thread in progress: the
// backend re-pins the conversation, re-resolves the model from that CLI's row in settings,
// and replays the history the new backend hasn't seen with the next message. 409
// (conversation_busy) while a turn is running — stop it first.
export const chatSetAgent = (id: string, agent: string): Promise<Conversation & { error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}`, "PATCH", { agent });
// Preview-only AI title suggestion (mirrors the session /title/suggest endpoint): never
// writes the conversation's title, just returns a candidate for the rename dialog.
export const chatSuggestTitle = (id: string): Promise<{ suggestedTitle?: string; error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/title/suggest`, "POST");
// Preview-only LLM reply suggestions v2 (chat_suggest_reply.go): context-aware short
// reply candidates for the composer's ✨ button. Never writes anything.
export const chatSuggestReplies = (id: string): Promise<{ suggestions?: string[]; error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/suggest-replies`, "POST");
export const chatDelete = (id: string): Promise<Response> =>
  raw(`api/chat/conversations/${encodeURIComponent(id)}`, { method: "DELETE" });
// Stop an in-flight assistant turn. The streaming turn is detached from its request
// connection (survives a reload), so aborting the fetch no longer cancels it — this does.
export const chatStop = (id: string): Promise<Response> =>
  raw(`api/chat/conversations/${encodeURIComponent(id)}/stop`, { method: "POST" });
// Compact the conversation's context (docs/33 第2段): the backend summarizes the current
// provider session, resets the resume handles, and carries only the summary into a fresh
// session on the next turn. Returns the updated conversation (or {error}).
export const chatCompact = (id: string): Promise<Conversation & { error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/compact`, "POST");
// 作業計画（docs/33 第5段）: 圧縮を跨いで原文のまま運ばれる枠。set は手編集（空文字で
// クリア）、refresh は直近の会話から計画を引き直す一発ヘッドレス（会話のプロバイダ
// セッションは使わないので、更新のためにコンテキストは増えない）。どちらも更新済みの
// 会話を返す。
export const chatSetPlan = (id: string, plan: string): Promise<Conversation & { error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/plan`, "PUT", { plan });
export const chatRefreshPlan = (id: string): Promise<Conversation & { error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/plan/refresh`, "POST");
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
  onAgent?: (agent: SessionKind) => void;
  onDelta?: (text: string) => void;
  onStep?: (step: ChatStep) => void; // a completed 作業過程 item (narration + tools before a tool call)
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
    h.onError?.(t("err.send_failed"));
    return;
  }
  if (!res.ok || !res.body) {
    let msg = t("err.send_failed");
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
      let obj: {
        agent?: SessionKind;
        delta?: string;
        step?: ChatStep;
        error?: unknown;
        done?: boolean;
        conversation?: Conversation;
      };
      try {
        obj = JSON.parse(line);
      } catch {
        continue;
      }
      if (obj.agent) h.onAgent?.(obj.agent);
      else if (typeof obj.delta === "string") h.onDelta?.(obj.delta);
      else if (obj.step) h.onStep?.(obj.step);
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

// --- 削除ロック（docs/45） ---
// セッション / 作業コピー（worktree 含む）/ アシスタント会話を削除保護に固定・解除する。
// 保護そのものは Agent の REST 層で効くので、ここを通らない削除（オペレーターの MCP
// ツール・ブリッジ）も同じ 403 locked で止まる。UI 側は locked を見て削除項目を無効化
// するだけ — 判定の正は常にサーバー。
export const sessionSetLock = (name: string, locked: boolean): Promise<{ locked?: boolean; error?: ApiError }> =>
  apiJSON(`api/sessions/${encodeURIComponent(name)}/lock`, "POST", { locked });
// 停止しないピン（docs/75）: アイドル自動停止（tier1 のセッション halt / tier2 の
// Workspace 停止）から、期限付きでこのセッションを守る。hours<=0 で解除。
// **時刻で切れる**のが本体 — 消し忘れたピンは閉じ忘れた端末タブと同じで、黙って
// 課金し続けるものになる。延長は押し直す。
export const sessionKeepAwake = (
  name: string,
  hours: number,
): Promise<{ keepAwakeUntil?: string; error?: ApiError }> =>
  apiJSON(`api/sessions/${encodeURIComponent(name)}/keep-awake`, "POST", { hours });
export const repoSetLock = (name: string, locked: boolean): Promise<{ locked?: boolean; error?: ApiError }> =>
  apiJSON(`api/repos/${encodeURIComponent(name)}/lock`, "POST", { locked });
export const chatSetLock = (id: string, locked: boolean): Promise<{ locked?: boolean; error?: ApiError }> =>
  apiJSON(`api/chat/conversations/${encodeURIComponent(id)}/lock`, "POST", { locked });

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

// --- session skills (mirror スキルピッカー, docs/50 / ADR0034) ---
// Invocable skills/commands for a RUNNING session. Sources are kind-specific
// (claude/codex/opencode: filesystem conventions; cursor: the CLI-advertised
// ACP list) — the Agent resolves them; unsupported kinds answer an empty list.
export interface SessionSkill {
  name: string; // invocation name, no "/" or "$" prefix
  description?: string;
  argumentHint?: string;
  source: "project" | "user" | "cli";
  type: "skill" | "command";
  // Native invocation: exact string to insert into the composer ("/name " / "$name ").
  // Empty for foreign entries (below).
  invoke?: string;
  // Cross-skill injection (docs/50 §8): a skill from ANOTHER convention this kind's
  // CLI won't discover natively. The composer turns it into a "read {path} and follow
  // its instructions" prompt — plain text, so it works on any kind/driver.
  path?: string; // repo-relative SKILL.md path
  origin?: string; // convention dir (".claude" | ".codex" | ".agents") — shown as a badge
}
export const sessionSkills = (session: string): Promise<{ skills: SessionSkill[] }> =>
  api(`api/sessions/${encodeURIComponent(session)}/skills`);

// --- memo queue (docs/21) ---
// Per-membership notes accumulated across devices, then flushed to a session as one
// message. Persisted in the Control Plane (membership-scoped), so they sync between
// devices; there is no server push, so callers refetch on open + poll while open.
import type { Memo, MemoInput, MemoPatch, MemoCategory, MemoCategoryInput, MemoCategoryPatch } from "../../types/memo.ts";

export const memoList = (): Promise<Memo[]> => api("api/memos");
export const memoCreate = (input: MemoInput): Promise<Memo> =>
  apiJSON("api/memos", "POST", input);
export const memoUpdate = (id: string, patch: MemoPatch): Promise<Memo> =>
  apiJSON(`api/memos/${encodeURIComponent(id)}`, "PATCH", patch);
export const memoDelete = (id: string): Promise<Response> =>
  raw(`api/memos/${encodeURIComponent(id)}`, { method: "DELETE" });

// Upload one memo image attachment (multipart, field "file"). Membership-scoped:
// stored in the workspace container under memo-images with no session/conv key, so a
// memo shared from any device can carry it and flush into whichever session is chosen.
// Returns the saved absolute path + basename — the flush references the path (the agent
// opens it with its Read tool) and the Console previews the basename via memoImageURL.
export function memoPasteImage(
  file: File,
): Promise<{ status: number; path?: string; name?: string; error?: unknown }> {
  const fd = new FormData();
  fd.append("file", file, file.name || "pasted.png");
  return statusJSON("api/memos/paste-image", { method: "POST", body: fd });
}

// Relative URL of a stored memo image by basename. The endpoint requires the tenant
// header (fetch injects X-AF-Tenant), so a bare <img src> can't reach it — fetch it as a
// blob via raw() for an object URL, as MirrorView's PastedThumb does.
export const memoImageURL = (name: string): string =>
  `api/memos/images/${encodeURIComponent(name)}`;

// Best-effort disk hygiene: tell the workspace agent which memo-image basenames are
// still referenced by a memo; it unlinks every other stored image. Fire-and-forget —
// a failure (container stopped, network) just leaves stale files for the next sweep.
export const memoImageGC = (keep: string[]): Promise<Response> =>
  rawJSON("api/memos/images/gc", "POST", { keep });
// Flush concatenates the selected memos (by id) into one message, sends it once to
// the session, and stamps them sent. `text`, when given, is sent verbatim instead of the
// server-composed message (the send modal lets the user edit it first). Returns
// {sent, sentAt, ids} or {error}.
export const memoFlush = (
  sessionName: string,
  ids: string[],
  text?: string,
): Promise<{ sent?: number; sentAt?: string; ids?: string[]; error?: ApiError }> =>
  apiJSON("api/memos/flush", "POST", { sessionName, ids, ...(text ? { text } : {}) });

// First-class categories (docs/21 UI刷新): add empty, rename (cascades onto memos),
// reorder by drag. Membership-scoped like the memos.
export const memoCategoryList = (): Promise<MemoCategory[]> => api("api/memo-categories");
export const memoCategoryCreate = (input: MemoCategoryInput): Promise<MemoCategory> =>
  apiJSON("api/memo-categories", "POST", input);
export const memoCategoryUpdate = (id: string, patch: MemoCategoryPatch): Promise<MemoCategory> =>
  apiJSON(`api/memo-categories/${encodeURIComponent(id)}`, "PATCH", patch);
export const memoCategoryDelete = (id: string): Promise<Response> =>
  raw(`api/memo-categories/${encodeURIComponent(id)}`, { method: "DELETE" });

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
