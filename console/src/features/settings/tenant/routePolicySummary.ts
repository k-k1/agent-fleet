// The tenant Overview's two answers, as data (ADR 0138 D10a, D16; #4380 P-4): what each agent,
// image provider and chat backend can use, and what each layer restricts. Pure functions over the
// parsed wire, so they are tested against the CP's own answers (scripts/shots/model-access) and
// the component only words them.
//
// Fail closed: the list of what members can use comes from a well-formed `owners`, or, from a CP
// that sends none, from a well-formed `hidden` (which says only what is hidden). Anything else is
// "could not be read", never "everything is usable".
import {
  ROUTE_CLASSES,
  ROUTE_DESTINATIONS,
  isPerConnection,
  parseHidden,
  parseOwners,
  withExtra,
  type RouteRule,
  type TenantPolicyView,
} from "./routePolicyRule.ts";

/** The order layers are named in, top first (routepolicysrv layerOrder). */
export const LAYERS = ["ceiling", "deployment", "tenant_cap", "tenant"] as const;

/** One narrowed list of a layer: what it allows out of how many, and what it leaves out. */
export interface ListSummary {
  allowed: string[];
  total: number;
  missing: string[];
}

export interface LayerSummary {
  /** The layer narrows nothing and names nothing: "No restriction". */
  none: boolean;
  classes?: ListSummary;
  destinations?: ListSummary;
  except: string[];
  deny: string[];
  /** Host patterns (or, on the tenant's own layer, hosts); undefined when not narrowed. */
  endpoints?: string[];
  /** The ceiling restricts destinations without naming a pattern, so no host may be approved. */
  noEndpoints: boolean;
}

function listOf(known: readonly string[], held: string[] | undefined): ListSummary | undefined {
  if (held === undefined) return undefined;
  const all = withExtra(known, held);
  return {
    allowed: all.filter((v) => held.includes(v)),
    total: all.length,
    missing: all.filter((v) => !held.includes(v)),
  };
}

/** summarizeLayer is one layer's rule as the overview words it. ceiling marks the operator's layer,
 *  whose unset endpoints mean "none" once it restricts destinations (routepolicy). */
export function summarizeLayer(rule: RouteRule, ceiling = false): LayerSummary {
  const except = rule.except ?? [];
  const deny = rule.deny ?? [];
  const noEndpoints = ceiling && rule.endpoints === undefined && rule.destinations !== undefined;
  const s: LayerSummary = {
    none: false,
    classes: listOf(ROUTE_CLASSES, rule.classes),
    destinations: listOf(ROUTE_DESTINATIONS, rule.destinations),
    except,
    deny,
    endpoints: rule.endpoints,
    noEndpoints,
  };
  s.none = !s.classes && !s.destinations && except.length === 0 && deny.length === 0 && !s.endpoints && !noEndpoints;
  return s;
}

/** The four layers of a tenant view, top first. */
export const layerRules = (v: TenantPolicyView): Record<(typeof LAYERS)[number], RouteRule> => ({
  ceiling: v.ceiling.rule,
  deployment: v.deployment.rule,
  tenant_cap: v.cap.rule,
  tenant: v.tenant.rule,
});

export interface RouteRow {
  id: string;
  allowed: boolean;
  /** The layers that forbid it, top first; empty for an allowed route. */
  stoppedBy: string[];
  /** Not allowed although no layer forbids it: a per-connection route without an approved host. */
  needsServer: boolean;
}

export interface OwnerRow {
  id: string;
  usable: boolean;
  routes: RouteRow[];
  /** Every layer that forbids one of its routes, top first. */
  stoppedBy: string[];
}

export interface OwnerGroups {
  kinds: OwnerRow[];
  imagegen: OwnerRow[];
  chat: OwnerRow[];
}

export type MemberUse =
  | { state: "unavailable" }
  /** From owners: every item, usable or not. */
  | { state: "owners"; groups: OwnerGroups }
  /** From a CP without owners: only the hidden items can be named. */
  | { state: "hiddenOnly"; groups: OwnerGroups };

const ordered = (ls: Iterable<string>) => {
  const set = new Set(ls);
  return [...LAYERS.filter((l) => set.has(l)), ...[...set].filter((l) => !(LAYERS as readonly string[]).includes(l))];
};

/** memberUse folds a tenant view into the overview's list. */
export function memberUse(v: TenantPolicyView): MemberUse {
  const byRoute = new Map(v.routes.map((r) => [r.id, r.forbiddenBy ?? []]));
  const routeRow = (id: string, allowed: boolean, fallback: string[] = []): RouteRow => {
    const by = allowed ? [] : ordered(byRoute.get(id)?.length ? byRoute.get(id)! : fallback);
    return { id, allowed, stoppedBy: by, needsServer: !allowed && by.length === 0 && isPerConnection(id) };
  };
  const row = (id: string, routes: RouteRow[]): OwnerRow => ({
    id,
    usable: routes.some((r) => r.allowed),
    routes,
    stoppedBy: ordered(routes.flatMap((r) => r.stoppedBy)),
  });
  const owners = parseOwners(v.owners);
  if (owners === null) return { state: "unavailable" };
  if (owners) {
    const fold = (items: typeof owners.kinds) =>
      items.map((it) =>
        row(
          it.id,
          it.routes.map((r) => routeRow(r, it.allowed.includes(r))),
        ),
      );
    return {
      state: "owners",
      groups: { kinds: fold(owners.kinds), imagegen: fold(owners.imagegen), chat: fold(owners.chat) },
    };
  }
  const hidden = parseHidden(v.hidden);
  if (!hidden) return { state: "unavailable" };
  const fold = (items: typeof hidden.kinds) =>
    items.map((it) =>
      row(
        it.id,
        it.routes.map((r) => routeRow(r, false, it.forbiddenBy)),
      ),
    );
  return {
    state: "hiddenOnly",
    groups: { kinds: fold(hidden.kinds), imagegen: fold(hidden.imagegen), chat: fold(hidden.chat) },
  };
}
