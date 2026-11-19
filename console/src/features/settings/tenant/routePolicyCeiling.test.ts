// Presets and "match the operator's setting" (#4380 P-5b) only ever rewrite the draft; the body a
// save sends is still ruleFromDraft's. The match cases are a golden that routepolicysrv
// TestMatchCases replays through the CP's own PUT handlers: each rule is refused under its
// ceiling, and its matched rule is accepted. AF_UPDATE_MATCH_CASES=1 rewrites the file.
import { describe, it, expect } from "vitest";
import { readFileSync, writeFileSync } from "node:fs";
import { PRESET_KEYS, allows, presetAvailable, applyPreset, exceeds, matchCeiling, stoppedBy } from "./routePolicyCeiling.ts";
import { draftOf, ruleEqual, ruleFromDraft, type RouteRule, type Vocabulary } from "./routePolicyRule.ts";

const DIR = "scripts/shots/model-access";
// The vocabulary the CP serves, as the real handlers wrote it into the fixtures.
const VOCAB: Vocabulary = JSON.parse(readFileSync(`${DIR}/policy-open.json`, "utf8"))["deployment"].vocabulary;
const ceilingOf = (name: string): { raw: string; rule: RouteRule } =>
  JSON.parse(readFileSync(`${DIR}/policy-${name}.json`, "utf8"))["deployment"].ceiling;

describe("presets", () => {
  // A loaded rule with everything a preset must not touch: endpoints, and a key from a newer CP.
  const BASE: RouteRule = {
    classes: ["member-key"],
    except: ["copilot-login"],
    deny: ["gemini-api"],
    endpoints: ["llm.corp.example"],
    future: { kept: true },
  };
  it.each([
    ["none", { endpoints: ["llm.corp.example"], future: { kept: true } }],
    [
      "no_subscriptions",
      {
        classes: ["member-cloud", "member-key", "provided", "self-hosted"],
        destinations: [
          "af-engine", "anthropic", "aws-bedrock", "cursor", "github", "google", "kiro", "meta", "openai", "openrouter", "sakana", "self-hosted",
        ],
        endpoints: ["llm.corp.example"],
        future: { kept: true },
      },
    ],
    [
      "no_plan_logins",
      {
        classes: ["member-cloud", "member-key", "provided", "self-hosted"],
        endpoints: ["llm.corp.example"],
        future: { kept: true },
      },
    ],
    ["bedrock_only", { destinations: ["aws-bedrock"], endpoints: ["llm.corp.example"], future: { kept: true } }],
    ["provided_only", { classes: ["provided"], endpoints: ["llm.corp.example"], future: { kept: true } }],
  ] as const)("%s rewrites the draft and keeps the endpoints and unknown keys", (key, want) => {
    const d = applyPreset(draftOf(BASE), key, VOCAB);
    const body = ruleFromDraft(BASE, d);
    expect(body).toEqual(want);
    // Applying it again changes nothing: this is what marks the preset as the current one.
    expect(ruleEqual(ruleFromDraft(BASE, applyPreset(d, key, VOCAB)), body)).toBe(true);
    // And no other preset reads as current.
    for (const other of PRESET_KEYS.filter((k) => k !== key)) {
      expect(ruleEqual(ruleFromDraft(BASE, applyPreset(d, other, VOCAB)), body), other).toBe(false);
    }
  });
});

describe("preset availability", () => {
  it("offers every preset on the CP's vocabulary and drops one whose target the CP no longer has", () => {
    expect(PRESET_KEYS.filter((k) => presetAvailable(k, VOCAB))).toEqual([...PRESET_KEYS]);
    const noBedrock = { ...VOCAB, destinations: VOCAB.destinations.filter((d) => d !== "aws-bedrock") };
    expect(PRESET_KEYS.filter((k) => !presetAvailable(k, noBedrock))).toEqual(["bedrock_only"]);
    const noProvided = { ...VOCAB, classes: VOCAB.classes.filter((c) => c !== "provided") };
    expect(PRESET_KEYS.filter((k) => !presetAvailable(k, noProvided))).toEqual(["provided_only"]);
  });
});

