// The wire contract between the MCP servers tab (McpTab.tsx) and the agent's
// registry REST (workspace/agent/mcp_servers.go, docs/log/48 + ADR0031). Kept out of the
// component so the rules that are easy to get subtly wrong — masked-secret round-trip,
// "the transport decides which half is sent", the CLI-narrowest name charset — are
// pinned by unit tests instead of only by a running browser.

import { tMaybe } from "../../../lib/i18n/index.ts";
import { FALLBACK_KIND_FACTS, catalogKinds, kindFacts, useKindCatalogVersion } from "../../../agents/kindCatalog.ts";
import { SESSION_KINDS, type SessionKind } from "../../../types/session.ts";

// The agent kinds a definition may be scoped to when no kind catalog has been loaded: the
// fallback facts' mcpClient "yes" kinds (kindCatalog.test.ts holds those equal to the build-time
// snapshot, which the CP's mcpKnownKinds reads), in SESSION_KINDS order. The non-agent session
// kinds (shell / ssm) run no MCP client, so they are not offered.
// How a kind RECEIVES its servers is not this list's business: every kind here but muse gets a
// native config file written for it, and muse takes them on the wire at session start.
export const MCP_KINDS: readonly SessionKind[] = SESSION_KINDS.filter((k) => FALLBACK_KIND_FACTS[k].mcpClient === "yes");

// The kinds a definition may be scoped to now: those the kind catalog says take MCP servers
// ("unverified" counts as no — the Agent refuses a kind it has not verified), in SESSION_KINDS
// order; a kind only the catalog knows follows, in the catalog's order.
export function mcpKinds(): string[] {
  const known = new Set<string>(SESSION_KINDS);
  const all = [...SESSION_KINDS, ...catalogKinds().filter((k) => !known.has(k))];
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
  // "oauth": the member signs in from the Console and the Agent holds the grant (ADR 0119).
  // Absent = static headers (or none).
  auth?: "oauth";
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
  // code + params are the language-free form of `error` (mcp.probe_err.<code> in the catalogues);
  // `error` stays the Agent's English fallback for a code this Console does not know.
  code?: string;
  params?: Record<string, string>;
  detail?: string;
  oauth?: OAuthReport;
  elapsedMs: number;
}

// probeErrText is the text of a failed test: the catalogue's wording for the Agent's code, or
// the Agent's own English `error` when the code is absent, newer than this Console, or arrives
// without a parameter its template needs (the placeholders are read from the template, not the
// result, because a server message may itself contain braces).
export const probeErrText = (p: ProbeResult): string | undefined => {
  const key = "mcp.probe_err." + p.code;
  const template = p.code ? tMaybe(key) : undefined;
  if (template === undefined) return p.error;
  const need = [...template.matchAll(/\{(\w+)\}/g)].map((m) => m[1]);
  if (need.some((k) => typeof p.params?.[k] !== "string")) return p.error;
  return tMaybe(key, p.params);
};

// isOAuthRequired: the test reached an OAuth-protected server. It is not a failure — the
// server answered and wants a sign-in — so the Console shows it apart from a red error.
export const isOAuthRequired = (p: ProbeResult): boolean => !p.ok && p.oauth?.required === true;

// probeOfAnswer turns the answer of POST api/mcp-servers/test into a ProbeResult. A request the
// CP/Agent refused is an error ENVELOPE (`error` is an object with a code); a probe's own
// failure also has a string `error`, and for an OAuth-protected server it rides together with
// the `oauth` report, which must not be thrown away as if the request had failed.
export const probeOfAnswer = (res: any, errMsg: (e: unknown) => string): ProbeResult =>
  res && res.error && typeof res.error === "object"
    ? { ok: false, toolCount: 0, elapsedMs: 0, error: errMsg(res.error) }
    : (res as ProbeResult);

export interface KV {
  k: string;
  v: string;
}

