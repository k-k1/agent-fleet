// The tenant Overview's data (#4380 P-4), checked against the CP's own answers: the fixtures under
// scripts/shots/model-access are written by the real route-policy handlers and compared against
// them on every go test (routepolicysrv TestShotFixturesPolicy), so these expectations follow the
// wire, not a hand-made shape.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { layerRules, memberUse, summarizeLayer, type MemberUse, type OwnerRow } from "./routePolicySummary.ts";
import type { TenantPolicyView } from "./routePolicyRule.ts";

const fixture = (name: string): TenantPolicyView =>
  JSON.parse(readFileSync(`scripts/shots/model-access/policy-${name}.json`, "utf8"))["tenant/demo"];

// One layer in a line: "none", or its narrowed parts.
function line(rule: Parameters<typeof summarizeLayer>[0], ceiling: boolean): string {
  const s = summarizeLayer(rule, ceiling);
  if (s.none) return "none";
  const parts: string[] = [];
  if (s.classes) parts.push(`classes ${s.classes.allowed.length}/${s.classes.total} -${s.classes.missing.join(",")}`);
  if (s.destinations)
    parts.push(`dest ${s.destinations.allowed.length}/${s.destinations.total} -${s.destinations.missing.join(",")}`);
  if (s.except.length) parts.push(`except ${s.except.join(",")}`);
  if (s.deny.length) parts.push(`deny ${s.deny.join(",")}`);
  if (s.endpoints) parts.push(`endpoints ${s.endpoints.join(",")}`);
  if (s.noEndpoints) parts.push("no endpoints");
  return parts.join("; ");
}

// One group in a line: id:usable:allowed/total:stopped-by.
const rows = (l: OwnerRow[]) =>
  l
    .map(
      (o) =>
        `${o.id}:${o.usable ? "Y" : "N"}:${o.routes.filter((r) => r.allowed).length}/${o.routes.length}:${o.stoppedBy.join("+")}`,
    )
    .join(" ");

const NOT_BEDROCK =
  "anthropic,openai,google,meta,github,cursor,kiro,opencode.ai,openrouter,sakana,af-engine,self-hosted";

describe("summarizeLayer, on the CP's answers", () => {
  it.each([
    ["open", { ceiling: "none", deployment: "none", tenant_cap: "none", tenant: "none" }],
    [
      "restricted",
      {
        ceiling: "dest 11/13 -openrouter,sakana; endpoints *.corp.example",
        deployment: "none",
        tenant_cap: "dest 11/13 -openrouter,sakana; deny cursor-login",
        tenant: "classes 4/5 -subscription; except copilot-login; endpoints llm.corp.example",
      },
    ],
    [
      "bedrockonly",
      { ceiling: `dest 1/13 -${NOT_BEDROCK}; no endpoints`, deployment: "none", tenant_cap: "none", tenant: "none" },
    ],
    [
      "nosubs",
      {
        ceiling: "none",
        deployment: "classes 4/5 -subscription",
        tenant_cap: "none",
        tenant: "deny openrouter-api; endpoints gpu.partner.example,llm.corp.example",
      },
    ],
  ])("%s", (name, want) => {
    const got = Object.fromEntries(
      Object.entries(layerRules(fixture(name))).map(([k, r]) => [k, line(r, k === "ceiling")]),
    );
    expect(got).toEqual(want);
  });

  it("counts a value the Console does not know, and an empty list as nothing allowed", () => {
    expect(summarizeLayer({ destinations: ["anthropic", "mars"] }).destinations).toEqual({
      allowed: ["anthropic", "mars"],
      total: 14,
      missing: expect.arrayContaining(["openai", "self-hosted"]),
    });
    const none = summarizeLayer({ classes: [] });
    expect(none.none).toBe(false);
    expect(none.classes).toEqual({
      allowed: [],
      total: 5,
      missing: ["subscription", "member-key", "member-cloud", "provided", "self-hosted"],
    });
    // An exception alone restricts nothing, but it is still named.
    expect(summarizeLayer({ except: ["copilot-login"] })).toMatchObject({ none: false, except: ["copilot-login"] });
  });
});

