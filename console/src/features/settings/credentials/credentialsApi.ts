// credentialsApi — typed wrappers for the endpoints the Credentials cards call (sign-in flows,
// API keys, the llama.cpp connection, the unusable-credential cleanup and the live kind catalogue).
// The paths stay string literals (control-plane/console_routes_test.go reads them as text); a
// caller gets `Rep<T>` and tells the outcomes apart with `res.error`.
//
// api() can resolve with a JSON `null` body. Every wrapper keeps `| null` in its type and the
// caller keeps the null handling it had (`!res || res.error`, `res && res.error`).
import { api, apiJSON, type ApiError } from "../../../core/api/client.ts";
import type { Ack, Rep } from "../workspace/workspaceApi.ts";

/** A browser or device sign-in the Agent started (`start` answers). `url` is missing on a refusal
 *  that carries no `error`; the callers test it. */
export type FlowStart = { url?: string; flow_id: string; user_code?: string; instructions?: string };

/** One poll of a running sign-in: `connected` ends it well, `failed` (with `detail`) or an opencode
 *  `status` of failed / expired ends it badly. */
export type FlowPoll = { error?: ApiError; connected?: boolean; failed?: boolean; detail?: string; status?: string; message?: string };

/** The llama.cpp reachability check (`build_info` / `n_ctx` / `models` only on success). */
export type LcppCheck = { build_info?: string; n_ctx?: number; models?: string[] };

const post = (path: string): Promise<Rep<FlowStart> | null> => api(path, { method: "POST" });
const poll = (path: string, flowId: string): Promise<FlowPoll | null> =>
  apiJSON(path, "POST", { flow_id: flowId });

// --- Claude / Agy: sign in in the browser, paste the code back ---------------------------------
export const startClaudeLogin = (): Promise<Rep<FlowStart> | null> => post("api/connections/claude/start");
export const completeClaudeLogin = (flowId: string, code: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/claude/complete", "POST", { flow_id: flowId, code });
export const startAgyLogin = (method: string): Promise<Rep<FlowStart> | null> =>
  apiJSON("api/connections/agy/start", "POST", { method });
export const completeAgyLogin = (flowId: string, code: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/agy/complete", "POST", { flow_id: flowId, code });

// --- Device / polling flows --------------------------------------------------------------------
export const startMuseLogin = (): Promise<Rep<FlowStart> | null> => post("api/connections/muse/start");
export const pollMuseLogin = (flowId: string): Promise<FlowPoll | null> =>
  poll("api/connections/muse/poll", flowId);
export const putMuseApiKey = (key: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/muse/api-key", "POST", { key });
export const startCodexDeviceLogin = (): Promise<Rep<FlowStart> | null> => post("api/connections/codex/device/start");
export const pollCodexDeviceLogin = (flowId: string): Promise<FlowPoll | null> =>
  poll("api/connections/codex/device/poll", flowId);
export const putCodexApiKey = (key: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/codex/api-key", "POST", { key });
export const startKiroLogin = (): Promise<Rep<FlowStart> | null> => post("api/connections/kiro/start");
export const pollKiroLogin = (flowId: string): Promise<FlowPoll | null> =>
  poll("api/connections/kiro/poll", flowId);
export const startCursorLogin = (): Promise<Rep<FlowStart> | null> => post("api/connections/cursor/start");
export const pollCursorLogin = (flowId: string): Promise<FlowPoll | null> =>
  poll("api/connections/cursor/poll", flowId);

// --- opencode ----------------------------------------------------------------------------------
export const putOpencodeKey = (env: string, key: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/opencode", "PUT", { env, key });
export const restartOpencodeServe = (): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/opencode/serve/restart", "POST", {});
export const putOpencodeWorkspace = (id: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/opencode/workspace", "PUT", { id });
export const startOpencodeOauth = (): Promise<Rep<FlowStart> | null> => post("api/connections/opencode/oauth/start");
export const pollOpencodeOauth = (flowId: string): Promise<FlowPoll | null> =>
  poll("api/connections/opencode/oauth/poll", flowId);
export const cancelOpencodeOauth = (flowId: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/opencode/oauth/cancel", "POST", { flow_id: flowId });

// --- llama.cpp ---------------------------------------------------------------------------------
export const putLcppConnection = (url: string, apiKey: string): Promise<Rep<Ack> | null> =>
  apiJSON("api/connections/lcpp", "PUT", { url, apiKey });
export const checkLcppConnection = (): Promise<Rep<LcppCheck> | null> =>
  apiJSON("api/connections/lcpp/check", "POST", {});

// --- Unusable credentials (ADR 0138 D14a) and the live kind catalogue --------------------------
/** The answer is validated by the caller (`isUnusable`), so it is typed `unknown`. */
export const getUnusableConnections = (): Promise<unknown> => api("api/connections/unusable");
export const removeUnusableConnections = (token: string): Promise<unknown> =>
  apiJSON("api/connections/unusable/remove", "POST", { token });
/** The answer is validated by `parseMemberCatalog`, so it is typed `unknown`. */
export const getAgentKinds = (): Promise<unknown> => api("api/agents/kinds");
