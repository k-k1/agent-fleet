// The wire contract between the MCP servers tab (McpTab.tsx) and the agent's
// registry REST (workspace/agent/mcp_servers.go, docs/48 + ADR0031). Kept out of the
// component so the rules that are easy to get subtly wrong — masked-secret round-trip,
// "the transport decides which half is sent", the CLI-narrowest name charset — are
// pinned by unit tests instead of only by a running browser.

// The agent kinds a definition may be scoped to. Mirrors mcpreg.knownKinds (Go); the
// non-agent session kinds (shell / ssm) run no MCP client, so they are not offered.
export const MCP_KINDS = ["claude", "codex", "opencode", "cursor", "kiro", "copilot", "agy"] as const;

// The stand-in the agent sends for every stored secret value. Sending it back
// unchanged keeps the stored value (mcpreg.MergeSecrets), so the Console can edit a
// definition without ever handling the real credential.
export const MASKED = "***";

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
  elapsedMs: number;
}

export interface KV {
  k: string;
  v: string;
}

export interface Form {
  id: string; // "" = a new registration
  name: string;
  label: string;
  transport: "stdio" | "http";
  command: string;
  args: string; // one argument per line (an argument may itself contain spaces)
  env: KV[];
  url: string;
  headers: KV[];
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
  transport: "stdio",
  command: "",
  args: "",
  env: [],
  url: "",
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

export const formOf = (s: McpServer): Form => ({
  id: s.id,
  name: s.name,
  label: s.label || "",
  transport: s.transport,
  command: s.command || "",
  args: (s.args || []).join("\n"),
  env: toKV(s.env),
  url: s.url || "",
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
    base.headers = fromKV(f.headers);
  }
  return base;
};

export const formValid = (f: Form): boolean =>
  NAME_RE.test(f.name.trim()) &&
  (f.transport === "stdio" ? f.command.trim() !== "" : /^https?:\/\/.+/.test(f.url.trim()));

// codexUnsupported — codex can only carry a bearer token on a remote MCP server
// (`bearer_token_env_var`), not arbitrary headers (docs/48 §7). A definition with any
// other header simply cannot be expressed there, so say so at registration time
// instead of letting codex silently come up without the server.
export const codexUnsupported = (transport: string, headers: KV[]): boolean =>
  transport === "http" &&
  headers.some((h) => h.k.trim() && h.k.trim().toLowerCase() !== "authorization");
