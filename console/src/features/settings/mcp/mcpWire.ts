// The wire contract between the MCP servers tab (McpTab.tsx) and the agent's
// registry REST (workspace/agent/mcp_servers.go, docs/log/48 + ADR0031). Kept out of the
// component so the rules that are easy to get subtly wrong — masked-secret round-trip,
// "the transport decides which half is sent", the CLI-narrowest name charset — are
// pinned by unit tests instead of only by a running browser.

import { catalogKinds, kindFacts, useKindCatalogVersion } from "../../../agents/kindCatalog.ts";

// The agent kinds a definition may be scoped to when no kind catalog has been loaded: the
// fallback for mcpKinds(), in display order. The kind catalog (GET /api/agents/kinds) is the
// source once it arrives; kindCatalog.test.ts holds this list equal to the build-time snapshot,
// and the Go test TestMcpKnownKindsMirrorConsole reads this line as text. The non-agent session
// kinds (shell / ssm) run no MCP client, so they are not offered.
// How a kind RECEIVES its servers is not this list's business: every kind here but muse gets a
// native config file written for it, and muse takes them on the wire at session start.
export const MCP_KINDS = ["claude", "codex", "opencode", "cursor", "kiro", "copilot", "agy", "muse", "lcpp"] as const;

// The kinds a definition may be scoped to now: those the kind catalog says take MCP servers
// ("unverified" counts as no — the Agent refuses a kind it has not verified). Kinds this list
// already knows keep its order; a kind only the catalog knows follows, in the catalog's order.
export function mcpKinds(): string[] {
  const seen = new Set<string>(MCP_KINDS);
  const all = [...MCP_KINDS, ...catalogKinds().filter((k) => !seen.has(k))];
  return all.filter((k) => kindFacts(k)?.mcpClient === "yes");
}

// Same list, re-read when the catalog arrives while a form is already on screen.
export function useMcpKinds(): string[] {
  useKindCatalogVersion();
  return mcpKinds();
}

// The stand-in the agent sends for every stored secret value. Sending it back
// unchanged keeps the stored value (mcpreg.MergeSecrets), so the Console can edit a
// definition without ever handling the real credential.
export const MASKED = "***";

// What the agent / CP send for a stored Bearer credential: the scheme (any case, as
// stored) stays, the secret does not. Sending it back unchanged keeps the stored value.
// Every other stored header value — a non-Bearer Authorization included — arrives as MASKED.
export const BEARER_MASKED_RE = /^bearer\s+\*\*\*$/i;

// Name rule, identical to mcpreg.nameRe (Go), so the form refuses locally what the
// agent would refuse anyway. It is the narrowest of the target CLIs' key charsets
// (codex writes the name as a TOML bare key).
export const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$/;

export interface McpServer {
  id: string;
  name: string;
  label?: string;
  origin: "user" | "tenant" | "builtin";
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  enabled: boolean;
  targets: { assistant: boolean; session: boolean };
  kinds?: string[];
  timeoutMs?: number;
  // userSecret marks a TENANT-distributed definition that arrives with header NAMES but
  // no values: the tenant describes the endpoint, the member supplies their own
  // credential (docs/log/48 §5.2). Such a row is the one tenant row whose content a member
  // may write — through PUT /mcp-servers/{id}/secrets, not the normal edit path.
  userSecret?: boolean;
  // Derived server-side (the Console must not recompute them): may this member edit
  // the row, and does it have everything it needs to actually start.
  editable: boolean;
  ready: boolean;
}

export interface Registry {
  servers: McpServer[];
  tenantFetchedAt?: number;
  shadowed?: string[];
}

// needsMemberSecrets reports whether a row is waiting on THIS member to type a header
// value. Distinct from `!ready`: an ordinary not-ready row is someone else's problem
// (a broken tenant definition, a missing builtin connection), while this one has an
// action the member can take right here.
export const needsMemberSecrets = (s: McpServer): boolean =>
  s.origin === "tenant" && !!s.userSecret && Object.values(s.headers || {}).some((v) => v === "");

// What the connection test learned from an OAuth-protected server (agent: mcpreg.OAuthReport,
// ADR 0119). `problems[].code` is a stable wire code; the Console words each one itself.
export interface OAuthServer {
  issuer: string;
  metadataUrl?: string;
  valid: boolean;
  registration: string[]; // "cimd" | "dcr"; empty = neither
  s256: boolean;
  issParam: boolean;
  scopesSupported?: string[];
}

export interface OAuthProblem {
  code: string;
  issuer?: string;
  detail?: string;
}