describe("allows and exceeds, against the CP fixtures", () => {
  it("agree with the CP's decisions on every route", () => {
    for (const name of ["open", "restricted", "unapproved", "bedrockonly", "nosubs"]) {
      const v = JSON.parse(readFileSync(`${DIR}/policy-${name}.json`, "utf8"))["tenant/demo"];
      const layers = [v.ceiling.rule, v.deployment.rule, v.cap.rule, v.tenant.rule];
      for (const r of v.routes as { id: string; allowed: boolean }[]) {
        // Per-connection routes are decided by host approval, which this rule-level mirror does not see.
        if (r.id === "custom-api" || r.id === "member-llamacpp") continue;
        expect(
          layers.every((l: RouteRule) => allows(l, r.id, VOCAB)),
          `${name} ${r.id}`,
        ).toBe(r.allowed);
      }
    }
  });

  it("names the routes and patterns the ceiling forbids", () => {
    const ceiling = ceilingOf("restricted").rule;
    expect(exceeds({ deny: ["cursor-login"] }, ceiling, VOCAB).routes).toEqual(["openrouter-api", "sakana-api"]);
    expect(
      exceeds({ destinations: ["anthropic"], endpoints: ["*.corp.example", "x.other.example"] }, ceiling, VOCAB),
    ).toEqual({ routes: [], endpoints: ["x.other.example"] });
    // Compared as the CP stores it: case and a trailing dot do not matter.
    expect(
      exceeds({ destinations: ["anthropic"], endpoints: ["LLM.Corp.Example."] }, ceiling, VOCAB).endpoints,
    ).toEqual([]);
    // An absent endpoint list leaves the ceiling in charge: nothing to refuse.
    expect(exceeds({ destinations: ["anthropic"] }, ceiling, VOCAB)).toEqual({ routes: [], endpoints: [] });
  });
});

// Endpoint patterns are compared in the CP's canonical spelling (routepolicy.NormalizePattern):
// lower case, no trailing dot, punycode. Each row: [ceiling pattern, rule pattern, covered]. The
// expectations are what routepolicy covers() answers once both sides are normalised; the CP's own
// refusals (idna.Lookup: STD3 rules, hyphen checks) are rows that must come out as "exceeds".
describe("endpoint patterns with internationalised host names", () => {
  const ceilingWith = (p: string): RouteRule => ({ destinations: ["anthropic"], endpoints: [p] });
  const covered = (outer: string, inner: string) =>
    exceeds({ destinations: ["anthropic"], endpoints: [inner] }, ceilingWith(outer), VOCAB).endpoints.length === 0;

  it.each([
    // ASCII, case, trailing dot.
    ["llm.corp.example", "llm.corp.example", true],
    ["llm.corp.example", "LLM.Corp.Example.", true],
    ["*.corp.example", "a.b.corp.example", true],
    ["*.corp.example", "corp.example", false],
    // A unicode host and its punycode spelling are one host, whichever side is which.
    ["xn--bcher-kva.example", "bücher.example", true],
    ["bücher.example", "xn--bcher-kva.example", true],
    ["bücher.example", "BÜCHER.example.", true],
    ["xn--bcher-kva.example", "xn--bcher-kva.example", true],
    // Wildcards with an IDN suffix, in both spellings.
    ["*.bücher.example", "llm.xn--bcher-kva.example", true],
    ["*.xn--bcher-kva.example", "llm.bücher.example", true],
    ["*.bücher.example", "*.shop.bücher.example", true],
    ["*.bücher.example", "*.xn--bcher-kva.example", true],
    ["*.bücher.example", "bücher.example", false],
    ["*.bücher.example", "llm.buecher.example", false],
    // Different hosts stay different.
    ["bücher.example", "bucher.example", false],
    ["*.corp.example", "x.bücher.example", false],
    // What the CP refuses is never read as covered: a bad label, a port, an empty label, a bare
    // wildcard, a non-canonical IPv4, a wildcard over an IP, a scheme or userinfo. (An empty label
    // and "*.0.0.5" the CP lets through as host names; the Console answers "exceeds" for them,
    // the safe side.)
    ["*.corp.example", "a_b.corp.example", false],
    ["*.corp.example", "-a.corp.example", false],
    ["*.corp.example", "ab--c.corp.example", false],
    ["*.corp.example", "a..corp.example", false],
    ["*.corp.example", "a.corp.example:8080", false],
    ["*.corp.example", "a.*.corp.example", false],
    ["10.0.0.5", "10.0.0.5", true],
    ["10.0.0.5", "0xa.0.0.5", false],
    ["*.0.0.5", "*.0.0.5", false],
    ["*.10.0.0.5", "*.10.0.0.5", false],
    ["[2001:db8::1]", "2001:DB8::1", true],
    ["*.corp.example", "xn--a.corp.example", false],
    ["*.corp.example", "xn--0.corp.example", false],
    ["*.corp.example", "xn---a.corp.example", false],
    ["*.corp.example", "xn--invalid-punycode-zzzzzzzzz.corp.example", false],
    ["*.corp.example", "xn--bcher-kva.corp.example", true],
    ["*.corp.example", "ab--ü.corp.example", false],
    ["*.corp.example", "xn--ab---3ra.corp.example", false],
    ["*.corp.example", "ü--a.corp.example", false],
    ["*.corp.example", "http://a.corp.example", false],
    ["*.corp.example", "https://evil.com/a.corp.example", false],
    ["*.corp.example", "user:pass@a.corp.example", false],
    ["*.corp.example", "a:b.corp.example", false],
    ["10.0.0.5", "10.0.0.5.", false],
    ["[2001:db8::1]", "2001:0db8::1", true],
    ["[2001:db8::1]", "[2001:db8::2]", false],
  ] as const)("ceiling %s, pattern %s: covered %s", (outer, inner, want) => {
    expect(covered(outer, inner)).toBe(want);
  });
});