describe("memberUse, on the CP's answers", () => {
  it.each([
    [
      "open",
      "agy:Y:1/1: claude:Y:4/4: codex:Y:2/2: copilot:Y:1/1: cursor:Y:1/1: kiro:Y:1/1: lcpp:Y:2/2: muse:Y:2/2: opencode:Y:11/11:",
      "agy:Y:1/1: claude:Y:1/1: codex:Y:2/2: cursor:Y:1/1: lcpp:Y:2/2: muse:Y:2/2: opencode:Y:10/10:",
    ],
    [
      "restricted",
      "agy:N:0/1:tenant claude:Y:3/4:tenant codex:Y:1/2:tenant copilot:Y:1/1: cursor:N:0/1:tenant_cap+tenant kiro:N:0/1:tenant lcpp:Y:2/2: muse:Y:1/2:tenant opencode:Y:7/11:ceiling+tenant_cap+tenant",
      "agy:N:0/1:tenant claude:N:0/1:tenant codex:Y:1/2:tenant cursor:N:0/1:tenant_cap+tenant lcpp:Y:2/2: muse:Y:1/2:tenant opencode:Y:6/10:ceiling+tenant_cap+tenant",
    ],
    [
      "bedrockonly",
      "agy:N:0/1:ceiling claude:Y:2/4:ceiling codex:N:0/2:ceiling copilot:N:0/1:ceiling cursor:N:0/1:ceiling kiro:N:0/1:ceiling lcpp:N:0/2:ceiling muse:N:0/2:ceiling opencode:Y:1/11:ceiling",
      "agy:N:0/1:ceiling claude:N:0/1:ceiling codex:N:0/2:ceiling cursor:N:0/1:ceiling lcpp:N:0/2:ceiling muse:N:0/2:ceiling opencode:N:0/10:ceiling",
    ],
    [
      "nosubs",
      "agy:N:0/1:deployment claude:Y:3/4:deployment codex:Y:1/2:deployment copilot:N:0/1:deployment cursor:N:0/1:deployment kiro:N:0/1:deployment lcpp:Y:2/2: muse:Y:1/2:deployment opencode:Y:8/11:deployment+tenant",
      "agy:N:0/1:deployment claude:N:0/1:deployment codex:Y:1/2:deployment cursor:N:0/1:deployment lcpp:Y:2/2: muse:Y:1/2:deployment opencode:Y:7/10:deployment+tenant",
    ],
  ])("%s", (name, kinds, chat) => {
    const u = memberUse(fixture(name));
    expect(u.state).toBe("owners");
    const g = (u as Extract<MemberUse, { state: "owners" }>).groups;
    expect(rows(g.kinds)).toBe(kinds);
    expect(rows(g.chat)).toBe(chat);
  });

  it("agrees with hidden: an item is unusable exactly when the CP lists it as hidden", () => {
    for (const name of ["open", "restricted", "bedrockonly", "nosubs"]) {
      const v = fixture(name);
      const u = memberUse(v) as Extract<MemberUse, { state: "owners" }>;
      const hidden = v.hidden as Record<"kinds" | "chat" | "imagegen", { id: string }[]>;
      for (const k of ["kinds", "chat", "imagegen"] as const) {
        expect(
          u.groups[k].filter((o) => !o.usable).map((o) => o.id),
          `${name} ${k}`,
        ).toEqual(hidden[k].map((h) => h.id));
      }
    }
  });

  it("falls back to the hidden items on a CP without owners", () => {
    const { owners: _, ...v } = fixture("restricted");
    const u = memberUse(v as TenantPolicyView);
    expect(u.state).toBe("hiddenOnly");
    expect(rows((u as Extract<MemberUse, { state: "hiddenOnly" }>).groups.kinds)).toBe(
      "agy:N:0/1:tenant cursor:N:0/1:tenant_cap+tenant kiro:N:0/1:tenant",
    );
  });

  it("fails closed on malformed owners, and on no owners without a well-formed hidden", () => {
    const v = fixture("open");
    const owners = v.owners as { kinds: unknown[]; chat: unknown[]; imagegen: unknown[] };
    for (const bad of [
      null,
      [],
      { ...owners, chat: undefined },
      { ...owners, kinds: [{ id: "claude", routes: [], allowed: [] }] },
      { ...owners, kinds: [{ id: "claude", routes: ["anthropic-api"], allowed: ["bedrock"] }] },
      { ...owners, kinds: [{ id: 1, routes: ["anthropic-api"], allowed: [] }] },
    ]) {
      expect(memberUse({ ...v, owners: bad }).state, JSON.stringify(bad)).toBe("unavailable");
    }
    const { owners: _, ...noOwners } = v;
    for (const hidden of [
      undefined,
      {},
      { kinds: [], chat: [] },
      { kinds: [{ id: "x", routes: [], forbiddenBy: [] }], chat: [], imagegen: [] },
    ]) {
      expect(memberUse({ ...(noOwners as TenantPolicyView), hidden }).state, JSON.stringify(hidden)).toBe(
        "unavailable",
      );
    }
  });
});
