// Sign-in for registered MCP servers that require OAuth (ADR 0119 P2): the pure parts. The wire is the
// Control Plane's (control-plane/oauth_mcp_client.go): start / disconnect / client id go through
// POST|DELETE|PUT api/mcp-servers/{id}/oauth…, the status is the Agent's (GET …/oauth), and the
// browser comes back to `/?mcp_oauth=ok|failed&server=&reason=&registration=`. Everything in
// that query is untrusted text: it is read against closed sets and shown only as our own words.

export type OAuthState = "not_signed_in" | "connected" | "sign_in_again" | "needs_more_permission";
export const OAUTH_STATES: readonly OAuthState[] = ["not_signed_in", "connected", "sign_in_again", "needs_more_permission"];

// How the client id was obtained.
export type OAuthRegistration = "preregistered" | "cimd" | "dcr";
export const OAUTH_REGISTRATIONS: readonly OAuthRegistration[] = ["preregistered", "cimd", "dcr"];

// GET api/mcp-servers/{id}/oauth (agent: mcpreg.OAuthStatus). No token appears in it.
export interface OAuthStatus {
  serverId: string;
  state: OAuthState;
  issuer?: string;
  registration?: OAuthRegistration;
  scopes?: string[];
  expiresAt?: number; // unix seconds; 0/absent = the server did not say
  connectedAt?: number;
  stepUpScope?: string;
  // A sign-in begun and not finished (its 10 minutes): the DCR retry is offered when it is "cimd".
  pendingRegistration?: OAuthRegistration;
}

export const asRegistration = (v: unknown): OAuthRegistration | undefined =>
  OAUTH_REGISTRATIONS.find((r) => r === v);

// normalizeStatus reads an Agent answer defensively: an unknown state reads as "not signed in".
export function normalizeStatus(raw: any, serverId: string): OAuthStatus {
  const state = OAUTH_STATES.find((s) => s === raw?.state) ?? "not_signed_in";
  return {
    serverId,
    state,
    issuer: typeof raw?.issuer === "string" ? raw.issuer : undefined,
    registration: asRegistration(raw?.registration),
    scopes: Array.isArray(raw?.scopes) ? raw.scopes.filter((x: unknown) => typeof x === "string") : undefined,
    expiresAt: typeof raw?.expiresAt === "number" ? raw.expiresAt : undefined,
    connectedAt: typeof raw?.connectedAt === "number" ? raw.connectedAt : undefined,
    stepUpScope: typeof raw?.stepUpScope === "string" ? raw.stepUpScope : undefined,
    pendingRegistration: asRegistration(raw?.pendingRegistration),
  };
}

// safeAuthorizeUrl: the browser is sent only to an http(s) URL. The value comes from the
// Agent's discovery of a server the member registered; a javascript: or data: URL must never
// be navigated to whatever produced it.
// stepUpRequest is the scope a step-up sign-in asks for: the one the server challenged for (the
// relay recorded it) together with what is already granted. The new grant replaces the old one,
// so asking for the challenged scope alone would drop the permissions the server already has.
export function stepUpRequest(st: Pick<OAuthStatus, "scopes" | "stepUpScope"> | null | undefined): string {
  const out: string[] = [];
  for (const sc of [...(st?.scopes ?? []), ...(st?.stepUpScope ?? "").split(/\s+/)]) {
    if (sc && !out.includes(sc)) out.push(sc);
  }
  return out.join(" ");
}

export function safeAuthorizeUrl(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.protocol === "http:" ? u.toString() : null;
  } catch {
    return null;
  }
}

// --- the return from the authorization server ---------------------------------------------

// The reasons the callback's redirect may carry (control-plane/oauth_mcp_client.go
// mcpClientOAuthReasons). Each has a text under mcp.oauth_return_<reason>; anything else reads as
// "failed".
export const RETURN_REASONS = [
  "state_unknown",
  "state_expired",
  "state_mismatch",
  "session",
  "authorization_denied",
  "iss_mismatch",
  "iss_missing",
  "no_code",
  "token_exchange_failed",
  "workspace_unreachable",
  "internal",
  "failed",
] as const;
export type ReturnReason = (typeof RETURN_REASONS)[number];

export interface OAuthReturn {
  ok: boolean;
  server: string; // an id as the Agent mints them; "" when absent or not id-shaped
  reason?: ReturnReason;
  registration?: OAuthRegistration;
}

const SERVER_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;

// parseOAuthReturn reads the callback's redirect query, or null when the URL is not one.
export function parseOAuthReturn(search: string): OAuthReturn | null {
  const q = new URLSearchParams(search);
  const outcome = q.get("mcp_oauth");
  if (outcome !== "ok" && outcome !== "failed") return null;
  const server = q.get("server") ?? "";
  const reason = RETURN_REASONS.find((r) => r === q.get("reason")) ?? "failed";
  return {
    ok: outcome === "ok",
    server: SERVER_ID_RE.test(server) ? server : "",
    reason: outcome === "ok" ? undefined : reason,
    registration: asRegistration(q.get("registration")),
  };
}

// stripOAuthReturn removes the four parameters (and only those) so a reload does not replay the
// notice.
export function stripOAuthReturn(search: string): string {
  const q = new URLSearchParams(search);
  for (const k of ["mcp_oauth", "server", "reason", "registration"]) q.delete(k);
  const s = q.toString();
  return s ? "?" + s : "";
}

// The server a failed sign-in concerned, for the McpTab to offer the DCR retry on: kept for the
// session only.
let lastReturn: OAuthReturn | null = null;
export const rememberReturn = (r: OAuthReturn | null) => {
  lastReturn = r;
};
export const takeReturn = (): OAuthReturn | null => {
  const r = lastReturn;
  lastReturn = null;
  return r;
};
