// The credential policy's wire shapes and vocabulary as the Model access pages read them
// (ADR 0138 D1, D7, D9c, D16), and the one rule that keeps a write from widening access by
// accident: a save sends the rule as loaded with only the edited fields replaced.
//
// The shapes mirror control-plane/internal/routepolicysrv (policyLayerWire, ceilingWire,
// tenantPolicyWire) and routepolicy.Rule. In a rule, an absent `classes` / `destinations` /
// `endpoints` means "this layer does not narrow" and an empty list means "none": the two must
// never be folded into each other, so the draft keeps that choice as its own flag.
//
// Pure data and functions only: no store, no component. Both the tenant page and the admin page
// import it.

/** routepolicy.Rule. Unknown keys a newer CP sends are carried through untouched. */
export interface RouteRule {
  classes?: string[];
  destinations?: string[];
  except?: string[];
  deny?: string[];
  endpoints?: string[];
  [key: string]: unknown;
}

/** policyLayerWire: one stored layer. */
export interface PolicyLayer {
  rule: RouteRule;
  revision: number;
  updatedAt?: string;
}

/** ceilingWire: the operator ceiling, read-only, with its source. */
export interface PolicyCeiling {
  raw: string;
  rule: RouteRule;
  source: string;
}

export interface RouteDecision {
  id: string;
  class: string;
  destination: string;
  allowed: boolean;
  forbiddenBy?: string[];
}

export interface EndpointDecision {
  host: string;
  approved: boolean;
  excludedBy?: string[];
}

/** tenantPolicyWire, as GET /api/admin/tenants/{slug}/route-policy answers it. */
export interface TenantPolicyView {
  ceiling: PolicyCeiling;
  deployment: PolicyLayer;
  cap: PolicyLayer;
  tenant: PolicyLayer;
  restricted: boolean;
  revision: number;
  routes: RouteDecision[];
  endpoints: EndpointDecision[];
  endpointRoutes: string[];
  /** What the layers hide from the tenant's members; read through parseHidden. */
  hidden?: unknown;
}

/** hiddenItemWire: a harness, chat backend or image provider none of whose routes the layers
 *  allow, its routes, and every layer that forbids one of them. */
export interface HiddenItem {
  id: string;
  routes: string[];
  forbiddenBy: string[];
}

/** hiddenWire. */
export interface HiddenView {
  kinds: HiddenItem[];
  chat: HiddenItem[];
  imagegen: HiddenItem[];
}

