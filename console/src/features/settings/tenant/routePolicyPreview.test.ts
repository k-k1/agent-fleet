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

  const predict = (n: string) => previewTenantRule(view(cases[n]), cases[n].edit)!;

  it("the table exercises the host and the upper-layer paths that decide a hide", () => {
    expect(Object.values(cases).some((c) => GROUPS.some((g) => previewTenantRule(view(c), c.edit)!.hides[g].length > 0)))
      .toBe(true);
    // Dropping the one approved host is what hides lcpp (af-engine-llm is denied).
    expect(predict("host-only-lcpp-drop-host").hides.kinds).toEqual(["lcpp"]);
    // A host the CP has not judged: the Console cannot tell, and says a rescue is possible.
    const swap = predict("host-only-lcpp-swap-host");
    expect(swap.hides.kinds).toEqual(["lcpp"]);
    expect(swap.unsavedHosts && swap.unsavedMayRescue).toBe(true);
    // A layer above that leaves self-hosted out (or denies the route) stops the approved host's
    // routes: lcpp stays hidden, and the preview must not count the host for the draft alone.
    for (const n of ["deployment-drops-self-hosted-keep-host", "cap-denies-llamacpp-keep-host"]) {
      const p = predict(n);
      expect(p.allowed.kinds["lcpp"], n).toEqual(cases[n].after.owners.kinds.find((o) => o.id === "lcpp")!.allowed);
      expect(p.allowed.kinds["lcpp"], n).toEqual([]);
    }
  });

  it("raises no unsaved-host flag when the host cannot change the answer", () => {
    // classes without self-hosted: no per-connection route is allowed by the draft at all.
    expect(predict("open-unapproved-host").unsavedHosts).toBe(false);
    // A host is already approved: a further one changes nothing.
    expect(predict("restricted-keep-host-narrow-classes").unsavedMayRescue).toBe(false);
    // The hidden owners have no per-connection route a host could make usable.
    expect(predict("restricted-add-host").unsavedMayRescue).toBe(false);
  });

  it("raises no rescue caveat for an owner that was already hidden", () => {
    const c = cases["host-only-lcpp-drop-host"];
    const v = view(c);
    // lcpp is already hidden before this edit; the draft hides others and adds an unjudged host.
    const owners = v.owners as OwnersView;
    const hiddenAlready = {
      ...owners,
      kinds: owners.kinds.map((o) => (o.id === "lcpp" ? { ...o, allowed: [] } : o)),
      chat: owners.chat.map((o) => (o.id === "lcpp" ? { ...o, allowed: [] } : o)),
    };
    const draft = { classes: ["self-hosted"], deny: ["af-engine-llm", "custom-api"], endpoints: ["x.corp.example"] };
    const p = previewTenantRule({ ...v, owners: hiddenAlready }, draft)!;
    expect(p.unsavedHosts).toBe(true);
    expect(p.hides.kinds.length).toBeGreaterThan(0);
    expect(p.hides.kinds).not.toContain("lcpp");
    expect(p.unsavedMayRescue).toBe(false);
  });

  it("does not count a host the CP lists as not approved, and raises no unsaved flag for it", () => {
    const c = cases["host-only-lcpp-swap-host"];
    const v = view(c);
    const draft = { ...c.edit, endpoints: ["llm.corp.example"] };
    const p = previewTenantRule(
      { ...v, endpoints: [{ host: "llm.corp.example", approved: false, excludedBy: ["ceiling"] }] },
      draft,
    )!;
    expect(p.unsavedHosts).toBe(false);
    expect(p.allowed.kinds["lcpp"]).toEqual([]);
  });

  it("keeps a route not allowed for no named layer forbidden (fail closed)", () => {
    const c = cases["open-except-only"];
    const v = view(c);
    const routes = v.routes.map((r) => (r.id === "anthropic-api" ? { ...r, allowed: false, forbiddenBy: undefined } : r));
    const p = previewTenantRule({ ...v, routes }, {})!;
    expect(p.allowed.kinds["claude"]).not.toContain("anthropic-api");
    // The same route with its layers named only by the tenant layer is the draft's to decide.
    const only = v.routes.map((r) => (r.id === "anthropic-api" ? { ...r, allowed: false, forbiddenBy: ["tenant"] } : r));
    expect(previewTenantRule({ ...v, routes: only }, {})!.allowed.kinds["claude"]).toContain("anthropic-api");
  });

  it("counts no route for a host the CP has not judged (less than the CP, never more)", () => {
    const c = cases["host-only-lcpp-swap-host"];
    const p = previewTenantRule(view(c), c.edit)!;
    expect(p.unsavedHosts).toBe(true);
    // The CP, once the host is saved, still lets lcpp through it; the preview cannot know yet.
    expect(c.after.owners.kinds.find((o) => o.id === "lcpp")!.allowed).toEqual(["member-llamacpp"]);
    expect(p.allowed.kinds["lcpp"]).toEqual([]);
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
