// The pre-save preview against the CP's own answers: scripts/shots/model-access/preview-cases.json
// is written by the real handlers (routepolicysrv TestPreviewCases): per case the tenant view as
// loaded, the edit, and what the CP said each owner could use once the edit was saved.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { previewTenantRule } from "./routePolicyPreview.ts";
import type { OwnersView, RouteRule, TenantPolicyView, Vocabulary } from "./routePolicyRule.ts";

const DIR = "scripts/shots/model-access";
const VOCAB: Vocabulary = JSON.parse(readFileSync(`${DIR}/policy-open.json`, "utf8"))["deployment"].vocabulary;

interface Case {
  edit: RouteRule;
  before: Omit<TenantPolicyView, "vocabulary">;
  after: { owners: OwnersView };
}
const cases: Record<string, Case> = JSON.parse(readFileSync(`${DIR}/preview-cases.json`, "utf8"));
const view = (c: Case): TenantPolicyView => ({ ...c.before, vocabulary: VOCAB }) as TenantPolicyView;
const GROUPS = ["kinds", "chat", "imagegen"] as const;

describe("previewTenantRule against the CP's table", () => {
  it("has cases", () => {
    expect(Object.keys(cases).length).toBeGreaterThan(10);
  });

  for (const [name, c] of Object.entries(cases)) {
    it(`${name}: never allows a route the CP refuses, and equals it when every host is known`, () => {
      const p = previewTenantRule(view(c), c.edit)!;
      expect(p).not.toBeNull();
      for (const g of GROUPS) {
        for (const it of c.after.owners[g]) {
          const got = p.allowed[g][it.id];
          expect(got, `${g}/${it.id}`).toBeDefined();
          // Never more permissive than the CP.
          for (const r of got) expect(it.allowed, `${g}/${it.id}: ${r}`).toContain(r);
          // The CP's host decisions are not known for a host it has not seen, so only then may the
          // preview say less than the CP.
          if (!p.unsavedHosts) expect(got, `${g}/${it.id}`).toEqual(it.allowed);
        }
      }
    });

    it(`${name}: hides exactly the owners the CP leaves with nothing (or more, with an unsaved host)`, () => {
      const p = previewTenantRule(view(c), c.edit)!;
      for (const g of GROUPS) {
        const before = new Map(c.before.owners ? (c.before.owners as OwnersView)[g].map((o) => [o.id, o]) : []);
        const cpHides = c.after.owners[g].filter((o) => o.allowed.length === 0 && before.get(o.id)!.allowed.length > 0);
        const ids = cpHides.map((o) => o.id);
        if (p.unsavedHosts) for (const id of ids) expect(p.hides[g]).toContain(id);
        else expect(p.hides[g]).toEqual(ids);
      }
    });
  }

  it("the table exercises a real hide, a host that changes the answer, and an unsaved host", () => {
    const ps = Object.entries(cases).map(([n, c]) => [n, previewTenantRule(view(c), c.edit)!] as const);
    expect(ps.some(([, p]) => GROUPS.some((g) => p.hides[g].length > 0))).toBe(true);
    expect(ps.some(([n, p]) => n === "restricted-keep-host-narrow-classes" && p.hides.kinds.includes("lcpp"))).toBe(true);
    expect(ps.some(([, p]) => p.unsavedHosts)).toBe(true);
  });

  it("counts no route for a host the CP has not judged (less than the CP, never more)", () => {
    const c = cases["restricted-add-host"];
    const p = previewTenantRule(view(c), c.edit)!;
    expect(p.unsavedHosts).toBe(true);
    // The CP, once the host is saved, allows lcpp a second route through it.
    expect(c.after.owners.kinds.find((o) => o.id === "lcpp")!.allowed).toHaveLength(2);
    expect(p.allowed.kinds["lcpp"]).toHaveLength(1);
  });

  it("answers null when the CP sent no owners or a malformed list", () => {
    const [c] = Object.values(cases);
    const v = view(c);
    expect(previewTenantRule({ ...v, owners: undefined }, c.edit)).toBeNull();
    expect(previewTenantRule({ ...v, owners: { kinds: "x" } }, c.edit)).toBeNull();
  });

  it("treats a route the CP did not list as not allowed", () => {
    const [c] = Object.values(cases);
    const v = view(c);
    const p = previewTenantRule({ ...v, routes: [] }, {})!;
    for (const g of GROUPS) for (const r of Object.values(p.allowed[g])) expect(r).toEqual([]);
  });
});