export interface OAuthReport {
  required: boolean;
  resource?: string;
  metadataUrl?: string;
  challengedScope?: string;
  scopesSupported?: string[];
  servers?: OAuthServer[];
  selected?: string;
  problems?: OAuthProblem[];
}

export interface ProbeResult {
  ok: boolean;
  serverName?: string;
  serverVersion?: string;
  toolCount: number;
  tools?: string[];
  revision?: string;
  supportedVersions?: string[];
  error?: string;
  detail?: string;
  oauth?: OAuthReport;
  elapsedMs: number;
}

// isOAuthRequired: the test reached an OAuth-protected server. It is not a failure — the
// server answered, and wants a sign-in this phase cannot do yet — so the Console shows it
// apart from a red error.
export const isOAuthRequired = (p: ProbeResult): boolean => !p.ok && p.oauth?.required === true;

export interface KV {
  k: string;
  v: string;
}

// How a remote server authenticates. A fourth option (OAuth, ADR 0119) joins this union and
// AUTH_MODES; authHeaders() is the one place that turns a mode into wire headers.
export type AuthMode = "none" | "bearer" | "headers";
export const AUTH_MODES: readonly AuthMode[] = ["none", "bearer", "headers"];

export interface Form {
  id: string; // "" = a new registration
  name: string; // the ID: the key written into every CLI config (NAME_RE)
  label: string; // the display name
  nameEdited: boolean; // the user typed the ID: stop deriving it
  transport: "stdio" | "http";
  command: string;
  args: string; // one argument per line (an argument may itself contain spaces)
  env: KV[];
  url: string;
  auth: AuthMode;
  token: string; // Bearer mode: a newly typed token; "" keeps the stored one
  headers: KV[]; // Custom headers mode; in Bearer mode it holds the stored Authorization row
  assistant: boolean;
  session: boolean;
  kinds: string[]; // empty = every kind
  timeoutMs: string;
  enabled: boolean;
}

export const emptyForm = (): Form => ({
  id: "",
  name: "",
  label: "",
  nameEdited: false,
  transport: "http",
  command: "",
  args: "",
  env: [],
  url: "",
  auth: "none",
  token: "",
  headers: [],
  assistant: true,
  session: true,
  kinds: [],
  timeoutMs: "",
  enabled: true,
});

export const toKV = (m?: Record<string, string>): KV[] =>
  Object.entries(m || {}).map(([k, v]) => ({ k, v }));

// fromKV drops blank keys so a half-typed row never reaches the agent as a broken
// entry. A blank VALUE is kept: it is how the user clears one.
export const fromKV = (rows: KV[]): Record<string, string> => {
  const out: Record<string, string> = {};
  for (const { k, v } of rows) if (k.trim()) out[k.trim()] = v;
  return out;
};

// --- authentication ---------------------------------------------------------------------

export const isAuthHeader = (k: string): boolean => k.trim().toLowerCase() === "authorization";

// stripBearer: a pasted `Bearer <token>` is a token with its scheme attached.
export const stripBearer = (t: string): string => t.trim().replace(/^bearer\s+/i, "").trim();

// authOf picks the editor a stored header set opens in. Bearer only when the headers are
// exactly one Authorization row whose value the server masked as `Bearer ***` (or, for a
// user_secret tenant row, whose value is not stored at all); anything else — a `Token abc`
// Authorization, extra headers — is Custom headers, so no header is ever lost by opening it.
export const authOf = (rows: KV[], userSecret = false): AuthMode => {
  const named = rows.filter((r) => r.k.trim() !== "");
  if (named.length === 0) return "none";
  if (named.length === 1 && isAuthHeader(named[0].k)) {
    const v = named[0].v;
    if (BEARER_MASKED_RE.test(v) || (userSecret && v === "")) return "bearer";
  }
  return "headers";
};

// storedBearer is the Authorization row that still carries a stored Bearer credential.
const storedBearer = (rows: KV[]): KV | undefined =>
  rows.find((r) => isAuthHeader(r.k) && BEARER_MASKED_RE.test(r.v));
export const hasStoredBearer = (rows: KV[]): boolean => !!storedBearer(rows);

// bearerReady: Bearer mode needs a token, typed now or already stored.
export const bearerReady = (auth: AuthMode, token: string, rows: KV[]): boolean =>
  auth !== "bearer" || stripBearer(token) !== "" || !!storedBearer(rows);

