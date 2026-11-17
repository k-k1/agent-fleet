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
  /** What a rule may name; parseTenantPolicy refuses an answer without it. */
  vocabulary: Vocabulary;
  /** What the layers hide from the tenant's members; read through parseHidden. */
  hidden?: unknown;
  /** Every harness, chat backend and image provider with its routes and the allowed ones; read
   *  through parseOwners. Absent from a CP older than #4381. */
  owners?: unknown;
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

/** ownerItemWire: one harness, chat backend or image provider, its routes, and the ones the
 *  layers allow (empty exactly when it is hidden). */
export interface OwnerItem {
  id: string;
  routes: string[];
  allowed: string[];
}

/** ownersWire. */
export interface OwnersView {
  kinds: OwnerItem[];
  chat: OwnerItem[];
  imagegen: OwnerItem[];
}

/** GET /api/admin/route-policy. */
export interface DeploymentPolicyView {
  ceiling: PolicyCeiling;
  deployment: PolicyLayer;
  revision: number;
  /** What a rule may name; parseDeploymentPolicy refuses an answer without it. */
  vocabulary: Vocabulary;
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
  if (!isObj(d) || !isCeiling(d.ceiling) || !isLayer(d.deployment) || !isLayer(d.cap) || !isLayer(d.tenant))
    return null;
  if (!Array.isArray(d.routes) || !Array.isArray(d.endpoints) || !parseVocabulary(d.vocabulary)) return null;
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

const isOwnerItem = (v: unknown): v is OwnerItem =>
  isObj(v) &&
  typeof v.id === "string" &&
  Array.isArray(v.routes) &&
  isStrList(v.routes) &&
  v.routes.length > 0 &&
  Array.isArray(v.allowed) &&
  isStrList(v.allowed) &&
  // An allowed route the item does not have is an answer the page cannot draw truthfully.
  (v.allowed as string[]).every((r) => (v.routes as string[]).includes(r));

/** parseOwners: undefined when the answer carries no owners at all (a CP from before them), null
 *  when it carries a malformed one, which fails the whole list closed. */
export function parseOwners(d: unknown): OwnersView | null | undefined {
  if (d === undefined) return undefined;
  if (!isObj(d)) return null;
  for (const k of ["kinds", "chat", "imagegen"]) {
    const l = d[k];
    if (!Array.isArray(l) || !l.every(isOwnerItem)) return null;
  }
  return d as unknown as OwnersView;
}

export function parseDeploymentPolicy(d: unknown): DeploymentPolicyView | null {
  if (!isObj(d) || !isCeiling(d.ceiling) || !isLayer(d.deployment) || !parseVocabulary(d.vocabulary)) return null;
  return d as unknown as DeploymentPolicyView;
}

/** One catalogued route as the CP's vocabulary lists it. */
export interface CatalogRoute {
  id: string;
  class: string;
  destination: string;
  /** Its class and destination come from the endpoint it is bound to; it cannot be excepted. */
  perConnection?: boolean;
}

/** vocabularyWire: what a rule may name, served by the CP beside the policy (single source:
 *  the route catalogue the CP validates against). The destinations leave out "unclassified",
 *  which the CP refuses in an allow list; a route's own destination may still be it. */
export interface Vocabulary {
  classes: string[];
  destinations: string[];
  routes: CatalogRoute[];
}

const isCatalogRoute = (v: unknown): v is CatalogRoute =>
  isObj(v) &&
  typeof v.id === "string" &&
  typeof v.class === "string" &&
  typeof v.destination === "string" &&
  (v.perConnection === undefined || typeof v.perConnection === "boolean");

/** parseVocabulary is null unless all three lists are present and well formed: a partial answer
 *  must never become pickers that offer less than the CP accepts. Ids the Console has no label
 *  for are kept; they are drawn as the raw id. */
export function parseVocabulary(d: unknown): Vocabulary | null {
  if (!isObj(d) || !Array.isArray(d.classes) || !Array.isArray(d.destinations) || !Array.isArray(d.routes)) return null;
  if (!isStrList(d.classes) || !isStrList(d.destinations) || !d.routes.every(isCatalogRoute)) return null;
  return d as unknown as Vocabulary;
}

/** isOlderPolicyAnswer: the answer is a well-formed policy that carries no vocabulary at all, as
 *  a Control Plane from before it sends. The page says so and draws no editor, rather than
 *  falling back to a list of its own that could offer a value that CP refuses. */
export function isOlderPolicyAnswer(d: unknown): boolean {
  return isObj(d) && d.vocabulary === undefined && isCeiling(d.ceiling) && isLayer(d.deployment);
}

/** The vocabulary's route ids plus any the caller names that it does not list, sorted. */
export function routeIds(vocab: Vocabulary, ...extra: (string[] | undefined)[]): string[] {
  const out = new Set(vocab.routes.map((r) => r.id));
  for (const l of extra) for (const id of l ?? []) out.add(id);
  return [...out].sort();
}

export const isPerConnection = (vocab: Vocabulary, id: string) =>
  vocab.routes.some((r) => r.id === id && r.perConnection);

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
