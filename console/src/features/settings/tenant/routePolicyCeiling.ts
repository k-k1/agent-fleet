// What the layers above an editor already stop, and help that only ever rewrites the draft
// (#4380 P-5b; ADR 0138 §6, §8 M1; #4376 item 5): the presets, the marks on options stopped above,
// and "match the operator's setting", which narrows a draft until the CP's ceiling check
// (routepolicy.Rule.Exceeds, EndpointsExceed) has nothing to refuse.
//
// Nothing here saves. Every function returns a draft; what is sent is still ruleFromDraft's.
//
// The route test mirrors routepolicy.Rule.Allows over the vocabulary the CP serves; match-cases.json, written from here, is replayed through the
// CP's own PUT handlers by routepolicysrv TestMatchCases, so a drift between the two is a failing
// Go test, not a refusal an administrator meets.
import { endpointLines, restricts, type RouteRule, type RuleDraft, type Vocabulary } from "./routePolicyRule.ts";

/** allows mirrors routepolicy.Rule.Allows. */
export function allows(r: RouteRule, id: string, vocab: Vocabulary): boolean {
  if (!restricts(r)) return true;
  if (r.deny?.includes(id)) return false;
  if (r.except?.includes(id)) return true;
  const rt = vocab.routes.find((x) => x.id === id);
  if (!rt || rt.destination === "unclassified") return false;
  return (
    (r.classes === undefined || r.classes.includes(rt.class)) &&
    (r.destinations === undefined || r.destinations.includes(rt.destination))
  );
}

// The CP compares patterns as it stores them (routepolicy.NormalizePattern): lower case, no
// trailing dot, an internationalised name in its punycode form. Without this, "LLM.Corp.Example."
// reads as outside "*.corp.example", and "bücher.example" outside "*.xn--bcher-kva.example".
//
// The punycode step is the platform's URL parser (UTS 46 ToASCII), which is more lenient than the
// CP's idna.Lookup profile (STD3 rules, hyphen checks). So its output is re-checked label by label
// against the strict subset, and whatever the CP might refuse yields null: a null never matches
// anything, so the check answers "exceeds" and is never more permissive than the CP.
function asciiHost(s: string): string | null {
  if (s === "" || /[\s/:@?#[\]%*\\]/.test(s)) return null;
  let h: string;
  try {
    h = new URL(`http://${s}/`).hostname;
  } catch {
    return null;
  }
  const labels = h.split(".");
  // A numeric last label is an IPv4 address to the URL parser ("0x7f.1" becomes 127.0.0.1) but a
  // plain name to the CP, so only the canonical dotted quad is accepted as one.
  if (/^(0x[0-9a-f]*|\d+)$/.test(labels[labels.length - 1]) && h !== s) return null;
  for (const l of labels) {
    if (!/^[a-z0-9-]+$/.test(l) || l.startsWith("-") || l.endsWith("-")) return null;
    if (l.slice(2, 4) === "--" && !l.startsWith("xn--")) return null;
  }
  return h;
}

// normPattern is the CP's canonical spelling of one pattern, or null when it might refuse it.
function normPattern(raw: string): string | null {
  let s = raw.trim().toLowerCase();
  const wild = s.startsWith("*.");
  if (wild) s = s.slice(2);
  if (s.startsWith("[") && s.endsWith("]")) s = s.slice(1, -1);
  // An IP literal stays as typed (the CP's canonical text is the same for what an administrator
  // types); a wildcard over one is refused by the CP.
  if (s.includes(":") || /^\d+\.\d+\.\d+\.\d+$/.test(s)) return wild ? null : s;
  s = s.replace(/\.$/, "");
  const h = asciiHost(s);
  return h === null ? null : (wild ? "*." : "") + h;
}

// covers mirrors routepolicy's covers: an exact host or a "*." pattern holding another pattern.
// A pattern the CP might refuse is covered by nothing.
function covers(outer: string, inner: string): boolean {
  const o = normPattern(outer);
  const i = normPattern(inner);
  if (o === null || i === null) return false;
  if (i.startsWith("*.")) {
    return o.startsWith("*.") && (i.slice(2) === o.slice(2) || i.slice(2).endsWith("." + o.slice(2)));
  }
  return o.startsWith("*.") ? i.endsWith("." + o.slice(2)) : o === i;
}

/** exceeds lists what the CP would refuse writing rule under the operator's ceiling: the routes it
 *  would allow that the ceiling forbids, then its endpoint patterns the ceiling does not cover. */
export function exceeds(
  rule: RouteRule,
  ceiling: RouteRule,
  vocab: Vocabulary,
): { routes: string[]; endpoints: string[] } {
  const routes = vocab.routes
    .filter((rt) => allows(rule, rt.id, vocab) && !allows(ceiling, rt.id, vocab))
    .map((rt) => rt.id);
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
export function matchCeiling(d: RuleDraft, ceiling: RouteRule, vocab: Vocabulary): RuleDraft {
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
    except: d.except.filter((id) => allows(ceiling, id, vocab)),
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

/** The ADR 0138 §6 configurations, as rules over the CP's vocabulary. Each replaces the draft's
 *  credential types, destinations and individual changes; the endpoints and any key the Console
 *  does not know stay as they are. */
export const PRESET_KEYS = ["none", "no_subscriptions", "no_plan_logins", "bedrock_only", "provided_only"] as const;

export type PresetKey = (typeof PRESET_KEYS)[number];

/** presetAvailable: a preset that names a value outright is offered only while the CP's vocabulary
 *  has it, so a button never fills in a draft the CP would refuse. */
export function presetAvailable(key: PresetKey, vocab: Vocabulary): boolean {
  const p = presetRule(key, vocab);
  return (
    (p.classes ?? []).every((c) => vocab.classes.includes(c)) &&
    (p.destinations ?? []).every((d) => vocab.destinations.includes(d))
  );
}

export function presetRule(key: PresetKey, vocab: Vocabulary): RouteRule {
  const noSubs = vocab.classes.filter((c) => c !== "subscription");
  switch (key) {
    case "none":
      return {};
    // #4159: no subscriptions, and no opencode.ai, whose Zen key is metered but its plans are not.
    case "no_subscriptions":
      return { classes: noSubs, destinations: vocab.destinations.filter((d) => d !== "opencode.ai") };
    case "no_plan_logins":
      return { classes: noSubs };
    case "bedrock_only":
      return { destinations: ["aws-bedrock"] };
    case "provided_only":
      return { classes: ["provided"] };
  }
}

export function applyPreset(d: RuleDraft, key: PresetKey, vocab: Vocabulary): RuleDraft {
  const p = presetRule(key, vocab);
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
