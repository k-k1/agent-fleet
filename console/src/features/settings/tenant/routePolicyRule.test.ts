// The Model access pages' rule helpers (ADR 0138 M2 P8). Pinned here:
//   1. The vocabulary equals the route catalogue the CP embeds, so an editor never offers a value
//      the CP refuses or misses one it accepts.
//   2. A save body is the loaded rule with only the edited fields replaced: a field the page does
//      not edit survives, and "every class" (absent) never becomes "no class" ([]) or the reverse.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  draftOf,
  isOlderPolicyAnswer,
  parseDeploymentPolicy,
  parseTenantPolicy,
  parseVocabulary,
  routeIds,
  ruleEqual,
  ruleFromDraft,
} from "./routePolicyRule.ts";

// The vocabulary is served by the CP (routepolicysrv), not kept here. These answers are the real
// handlers' own, written by routepolicysrv TestShotFixturesPolicy.
const answers = (name: string) => JSON.parse(readFileSync(`scripts/shots/model-access/policy-${name}.json`, "utf8"));

describe("the served vocabulary", () => {
  it("parses from both GETs, and the two agree", () => {
    const a = answers("open");
    const dep = parseDeploymentPolicy(a["deployment"]);
    const ten = parseTenantPolicy(a["tenant/demo"]);
    expect(dep?.vocabulary.routes.length).toBeGreaterThan(0);
    expect(dep?.vocabulary.classes).toContain("subscription");
    expect(dep?.vocabulary.destinations).not.toContain("unclassified");
    expect(ten?.vocabulary).toEqual(dep?.vocabulary);
  });

  it("refuses an answer whose vocabulary is malformed, so no picker is drawn from part of a list", () => {
    const a = answers("open");
    for (const bad of [
      null,
      {},
      { ...a["deployment"].vocabulary, routes: "x" },
      { ...a["deployment"].vocabulary, classes: [1] },
    ]) {
      expect(parseVocabulary(bad)).toBeNull();
      expect(parseDeploymentPolicy({ ...a["deployment"], vocabulary: bad })).toBeNull();
      expect(parseTenantPolicy({ ...a["tenant/demo"], vocabulary: bad })).toBeNull();
    }
  });

  it("tells a Control Plane that sends none (older) from a malformed answer", () => {
    const a = answers("open");
    const { vocabulary: _d, ...oldDep } = a["deployment"];
    const { vocabulary: _t, ...oldTen } = a["tenant/demo"];
    expect(parseDeploymentPolicy(oldDep)).toBeNull();
    expect(parseTenantPolicy(oldTen)).toBeNull();
    expect(isOlderPolicyAnswer(oldDep)).toBe(true);
    expect(isOlderPolicyAnswer(oldTen)).toBe(true);
    expect(isOlderPolicyAnswer(a["deployment"])).toBe(false);
    expect(isOlderPolicyAnswer({ ...a["deployment"], vocabulary: {} })).toBe(false);
    expect(isOlderPolicyAnswer({ error: "x" })).toBe(false);
  });

  it("keeps a route, class or destination it has no label for, and offers it", () => {
    const v = parseVocabulary({
      classes: ["subscription", "new-class"],
      destinations: ["anthropic", "mars"],
      routes: [{ id: "new-route", class: "new-class", destination: "mars", perConnection: true }],
    });
    expect(v).not.toBeNull();
    expect(routeIds(v!, ["held-route"])).toEqual(["held-route", "new-route"]);
  });
});

describe("ruleFromDraft", () => {
  it("sends the loaded rule back unchanged when nothing was edited", () => {
    const loaded = { classes: ["member-key"], deny: ["openai-api"], endpoints: ["*.corp.example"], future: { x: 1 } };
    expect(ruleFromDraft(loaded, draftOf(loaded))).toEqual(loaded);
  });

  it("keeps absent apart from empty", () => {
    const all = ruleFromDraft({}, draftOf({}));
    expect("classes" in all).toBe(false);
    expect("endpoints" in all).toBe(false);
    const none = ruleFromDraft({}, { ...draftOf({}), classesAll: false, endpointsAll: false });
    expect(none.classes).toEqual([]);
    expect(none.endpoints).toEqual([]);
  });

  it("replaces only the edited field and drops an emptied exception list", () => {
    const loaded = { destinations: ["aws-bedrock"], except: ["copilot-login"], endpoints: ["llm.corp.example"] };
    const d = { ...draftOf(loaded), except: [] as string[], classesAll: false, classes: ["provided"] };
    expect(ruleFromDraft(loaded, d)).toEqual({
      classes: ["provided"],
      destinations: ["aws-bedrock"],
      endpoints: ["llm.corp.example"],
    });
  });

  it("splits endpoint text on lines, spaces and commas", () => {
    const d = {
      ...draftOf({}),
      endpointsAll: false,
      endpoints: " a.corp.example\nb.corp.example, c.corp.example \n\n",
    };
    expect(ruleFromDraft({}, d).endpoints).toEqual(["a.corp.example", "b.corp.example", "c.corp.example"]);
  });
});

describe("ruleEqual", () => {
  it("ignores order, not absence", () => {
    expect(ruleEqual({ classes: ["a", "b"] }, { classes: ["b", "a"] })).toBe(true);
    expect(ruleEqual({}, { classes: [] })).toBe(false);
    expect(ruleEqual({ except: [] }, {})).toBe(true);
  });
});

describe("parseTenantPolicy", () => {
  it("refuses a partial answer", () => {
    expect(parseTenantPolicy({ ceiling: { raw: "", rule: {}, source: "deployment" } })).toBeNull();
    expect(parseTenantPolicy({ error: { code: "forbidden" } })).toBeNull();
  });
});

describe("parseHidden", () => {
  it("accepts only all three well-formed lists", async () => {
    const { parseHidden } = await import("./routePolicyRule.ts");
    expect(parseHidden({ kinds: [], chat: [], imagegen: [] })).not.toBeNull();
    expect(parseHidden(undefined)).toBeNull();
    expect(parseHidden({ kinds: [], chat: [] })).toBeNull();
    expect(parseHidden({ kinds: [{ id: "agy", routes: ["agy-login"] }], chat: [], imagegen: [] })).toBeNull();
    expect(
      parseHidden({ kinds: [{ id: "agy", routes: ["agy-login"], forbiddenBy: [] }], chat: [], imagegen: [] }),
    ).toBeNull();
    expect(
      parseHidden({ kinds: [{ id: "agy", routes: [], forbiddenBy: ["ceiling"] }], chat: [], imagegen: [] }),
    ).toBeNull();
  });
});
