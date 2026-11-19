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
    if (l.slice(2, 4) === "--" && !(l.startsWith("xn--") && validPunycodeLabel(l))) return null;
  }
  return h;
}

// punycodeDecode is RFC 3492 decoding of the part after "xn--"; null when it is malformed.
function punycodeDecode(input: string): string | null {
  const base = 36, tMin = 1, tMax = 26, skew = 38, damp = 700;
  const out: number[] = [];
  const d = input.lastIndexOf("-");
  for (let j = 0; j < Math.max(d, 0); j++) {
    const c = input.charCodeAt(j);
    if (c >= 0x80) return null;
    out.push(c);
  }
  let n = 128, bias = 72, i = 0;
  for (let k = d > 0 ? d + 1 : 0; k < input.length; ) {
    const oldi = i;
    for (let w = 1, kk = base; ; kk += base) {
      if (k >= input.length) return null;
      const c = input.charCodeAt(k++);
      const digit = c >= 48 && c <= 57 ? c - 22 : c >= 97 && c <= 122 ? c - 97 : base;
      if (digit >= base) return null;
      i += digit * w;
      const t = kk <= bias ? tMin : kk >= bias + tMax ? tMax : kk - bias;
      if (digit < t) break;
      w *= base - t;
      if (i > 0x7fffffff || w > 0x7fffffff) return null;
    }
    const len = out.length + 1;
    let delta = oldi === 0 ? Math.floor((i - oldi) / damp) : Math.floor((i - oldi) / 2);
    delta += Math.floor(delta / len);
    let kk = 0;
    while (delta > ((base - tMin) * tMax) >> 1) {
      delta = Math.floor(delta / (base - tMin));
      kk += base;
    }
    bias = kk + Math.floor(((base - tMin + 1) * delta) / (delta + skew));
    n += Math.floor(i / len);
    i %= len;
    if (n > 0x10ffff || (n >= 0xd800 && n <= 0xdfff)) return null;
    out.splice(i++, 0, n);
  }
  return String.fromCodePoint(...out);
}

// validPunycodeLabel: an "xn--" label the CP's idna.Lookup would accept. Chromium's URL parser
// passes "xn--a" through unchanged where Node's refuses it, so the label is decoded here and
// must re-encode, through the URL parser, to exactly itself (no non-minimal form, no invalid
// character, the same answer in every engine).
function validPunycodeLabel(l: string): boolean {
  const u = punycodeDecode(l.slice(4));
  if (u === null || u === "" || /^[\x00-\x7f]*$/.test(u) || u.startsWith("-") || u.endsWith("-")) return false;
  // idna's hyphen check runs on the decoded label's UTF-8 bytes: "--" at positions 3-4 is refused.
  const b = new TextEncoder().encode(u);
  if (b.length > 4 && b[2] === 45 && b[3] === 45) return false;
  try {
    return new URL(`http://${u}.invalid/`).hostname === `${l}.invalid`;
  } catch {
    return false;
  }
}

