// What the layers above an editor already stop, and help that only ever rewrites the draft
// (#4380 P-5b; ADR 0138 §6, §8 M1; #4376 item 5): the presets, the marks on options stopped above,
// and "match the operator's setting", which narrows a draft until the CP's ceiling check
// (routepolicy.Rule.Exceeds, EndpointsExceed) has nothing to refuse.
//
// Nothing here saves. Every function returns a draft; what is sent is still ruleFromDraft's.
//
// The route test mirrors routepolicy.Rule.Allows over ROUTE_CATALOG, which routePolicyRule.test.ts
// holds equal to the CP's catalogue; match-cases.json, written from here, is replayed through the
// CP's own PUT handlers by routepolicysrv TestMatchCases, so a drift between the two is a failing
// Go test, not a refusal an administrator meets.
import {
  ROUTE_CATALOG,
  ROUTE_CLASSES,
  ROUTE_DESTINATIONS,
  endpointLines,
  restricts,
  type RouteRule,
  type RuleDraft,
} from "./routePolicyRule.ts";

/** allows mirrors routepolicy.Rule.Allows. */
export function allows(r: RouteRule, id: string): boolean {
  if (!restricts(r)) return true;
  if (r.deny?.includes(id)) return false;
  if (r.except?.includes(id)) return true;
  const rt = ROUTE_CATALOG.find((x) => x.id === id);
  if (!rt || rt.destination === "unclassified") return false;
  return (
    (r.classes === undefined || r.classes.includes(rt.class)) &&
    (r.destinations === undefined || r.destinations.includes(rt.destination))
  );
}

// covers mirrors routepolicy's covers: an exact host or a "*." pattern holding another pattern.
function covers(o: string, i: string): boolean {
  if (i.startsWith("*.")) {
    return o.startsWith("*.") && (i.slice(2) === o.slice(2) || i.slice(2).endsWith("." + o.slice(2)));
  }
  return o.startsWith("*.") ? i.endsWith("." + o.slice(2)) : o === i;
}

/** exceeds lists what the CP would refuse writing rule under the operator's ceiling: the routes it
 *  would allow that the ceiling forbids, then its endpoint patterns the ceiling does not cover. */
export function exceeds(rule: RouteRule, ceiling: RouteRule): { routes: string[]; endpoints: string[] } {
  const routes = ROUTE_CATALOG.filter((rt) => allows(rule, rt.id) && !allows(ceiling, rt.id)).map((rt) => rt.id);
  // The ceiling narrows endpoints when it names patterns, or (no patterns) once it restricts destinations.
  const narrows = ceiling.endpoints !== undefined || ceiling.destinations !== undefined;
  const ps = ceiling.endpoints ?? [];
  const endpoints =
    rule.endpoints === undefined || !narrows ? [] : rule.endpoints.filter((p) => !ps.some((o) => covers(o, p)));
  return { routes, endpoints };
}

/** matchCeiling narrows a draft until the ceiling has nothing to refuse, and never widens it:
 *  each list is cut to what the ceiling allows, an individual allowance the ceiling forbids is
 *  dropped, the ceiling's own blocks are added, and patterns it does not cover are removed. */
export function matchCeiling(d: RuleDraft, ceiling: RouteRule): RuleDraft {
  if (!restricts(ceiling) && ceiling.endpoints === undefined) return d;
  const cut = (all: boolean, list: string[], above: string[] | undefined) =>
    above === undefined
      ? { all, list }
      : { all: false, list: all ? [...above] : list.filter((v) => above.includes(v)) };
  const c = cut(d.classesAll, d.classes, ceiling.classes);
  const t = cut(d.destinationsAll, d.destinations, ceiling.destinations);
  const deny = [...d.deny];
  for (const id of ceiling.deny ?? []) if (!deny.includes(id)) deny.push(id);
  const out: RuleDraft = {
    ...d,
    classesAll: c.all,
    classes: c.list,
    destinationsAll: t.all,
    destinations: t.list,
    except: d.except.filter((id) => allows(ceiling, id)),
    deny,
  };
  if (!d.endpointsAll) {
    const narrows = ceiling.endpoints !== undefined || ceiling.destinations !== undefined;
    const ps = ceiling.endpoints ?? [];
    if (narrows)
      out.endpoints = endpointLines(d.endpoints)
        .filter((p) => ps.some((o) => covers(o, p)))
        .join("\n");
  }
  return out;
}

/** stoppedBy names the layers above that leave an option out: a class or destination one of them
 *  narrows to a list without it. */
export function stoppedBy(
  above: readonly { layer: string; rule: RouteRule }[],
  field: "classes" | "destinations",
  value: string,
): string[] {
  return above.filter((a) => a.rule[field] !== undefined && !a.rule[field]!.includes(value)).map((a) => a.layer);
}

/** The ADR 0138 §6 configurations. Each replaces the draft's credential types, destinations and
 *  individual changes; the endpoints and any key the Console does not know stay as they are. */
export const PRESETS = [
  { key: "none", rule: {} },
  // #4159: no subscriptions, and no opencode.ai, whose Zen key is metered but its plans are not.
  {
    key: "no_subscriptions",
    rule: {
      classes: ROUTE_CLASSES.filter((c) => c !== "subscription"),
      destinations: ROUTE_DESTINATIONS.filter((d) => d !== "opencode.ai"),
    },
  },
  { key: "no_plan_logins", rule: { classes: ROUTE_CLASSES.filter((c) => c !== "subscription") } },
  { key: "bedrock_only", rule: { destinations: ["aws-bedrock"] } },
  { key: "provided_only", rule: { classes: ["provided"] } },
] as const satisfies readonly { key: string; rule: RouteRule }[];

export type PresetKey = (typeof PRESETS)[number]["key"];

export function applyPreset(d: RuleDraft, key: PresetKey): RuleDraft {
  const p: RouteRule = PRESETS.find((x) => x.key === key)!.rule;
  return {
    ...d,
    classesAll: p.classes === undefined,
    classes: [...(p.classes ?? [])],
    destinationsAll: p.destinations === undefined,
    destinations: [...(p.destinations ?? [])],
    except: [],
    deny: [],
  };
}
