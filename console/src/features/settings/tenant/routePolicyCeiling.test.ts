// Presets and "match the operator's setting" (#4380 P-5b) only ever rewrite the draft; the body a
// save sends is still ruleFromDraft's. The match cases are a golden that routepolicysrv
// TestMatchCases replays through the CP's own PUT handlers: each rule is refused under its
// ceiling, and its matched rule is accepted. AF_UPDATE_MATCH_CASES=1 rewrites the file.
import { describe, it, expect } from "vitest";
import { readFileSync, writeFileSync } from "node:fs";
import { PRESETS, allows, applyPreset, exceeds, matchCeiling, stoppedBy } from "./routePolicyCeiling.ts";
import { draftOf, ruleEqual, ruleFromDraft, type RouteRule } from "./routePolicyRule.ts";

const DIR = "scripts/shots/model-access";
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
        classes: ["member-key", "member-cloud", "provided", "self-hosted"],
        destinations: [
          "anthropic",
          "openai",
          "google",
          "meta",
          "github",
          "cursor",
          "kiro",
          "openrouter",
          "sakana",
          "aws-bedrock",
          "af-engine",
          "self-hosted",
        ],
        endpoints: ["llm.corp.example"],
        future: { kept: true },
      },
    ],
    [
      "no_plan_logins",
      {
        classes: ["member-key", "member-cloud", "provided", "self-hosted"],
        endpoints: ["llm.corp.example"],
        future: { kept: true },
      },
    ],
    ["bedrock_only", { destinations: ["aws-bedrock"], endpoints: ["llm.corp.example"], future: { kept: true } }],
    ["provided_only", { classes: ["provided"], endpoints: ["llm.corp.example"], future: { kept: true } }],
  ] as const)("%s rewrites the draft and keeps the endpoints and unknown keys", (key, want) => {
    const d = applyPreset(draftOf(BASE), key);
    const body = ruleFromDraft(BASE, d);
    expect(body).toEqual(want);
    // Applying it again changes nothing: this is what marks the preset as the current one.
    expect(ruleEqual(ruleFromDraft(BASE, applyPreset(d, key)), body)).toBe(true);
    // And no other preset reads as current.
    for (const other of PRESETS.filter((p) => p.key !== key)) {
      expect(ruleEqual(ruleFromDraft(BASE, applyPreset(d, other.key)), body), other.key).toBe(false);
    }
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
          layers.every((l: RouteRule) => allows(l, r.id)),
          `${name} ${r.id}`,
        ).toBe(r.allowed);
      }
    }
  });

  it("names the routes and patterns the ceiling forbids", () => {
    const ceiling = ceilingOf("restricted").rule;
    expect(exceeds({ deny: ["cursor-login"] }, ceiling).routes).toEqual(["openrouter-api", "sakana-api"]);
    expect(exceeds({ destinations: ["anthropic"], endpoints: ["*.corp.example", "x.other.example"] }, ceiling)).toEqual(
      {
        routes: [],
        endpoints: ["x.other.example"],
      },
    );
    // An absent endpoint list leaves the ceiling in charge: nothing to refuse.
    expect(exceeds({ destinations: ["anthropic"] }, ceiling)).toEqual({ routes: [], endpoints: [] });
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
];
const RULES: RouteRule[] = [
  {},
  { deny: ["cursor-login"] },
  { classes: ["member-key"], except: ["openrouter-api"] },
  { classes: ["subscription", "member-key"] },
  { destinations: ["openrouter", "anthropic"] },
  { destinations: ["anthropic"], endpoints: ["*.corp.example", "*.other.example"] },
  // No key the Console does not know: the CP refuses one (the presets pin that it is carried).
  { endpoints: ["llm.corp.example"] },
];

describe("match the operator's setting", () => {
  const cases = CEILINGS.flatMap((c) =>
    RULES.filter((r) => {
      const x = exceeds(r, c.rule);
      return x.routes.length + x.endpoints.length > 0;
    }).map((rule) => ({
      ceilingRaw: c.raw,
      ceiling: c.rule,
      rule,
      matched: ruleFromDraft(rule, matchCeiling(draftOf(rule), c.rule)),
    })),
  );

  it("leaves nothing the ceiling refuses, and never allows a route the rule did not", () => {
    expect(cases.length).toBeGreaterThan(15);
    for (const c of cases) {
      expect(exceeds(c.matched, c.ceiling), JSON.stringify(c)).toEqual({ routes: [], endpoints: [] });
      for (const rt of [
        "anthropic-api",
        "anthropic-subscription",
        "openrouter-api",
        "bedrock",
        "vertex",
        "copilot-login",
      ]) {
        if (allows(c.matched, rt)) expect(allows(c.rule, rt), `${JSON.stringify(c)} ${rt}`).toBe(true);
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