// normPattern is the CP's canonical spelling of one pattern, or null when it might refuse it.
function normPattern(raw: string): string | null {
  let s = raw.trim().toLowerCase();
  const wild = s.startsWith("*.");
  if (wild) s = s.slice(2);
  if (s.startsWith("[") && s.endsWith("]")) s = s.slice(1, -1);
  // A colon makes it an IPv6 literal, and nothing else: it must parse as one. A wildcard over an
  // IP is refused by the CP, and so is a scheme, port, path or userinfo (the CP's host check).
  if (s.includes(":")) {
    if (wild || /[\s/@?#[\]%*\\]/.test(s)) return null;
    try {
      const h = new URL(`http://[${s}]/`).hostname;
      if (!h.startsWith("[") || !h.endsWith("]")) return null;
      // An IPv4-mapped address reads as null here (the CP judges it as its IPv4 address): "exceeds".
      const ip = parseIp(h.slice(1, -1));
      return ip === null || refusedIp(ip) ? null : h.slice(1, -1);
    } catch {
      return null;
    }
  }
  const dotted = /^\d+\.\d+\.\d+\.\d+$/;
  // An IPv4 address stays as typed; with a trailing dot the CP no longer reads it as one and refuses it.
  if (dotted.test(s)) {
    const ip = parseIp(s);
    return wild || ip === null || refusedIp(ip) ? null : s;
  }
  if (s.endsWith(".")) {
    s = s.slice(0, -1);
    if (dotted.test(s)) return null;
  }
  const h = asciiHost(s);
  return h === null ? null : (wild ? "*." : "") + h;
}

// CIDR ranges (routepolicy endpoints_cidr.go). A range holds IP literals only; a host name never
// falls under one. The checks mirror the CP's refusals (a range must lie inside private address
// space, hold no metadata address, have no host bits and not be a single address), and any doubt
// yields null: a null range covers nothing and is covered by nothing, so the check says "exceeds".
interface Cidr {
  v: 4 | 6;
  n: bigint; // the address
  bits: number;
}

const PRIVATE_SPACE = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"];
const METADATA = ["100.100.100.200", "fd00:ec2::254", "fd00:ec2::23", "fd20:ce::254"];

// parseIp reads an IPv4 dotted quad (no leading zeros) or an IPv6 address; an IPv4-mapped one is
// not read (null): the CP unmaps it to the IPv4 address, and the Console answers "exceeds".
function parseIp(s: string): { v: 4 | 6; n: bigint } | null {
  if (s.includes(":")) {
    if (/[\s/@?#[\]%*\\]/.test(s)) return null;
    let h: string;
    try {
      h = new URL(`http://[${s}]/`).hostname;
    } catch {
      return null;
    }
    if (!h.startsWith("[") || !h.endsWith("]")) return null;
    const [head, tail, extra] = h.slice(1, -1).split("::");
    if (extra !== undefined) return null;
    const groups = (x: string) => (x === "" ? [] : x.split(":"));
    const hs = groups(head);
    const ts = tail === undefined ? [] : groups(tail);
    const fill = tail === undefined ? 0 : 8 - hs.length - ts.length;
    if (fill < 0 || (tail === undefined && hs.length !== 8)) return null;
    const all = [...hs, ...Array<string>(fill).fill("0"), ...ts];
    if (all.length !== 8 || !all.every((g) => /^[0-9a-f]{1,4}$/.test(g))) return null;
    const n = all.reduce((acc, g) => (acc << 16n) | BigInt(parseInt(g, 16)), 0n);
    if (n >> 32n === 0xffffn) return null; // ::ffff:0:0/96, IPv4-mapped
    return { v: 6, n };
  }
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(s);
  if (!m) return null;
  let n = 0n;
  for (const o of m.slice(1)) {
    if ((o.length > 1 && o.startsWith("0")) || Number(o) > 255) return null;
    n = (n << 8n) | BigInt(o);
  }
  return { v: 4, n };
}

// refusedIp mirrors routepolicy refusedAddr: link-local and instance-metadata addresses are never
// an approvable exact host.
function refusedIp(ip: { v: 4 | 6; n: bigint }): boolean {
  if (ip.v === 4 ? ip.n >> 16n === 0xa9fen : ip.n >> 118n === 0x3fan) return true; // 169.254/16, fe80::/10
  return METADATA.some((m) => {
    const a = parseIp(m)!;
    return a.v === ip.v && a.n === ip.n;
  });
}

const width = (v: 4 | 6) => (v === 4 ? 32 : 128);
const inRange = (outer: Cidr, v: 4 | 6, n: bigint) =>
  outer.v === v && n >> BigInt(width(v) - outer.bits) === outer.n >> BigInt(width(v) - outer.bits);

function rawCidr(s: string): Cidr | null {
  const m = /^([0-9a-f:.]+)\/(0|[1-9]\d{0,2})$/.exec(s.trim().toLowerCase());
  if (!m) return null;
  const a = parseIp(m[1]);
  const bits = Number(m[2]);
  if (a === null || bits > width(a.v)) return null;
  return { v: a.v, n: a.n, bits };
}

const PRIVATE = PRIVATE_SPACE.map((s) => rawCidr(s)!);

function parseCidr(s: string): Cidr | null {
  const c = rawCidr(s);
  if (c === null || c.bits === width(c.v)) return null;
  const hostBits = BigInt(width(c.v) - c.bits);
  if ((c.n >> hostBits) << hostBits !== c.n) return null;
  if (!PRIVATE.some((p) => p.v === c.v && p.bits <= c.bits && inRange(p, c.v, c.n))) return null;
  for (const m of METADATA) {
    const a = parseIp(m)!;
    if (inRange(c, a.v, a.n)) return null;
  }
  return c;
}

// covers mirrors routepolicy's covers: an exact host or a "*." pattern holding another pattern,
// and a range holding an IP literal or a narrower range. A pattern the CP might refuse is covered
// by nothing.
function covers(outer: string, inner: string): boolean {
  const oc = parseCidr(outer);
  const ic = parseCidr(inner);
  if (ic !== null) return oc !== null && oc.v === ic.v && oc.bits <= ic.bits && inRange(oc, ic.v, ic.n);
  if (oc !== null) {
    const i = normPattern(inner);
    const ip = i === null ? null : parseIp(i);
    return ip !== null && inRange(oc, ip.v, ip.n);
  }
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