// authHeaders is the wire header map for a mode. namesOnly (a tenant user_secret row)
// keeps the names and blanks every value.
export const authHeaders = (
  auth: AuthMode,
  token: string,
  rows: KV[],
  namesOnly = false,
): Record<string, string> => {
  switch (auth) {
    case "none":
      return {};
    case "headers": {
      const h = fromKV(rows);
      return namesOnly ? Object.fromEntries(Object.keys(h).map((k) => [k, ""])) : h;
    }
    case "bearer": {
      // Reuse the stored row's key as written: the stored map is keyed by it, so a renamed
      // `authorization` would no longer match and `Bearer ***` would be dropped.
      const row = rows.find((r) => isAuthHeader(r.k));
      const key = row ? row.k.trim() : "Authorization";
      if (namesOnly) return { [key]: "" };
      const typed = stripBearer(token);
      if (typed) return { [key]: "Bearer " + typed };
      const kept = storedBearer(rows);
      return kept ? { [key]: kept.v } : {};
    }
  }
};

// A member's value for a distributed header is a bare token when the header is a Bearer
// Authorization: either the stored value is `Bearer ***`, or nothing is stored yet and the
// header is Authorization (the tenant carries header names only, so there is no scheme marker
// to read before the first entry).
export const memberTokenOnly = (k: string, stored: string): boolean =>
  BEARER_MASKED_RE.test(stored) || (stored === "" && isAuthHeader(k));

// What goes on the wire for a token-only row. Blank: untouched, so a stored value is kept
// (as sent back unchanged) and an unset one stays unset. Typed: the token with `Bearer `
// in front, unless the member typed their own scheme (`Token abc`), which is kept as is.
export const memberTokenValue = (typed: string, stored: string): string => {
  const t = typed.trim();
  if (t === "") return stored;
  if (/^bearer\s/i.test(t) || !/\s/.test(t)) return "Bearer " + stripBearer(t);
  return t;
};

// --- ID derivation ----------------------------------------------------------------------

// slugId turns free text into a NAME_RE-valid ID, or "" when nothing usable is left
// (a Japanese-only label has no [a-z0-9] at all).
export const slugId = (text: string): string =>
  text
    .normalize("NFKD")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48)
    .replace(/-+$/, "");

export const hostOf = (url: string): string => {
  try {
    return new URL(url.trim()).hostname;
  } catch {
    return "";
  }
};

// deriveId: from the display name; when that yields nothing, from the URL host.
export const deriveId = (label: string, url: string): string => slugId(label) || slugId(hostOf(url));

// withDerivedId re-derives a NEW registration's ID until the user edits it. An existing
// server's ID is never regenerated: it is the key in the stored config and in every CLI's.
export const withDerivedId = <T extends { id: string; name: string; label: string; url: string; nameEdited: boolean }>(
  f: T,
): T => (f.id !== "" || f.nameEdited ? f : { ...f, name: deriveId(f.label, f.url) });