/** GET /api/admin/route-policy. */
export interface DeploymentPolicyView {
  ceiling: PolicyCeiling;
  deployment: PolicyLayer;
  revision: number;
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const isStrList = (v: unknown) => v === undefined || (Array.isArray(v) && v.every((s) => typeof s === "string"));
const isRule = (v: unknown): v is RouteRule =>
  isObj(v) && ["classes", "destinations", "except", "deny", "endpoints"].every((k) => isStrList(v[k]));
const isLayer = (v: unknown): v is PolicyLayer => isObj(v) && isRule(v.rule) && typeof v.revision === "number";
const isCeiling = (v: unknown): v is PolicyCeiling =>
  isObj(v) && typeof v.raw === "string" && isRule(v.rule) && typeof v.source === "string";

// Shape checks rather than trust: a page that cannot read the policy must not draw an editor
// whose save would write a rule built from a partial answer (fail closed).
export function parseTenantPolicy(d: unknown): TenantPolicyView | null {
  if (!isObj(d) || !isCeiling(d.ceiling) || !isLayer(d.deployment) || !isLayer(d.cap) || !isLayer(d.tenant)) return null;
  if (!Array.isArray(d.routes) || !Array.isArray(d.endpoints)) return null;
  return d as unknown as TenantPolicyView;
}

const isHiddenItem = (v: unknown): v is HiddenItem =>
  isObj(v) &&
  typeof v.id === "string" &&
  Array.isArray(v.routes) &&
  isStrList(v.routes) &&
  Array.isArray(v.forbiddenBy) &&
  isStrList(v.forbiddenBy) &&
  // hiddenOf never lists an item with no route or no forbidding layer; one that arrives would
  // draw "Hidden by:" with nothing after it, so it fails the whole list closed instead.
  v.routes.length > 0 &&
  v.forbiddenBy.length > 0;

// parseHidden is null unless all three lists are present and well formed: a missing or partial
// answer must never read as "nothing is hidden".
export function parseHidden(d: unknown): HiddenView | null {
  if (!isObj(d)) return null;
  for (const k of ["kinds", "chat", "imagegen"]) {
    const l = d[k];
    if (!Array.isArray(l) || !l.every(isHiddenItem)) return null;
  }
  return d as unknown as HiddenView;
}

export function parseDeploymentPolicy(d: unknown): DeploymentPolicyView | null {
  if (!isObj(d) || !isCeiling(d.ceiling) || !isLayer(d.deployment)) return null;
  return d as unknown as DeploymentPolicyView;
}

// The vocabulary, held equal to control-plane/internal/agentroutes/catalog.json by
// routePolicyRule.test.ts. The deployment rule's GET carries no catalogue, so the editors need
// their own list; values a server sends that are not here are still shown and kept.
export const ROUTE_CLASSES = ["subscription", "member-key", "member-cloud", "provided", "self-hosted"] as const;

// "unclassified" is left out on purpose: the CP refuses it in an allow list.
export const ROUTE_DESTINATIONS = [
  "anthropic",
  "openai",
  "google",
  "meta",
  "github",
  "cursor",
  "kiro",
  "opencode.ai",
  "openrouter",
  "sakana",
  "aws-bedrock",
  "af-engine",
  "self-hosted",
] as const;

export interface CatalogRoute {
  id: string;
  class: string;
  destination: string;
  /** Its class and destination come from the endpoint it is bound to; it cannot be excepted. */
  perConnection?: boolean;
}

export const ROUTE_CATALOG: CatalogRoute[] = [
  { id: "af-engine-image", class: "self-hosted", destination: "af-engine" },
  { id: "af-engine-llm", class: "self-hosted", destination: "af-engine" },
  { id: "agy-login", class: "subscription", destination: "google" },
  { id: "anthropic-api", class: "member-key", destination: "anthropic" },
  { id: "anthropic-subscription", class: "subscription", destination: "anthropic" },
  { id: "bedrock", class: "member-cloud", destination: "aws-bedrock" },
  { id: "bedrock-deployment", class: "provided", destination: "aws-bedrock" },
  { id: "bedrock-tenant", class: "provided", destination: "aws-bedrock" },
  { id: "chatgpt-subscription", class: "subscription", destination: "openai" },
  { id: "copilot-login", class: "subscription", destination: "github" },
  { id: "cursor-login", class: "subscription", destination: "cursor" },
  { id: "custom-api", class: "member-key", destination: "unclassified", perConnection: true },
  { id: "gemini-api", class: "member-key", destination: "google" },
  { id: "kiro-login", class: "subscription", destination: "kiro" },
  { id: "member-llamacpp", class: "member-key", destination: "unclassified", perConnection: true },
  { id: "meta-api", class: "member-key", destination: "meta" },
  { id: "muse-login", class: "subscription", destination: "meta" },
  { id: "openai-api", class: "member-key", destination: "openai" },
  { id: "opencode-account", class: "subscription", destination: "opencode.ai" },
  { id: "opencode-go", class: "subscription", destination: "opencode.ai" },
  { id: "opencode-zen", class: "member-key", destination: "opencode.ai" },
  { id: "openrouter-api", class: "member-key", destination: "openrouter" },
  { id: "sakana-api", class: "member-key", destination: "sakana" },
  { id: "vertex", class: "provided", destination: "google" },
];

/** The catalogue's route ids plus any the server named that it does not list, sorted. */
export function routeIds(...extra: (string[] | undefined)[]): string[] {
  const out = new Set(ROUTE_CATALOG.map((r) => r.id));
  for (const l of extra) for (const id of l ?? []) out.add(id);
  return [...out].sort();
}

export const isPerConnection = (id: string) => ROUTE_CATALOG.some((r) => r.id === id && r.perConnection);

/** withExtra lists the known values first and then any value a rule holds that is not known. */
export function withExtra(known: readonly string[], ...held: (string[] | undefined)[]): string[] {
  const out = [...known];
  for (const l of held) for (const v of l ?? []) if (!out.includes(v)) out.push(v);
  return out;
}

/** RuleDraft is the editor's state. `*All` true means the field is absent: this layer does not
 *  narrow it. */
export interface RuleDraft {
  classesAll: boolean;
  classes: string[];
  destinationsAll: boolean;
  destinations: string[];
  except: string[];
  deny: string[];
  endpointsAll: boolean;
  endpoints: string;
}

export function draftOf(rule: RouteRule): RuleDraft {
  return {
    classesAll: rule.classes === undefined,
    classes: [...(rule.classes ?? [])],
    destinationsAll: rule.destinations === undefined,
    destinations: [...(rule.destinations ?? [])],
    except: [...(rule.except ?? [])],
    deny: [...(rule.deny ?? [])],
    endpointsAll: rule.endpoints === undefined,
    endpoints: (rule.endpoints ?? []).join("\n"),
  };
}

export const endpointLines = (text: string) =>
  text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);