// How a remote server authenticates. authHeaders() is the one place that turns a mode into wire
// headers; OAuth (ADR 0119) sends none, and bodyOf() marks the definition `auth: "oauth"`.
export type AuthMode = "none" | "bearer" | "headers" | "oauth";
export const AUTH_MODES: readonly AuthMode[] = ["none", "bearer", "headers", "oauth"];

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
    case "oauth":
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

// switchAuth changes the mode and carries the credential already entered across the
// Bearer <-> Custom headers boundary, so a typed token is never silently dropped by the
// switch. Leaving Bearer (for Custom headers or None) writes the effective Authorization into
// the rows; entering Bearer (from Custom headers or None) reads it back out of them. None
// therefore keeps the same contract: authHeaders("none") keeps the rows off the wire, and
// they come back intact on the way out of it.
export const switchAuth = <T extends { auth: AuthMode; token: string; headers: KV[] }>(
  f: T,
  next: AuthMode,
  userSecret = false,
): T => {
  const from = f.auth;
  if (from === next) return f;
  if (from === "bearer") {
    const eff = authHeaders("bearer", f.token, f.headers, userSecret);
    const entries = Object.entries(eff);
    if (entries.length === 0) return { ...f, auth: next };
    const [k, v] = entries[0];
    const i = f.headers.findIndex((r) => isAuthHeader(r.k));
    const headers =
      i >= 0 ? f.headers.map((r, j) => (j === i ? { k, v } : r)) : [{ k, v }, ...f.headers];
    return { ...f, auth: next, token: "", headers };
  }
  if (next === "bearer") {
    // A real (unmasked) `Bearer <token>` typed in the editor becomes the token; a mask or a
    // non-Bearer value stays where it is.
    const row = f.headers.find((r) => isAuthHeader(r.k));
    const m = row && !BEARER_MASKED_RE.test(row.v) ? /^bearer\s+(\S.*)$/i.exec(row.v.trim()) : null;
    return { ...f, auth: next, token: m ? m[1].trim() : f.token };
  }
  return { ...f, auth: next };
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
  auth: s.auth === "oauth" ? "oauth" : authOf(toKV(s.headers)),
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
    // Omitted for every other mode: an update replaces the definition, so leaving OAuth is
    // just not sending it.
    if (f.auth === "oauth") base.auth = "oauth";
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
  // "oauth": each member signs in for themselves (ADR 0119 decision 9); the row has no headers.
  auth?: "oauth";
  // The client the tenant registered at the server's authorization server, if any. A stored
  // secret comes back as MASKED; none at all means a public client.
  oauth_client?: { issuer: string; client_id: string; client_secret?: string };
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
  // OAuth only: the tenant's pre-registered client. Empty client id = none (members' sign-ins
  // register as usual). oauthSecret is a newly typed secret; "" keeps the stored one unless
  // oauthSecretDrop is set.
  oauthIssuer: string;
  oauthClientId: string;
  oauthSecret: string;
  oauthSecretStored: boolean;
  oauthSecretDrop: boolean;
  // The issuer and client id the stored secret belongs to, as loaded. The control plane keeps a
  // stored secret only for the same pair, so changing either needs the secret typed again.
  oauthOrigIssuer: string;
  oauthOrigClientId: string;
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
  oauthIssuer: "",
  oauthClientId: "",
  oauthSecret: "",
  oauthSecretStored: false,
  oauthSecretDrop: false,
  oauthOrigIssuer: "",
  oauthOrigClientId: "",
});