// isInsecureRemote: plain http:// to a host that is not this machine. Warned about, not refused.
export const isInsecureRemote = (url: string): boolean => {
  const u = url.trim();
  if (!/^http:\/\//i.test(u)) return false;
  const h = hostOf(u).replace(/^\[|\]$/g, "");
  return !(h === "localhost" || h.endsWith(".localhost") || h === "::1" || /^127(\.\d{1,3}){3}$/.test(h));
};

// --- advanced section ---------------------------------------------------------------------

export interface AdvancedValues {
  assistant: boolean;
  session: boolean;
  kinds: string[];
  timeoutMs: string;
  enabled: boolean;
}

// advancedIsDefault: every advanced value is what a fresh form starts with. withTimeout is
// false for the tenant form, which has no timeout field.
export const advancedIsDefault = (v: AdvancedValues, withTimeout = true): boolean =>
  v.assistant && v.session && v.kinds.length === 0 && v.enabled && (!withTimeout || v.timeoutMs.trim() === "");

export const formOf = (s: McpServer): Form => ({
  id: s.id,
  name: s.name,
  label: s.label || "",
  nameEdited: true,
  transport: s.transport,
  command: s.command || "",
  args: (s.args || []).join("\n"),
  env: toKV(s.env),
  url: s.url || "",
  auth: authOf(toKV(s.headers)),
  token: "",
  headers: toKV(s.headers),
  assistant: !!s.targets?.assistant,
  session: !!s.targets?.session,
  kinds: s.kinds || [],
  timeoutMs: s.timeoutMs ? String(s.timeoutMs) : "",
  enabled: s.enabled,
});

// bodyOf builds the wire definition. The transport decides which half is sent: the
// agent refuses a definition carrying both (a stdio server with a URL is a mistake,
// not a merge), so the unused half is omitted rather than sent empty.
export const bodyOf = (f: Form): Record<string, unknown> => {
  const base: Record<string, unknown> = {
    id: f.id || undefined,
    name: f.name.trim(),
    label: f.label.trim(),
    transport: f.transport,
    enabled: f.enabled,
    targets: { assistant: f.assistant, session: f.session },
    kinds: f.kinds,
    timeoutMs: Number(f.timeoutMs) || 0,
  };
  if (f.transport === "stdio") {
    base.command = f.command.trim();
    base.args = f.args.split("\n").map((a) => a.trim()).filter(Boolean);
    base.env = fromKV(f.env);
  } else {
    base.url = f.url.trim();
    base.headers = authHeaders(f.auth, f.token, f.headers);
  }
  return base;
};

export const formValid = (f: Form): boolean =>
  NAME_RE.test(f.name.trim()) &&
  (f.transport === "stdio"
    ? f.command.trim() !== ""
    : /^https?:\/\/.+/.test(f.url.trim()) && bearerReady(f.auth, f.token, f.headers));

// --- tenant distribution (docs/log/48 P4, AdminTab) -----------------------------------
//
// A tenant-distributed definition is a STRICT SUBSET of the member's own: remote only,
// with no command / args / env. That is ADR0031 decision 2 — a distributed stdio server is an
// admin running an arbitrary command in every member's container — and it is enforced by
// the CP table having no such columns, not just by this form omitting them. So the admin
// form is deliberately a separate shape rather than the member Form with fields hidden:
// there is no state of it that could produce a stdio definition.

// TenantServer is one row of GET /api/admin/mcp-servers. Header values arrive masked
// ("***"), or empty when user_secret means no value is stored at all.
export interface TenantServer {
  id: string;
  name: string;
  label?: string;
  transport: "http";
  url: string;
  headers?: Record<string, string>;
  targets: { assistant: boolean; session: boolean };
  kinds?: string[];
  timeoutMs?: number;
  enabled: boolean;
  user_secret?: boolean;
  created_by?: string;
  updated_at?: string;
}

export interface TenantForm {
  id: string; // "" = a new distribution
  name: string; // the ID (see Form)
  label: string; // the display name
  nameEdited: boolean;
  url: string;
  auth: AuthMode;
  token: string;
  headers: KV[];
  assistant: boolean;
  session: boolean;
  kinds: string[];
  timeoutMs: string;
  enabled: boolean;
  userSecret: boolean;
}

export const emptyTenantForm = (): TenantForm => ({
  id: "",
  name: "",
  label: "",
  nameEdited: false,
  url: "",
  auth: "none",
  token: "",
  headers: [],
  assistant: true,
  session: true,
  kinds: [],
  timeoutMs: "",
  enabled: true,
  userSecret: false,
});

export const tenantFormOf = (s: TenantServer): TenantForm => ({
  id: s.id,
  name: s.name,
  label: s.label || "",
  nameEdited: true,
  url: s.url || "",
  auth: authOf(toKV(s.headers), !!s.user_secret),
  token: "",
  headers: toKV(s.headers),
  assistant: !!s.targets?.assistant,
  session: !!s.targets?.session,
  kinds: s.kinds || [],
  timeoutMs: s.timeoutMs ? String(s.timeoutMs) : "",
  enabled: s.enabled,
  userSecret: !!s.user_secret,
});

// bodyOfTenant builds the admin wire body. transport is pinned to "http" rather than
// taken from the form — see the note above. With userSecret on, only the header NAMES
// carry meaning, so values are sent blank: the CP drops stored values when the flag is
// set, and sending a real one would only put a credential on the wire for nothing.
export const bodyOfTenant = (f: TenantForm, tenantSlug: string): Record<string, unknown> => ({
  id: f.id || undefined,
  tenant_slug: tenantSlug,
  name: f.name.trim(),
  label: f.label.trim(),
  transport: "http",
  url: f.url.trim(),
  headers: authHeaders(f.auth, f.token, f.headers, f.userSecret),
  targets: { assistant: f.assistant, session: f.session },
  kinds: f.kinds,
  timeoutMs: Number(f.timeoutMs) || 0,
  enabled: f.enabled,
  user_secret: f.userSecret,
});

export const tenantFormValid = (f: TenantForm): boolean =>
  NAME_RE.test(f.name.trim()) &&
  /^https?:\/\/.+/.test(f.url.trim()) &&
  (f.userSecret || bearerReady(f.auth, f.token, f.headers));

// NOTE: this file used to export a codexUnsupported() helper, on the premise that
// codex could carry nothing but a bearer token on a remote MCP server. That premise
// was measured false on codex-cli 0.145.0 (docs/log/48 §7): `http_headers` and
// `env_http_headers` both round-trip through the config AND arrive on the wire. The
// helper and its "not supported on codex" warning are gone rather than left showing a
// warning that is not true.