/** ruleFromDraft is the body a save sends: the loaded rule with the draft's fields over it. A key
 *  the editor does not know stays as loaded, and a field the draft marks "all" is removed rather
 *  than sent as an empty list (which would mean "none"). */
export function ruleFromDraft(base: RouteRule, d: RuleDraft): RouteRule {
  const out: RouteRule = { ...base };
  const set = (k: keyof RouteRule & string, all: boolean, list: string[]) => {
    if (all) delete out[k];
    else out[k] = [...list];
  };
  set("classes", d.classesAll, d.classes);
  set("destinations", d.destinationsAll, d.destinations);
  // An empty exception or deny list is the same as none on the server (Normalize drops it).
  set("except", d.except.length === 0, d.except);
  set("deny", d.deny.length === 0, d.deny);
  set("endpoints", d.endpointsAll, endpointLines(d.endpoints));
  return out;
}

const sameList = (a: string[] | undefined, b: string[] | undefined) => {
  if (a === undefined || b === undefined) return a === b;
  const x = [...a].sort();
  const y = [...b].sort();
  return x.length === y.length && x.every((v, i) => v === y[i]);
};

/** ruleEqual compares two rules by meaning: list order does not count, absent and empty do,
 *  except for `except` and `deny`, where the server reads them alike. */
export function ruleEqual(a: RouteRule, b: RouteRule): boolean {
  const opt = (l: string[] | undefined) => (l && l.length > 0 ? l : undefined);
  return (
    sameList(a.classes, b.classes) &&
    sameList(a.destinations, b.destinations) &&
    sameList(opt(a.except), opt(b.except)) &&
    sameList(opt(a.deny), opt(b.deny)) &&
    sameList(a.endpoints, b.endpoints)
  );
}

/** restricts mirrors routepolicy.Rule.Restricted: an exception alone restricts nothing. */
export const restricts = (r: RouteRule) =>
  r.classes !== undefined || r.destinations !== undefined || (r.deny?.length ?? 0) > 0;

/** routesAllowedAbove: the routes no layer above the tenant forbids, i.e. what the deployment
 *  leaves the tenant's own rule to narrow. */
export const routesAllowedAbove = (routes: RouteDecision[]) =>
  routes.filter((r) => (r.forbiddenBy ?? []).every((l) => l === "tenant"));