export const tenantFormOf = (s: TenantServer): TenantForm => ({
  id: s.id,
  name: s.name,
  label: s.label || "",
  nameEdited: true,
  url: s.url || "",
  auth: s.auth === "oauth" ? "oauth" : authOf(toKV(s.headers), !!s.user_secret),
  token: "",
  headers: toKV(s.headers),
  assistant: !!s.targets?.assistant,
  session: !!s.targets?.session,
  kinds: s.kinds || [],
  timeoutMs: s.timeoutMs ? String(s.timeoutMs) : "",
  enabled: s.enabled,
  userSecret: !!s.user_secret,
  oauthIssuer: s.oauth_client?.issuer || "",
  oauthClientId: s.oauth_client?.client_id || "",
  oauthSecret: "",
  oauthSecretStored: !!s.oauth_client?.client_secret,
  oauthSecretDrop: false,
  oauthOrigIssuer: s.oauth_client?.issuer || "",
  oauthOrigClientId: s.oauth_client?.client_id || "",
});

// tenantOAuthSecretReentry: the stored secret no longer applies (the issuer or client id changed)
// and no new one is typed. Saving would be refused, so the form asks for the secret instead.
export const tenantOAuthSecretReentry = (f: TenantForm): boolean =>
  f.auth === "oauth" &&
  f.oauthClientId.trim() !== "" &&
  f.oauthSecretStored &&
  !f.oauthSecretDrop &&
  f.oauthSecret.trim() === "" &&
  (f.oauthIssuer.trim() !== f.oauthOrigIssuer || f.oauthClientId.trim() !== f.oauthOrigClientId);

// An issuer the control plane accepts: https, a host, no credentials, query or fragment.
const issuerUsable = (raw: string): boolean => {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" && u.hostname !== "" && !u.username && !u.password && !u.search && !u.hash;
  } catch {
    return false;
  }
};

// tenantOAuthClient is the oauth_client of a tenant body: present only for OAuth with a client
// id. The secret is the typed one, else MASKED to keep the stored one, else "" (a public client,
// or a stored secret the admin removed).
const tenantOAuthClient = (f: TenantForm) => {
  if (f.auth !== "oauth" || f.oauthClientId.trim() === "") return undefined;
  const typed = f.oauthSecret.trim();
  const secret = typed !== "" ? typed : f.oauthSecretStored && !f.oauthSecretDrop ? MASKED : "";
  return { issuer: f.oauthIssuer.trim(), client_id: f.oauthClientId.trim(), client_secret: secret };
};

// OAuth distributes no credential, so "each member supplies the value" has nothing to apply to:
// the form hides the choice and the body never sends it with OAuth (the CP refuses the pair).
export const tenantUserSecret = (f: TenantForm): boolean => f.userSecret && f.auth !== "oauth";

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
  headers: authHeaders(f.auth, f.token, f.headers, tenantUserSecret(f)),
  targets: { assistant: f.assistant, session: f.session },
  kinds: f.kinds,
  timeoutMs: Number(f.timeoutMs) || 0,
  enabled: f.enabled,
  user_secret: tenantUserSecret(f),
  ...(f.auth === "oauth" ? { auth: "oauth" } : {}),
  ...(tenantOAuthClient(f) ? { oauth_client: tenantOAuthClient(f) } : {}),
});

// A tenant client names the authorization server it was registered at: an https issuer.
export const tenantOAuthClientValid = (f: TenantForm): boolean =>
  f.auth !== "oauth" ||
  f.oauthClientId.trim() === "" ||
  (issuerUsable(f.oauthIssuer.trim()) && !/[\s#?]/.test(f.oauthIssuer.trim()) && !tenantOAuthSecretReentry(f));

export const tenantFormValid = (f: TenantForm): boolean =>
  NAME_RE.test(f.name.trim()) &&
  /^https?:\/\/.+/.test(f.url.trim()) &&
  (tenantUserSecret(f) || bearerReady(f.auth, f.token, f.headers)) &&
  tenantOAuthClientValid(f);

// NOTE: this file used to export a codexUnsupported() helper, on the premise that
// codex could carry nothing but a bearer token on a remote MCP server. That premise
// was measured false on codex-cli 0.145.0 (docs/log/48 §7): `http_headers` and
// `env_http_headers` both round-trip through the config AND arrive on the wire. The
// helper and its "not supported on codex" warning are gone rather than left showing a
// warning that is not true.