describe("stoppedBy", () => {
  it("names each layer above that narrows the field without the value", () => {
    const above = [
      { layer: "ceiling", rule: { destinations: ["anthropic", "openai"] } },
      { layer: "deployment", rule: {} },
      { layer: "tenant_cap", rule: { destinations: ["anthropic"] } },
    ];
    expect(stoppedBy(above, "destinations", "openai")).toEqual(["tenant_cap"]);
    expect(stoppedBy(above, "destinations", "google")).toEqual(["ceiling", "tenant_cap"]);
    expect(stoppedBy(above, "classes", "subscription")).toEqual([]);
  });
});

// The ceilings: two the CP generated (the shots fixtures) and two by their operator strings,
// which TestMatchCases parses and compares with the rule written here.
const CEILINGS: { raw: string; rule: RouteRule }[] = [
  ceilingOf("restricted"),
  ceilingOf("bedrockonly"),
  { raw: "classes=-subscription", rule: { classes: ["member-cloud", "member-key", "provided", "self-hosted"] } },
  { raw: "classes=provided", rule: { classes: ["provided"] } },
  // A ceiling's own blocks and allowances: match adds the first and drops what the rule allows
  // individually against them.
  {
    raw: "classes=-subscription;deny=openrouter-api",
    rule: { classes: ["member-cloud", "member-key", "provided", "self-hosted"], deny: ["openrouter-api"] },
  },
  {
    raw: "destinations=anthropic,aws-bedrock;except=copilot-login",
    rule: { destinations: ["anthropic", "aws-bedrock"], except: ["copilot-login"] },
  },
];
const RULES: RouteRule[] = [
  {},
  { deny: ["cursor-login"] },
  { classes: ["member-key"], except: ["openrouter-api"] },
  { classes: ["subscription", "member-key"] },
  { destinations: ["openrouter", "anthropic"] },
  { destinations: ["anthropic"], endpoints: ["*.corp.example", "*.other.example"] },
  // As typed: the CP lower-cases and drops a trailing dot before it compares, so the first stays.
  { destinations: ["anthropic"], endpoints: ["LLM.Corp.Example.", "x.other.example"] },
  { except: ["openrouter-api", "copilot-login"] },
  // No key the Console does not know: the CP refuses one (the presets pin that it is carried).
  { endpoints: ["llm.corp.example"] },
];

describe("match the operator's setting", () => {
  const cases = CEILINGS.flatMap((c) =>
    RULES.filter((r) => {
      const x = exceeds(r, c.rule, VOCAB);
      return x.routes.length + x.endpoints.length > 0;
    }).map((rule) => ({
      ceilingRaw: c.raw,
      ceiling: c.rule,
      rule,
      matched: ruleFromDraft(rule, matchCeiling(draftOf(rule), c.rule, VOCAB)),
    })),
  );

  it("leaves nothing the ceiling refuses, and never allows a route the rule did not", () => {
    expect(cases.length).toBeGreaterThan(15);
    for (const c of cases) {
      expect(exceeds(c.matched, c.ceiling, VOCAB), JSON.stringify(c)).toEqual({ routes: [], endpoints: [] });
      for (const rt of [...VOCAB.routes.map((r) => r.id), "no-such-route"]) {
        if (allows(c.matched, rt, VOCAB)) expect(allows(c.rule, rt, VOCAB), `${JSON.stringify(c)} ${rt}`).toBe(true);
      }
    }
  });

  it("matches the golden the CP replays (match-cases.json)", () => {
    const path = `${DIR}/match-cases.json`;
    const text = JSON.stringify({ cases }, null, 1) + "\n";
    if (process.env.AF_UPDATE_MATCH_CASES === "1") writeFileSync(path, text);
    expect(readFileSync(path, "utf8")).toBe(text);
  });
});
