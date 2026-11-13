import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { getSettings } from "../../lib/settings.ts";
import { DEFAULT_AGENT_LAUNCH } from "../../lib/settings/normalize.ts";
import { parseKindCatalog, setKindCatalog } from "../../agents/kindCatalog.ts";
import { parseBudgetText } from "../settings/tenant/tenantMemberDefaults.tsx";
import type { EffectiveDefaults, LaunchDefaults, MemberUIState } from "../../core/store/uiState.ts";
import { defaultKind, ownLaunchProblem, tenantBudget, tenantDriver, tenantModel, tenantSkipPerm, teamFirst, teamCloneHost, teamCloneTargets } from "./tenantLaunchDefaults.ts";

const L = (p: Partial<LaunchDefaults> = {}): LaunchDefaults => ({ agent: "", model: "", driver: "", permission_mode: "", budget_usd: null, ...p });
// The state the CP would answer: both layers and their field-by-field merge ("" / null = not set).
const ops = { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" };
const build = (agent: string, tenant: Partial<LaunchDefaults>, member?: Partial<LaunchDefaults>): MemberUIState => {
  const t = L(tenant);
  const m = member ? L(member) : undefined;
  const launch = L({
    agent: m?.agent || agent,
    model: m?.model || t.model,
    driver: m?.driver || t.driver,
    permission_mode: m?.permission_mode || t.permission_mode,
    budget_usd: m?.budget_usd ?? t.budget_usd,
  });
  return { defaults: { agent, launch: t }, ...(m ? { launch: m } : {}), effective: { launch, repos: [], ops } };
};
const tenantState = (agent: string, launch: Partial<LaunchDefaults>) => build(agent, launch);
const settings = () => ({ ...getSettings(), agentLaunchDefaults: structuredClone(DEFAULT_AGENT_LAUNCH), sessionSpendCapUsd: 0 });

describe("tenant launch defaults prefill", () => {
  it("starts on the merged agent only when it is launchable", () => {
    const eff = (agent: string): EffectiveDefaults => ({ launch: L({ agent }), repos: [], ops: { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" } });
    expect(defaultKind(eff("codex"), ["claude", "codex"])).toBe("codex");
    expect(defaultKind(eff("codex"), ["claude"])).toBeUndefined();
    expect(defaultKind(undefined, ["claude"])).toBeUndefined();
  });

  it("uses a model only for the agent of the layer that set it, never a merged-in other agent (C3)", () => {
    const s = settings();
    // The CP's merged launch reads {agent: codex, model: opus}; the layers say opus belongs to claude.
    const st = build("claude", { model: "opus" }, { agent: "codex" });
    expect(st.effective!.launch).toMatchObject({ agent: "codex", model: "opus" });
    expect(tenantModel(st, "codex", s)).toBeUndefined();
    expect(tenantModel(st, "claude", s)).toBe("opus");
    // The member's own model is for the member's agent.
    const own = build("claude", { model: "opus" }, { agent: "codex", model: "gpt-y" });
    expect(tenantModel(own, "codex", s)).toBe("gpt-y");
    expect(tenantModel(own, "claude", s)).toBeUndefined();
  });

  it("keeps the CP's inheritance: a partial own block for the same agent inherits the tenant's values (C7)", () => {
    const s = settings();
    const st = build("codex", { model: "gpt-x", permission_mode: "ask", budget_usd: 5 }, { agent: "codex" });
    expect(tenantModel(st, "codex", s)).toBe("gpt-x");
    expect(tenantSkipPerm(st, "codex", s)).toBe(false);
    expect(tenantBudget(st, "codex", s)).toBe("5");
  });

  it("a member's own permission and budget win over the tenant's (C4)", () => {
    const s = settings();
    const st = build("codex", { permission_mode: "ask", budget_usd: 5 }, { agent: "codex", permission_mode: "skip", budget_usd: 0 });
    expect(tenantSkipPerm(st, "codex", s)).toBe(true);
    expect(tenantBudget(st, "codex", s)).toBe("0");
    // The values are not tied to one agent: the CP's merge is what the dialog follows.
    expect(tenantBudget(st, "claude", s)).toBe("0");
  });

  it("yields to Settings values that differ from the built-in ones", () => {
    const s = settings();
    const st = tenantState("codex", { permission_mode: "ask", model: "gpt-x" });
    expect(tenantSkipPerm(st, "codex", s)).toBe(false);
    // ...but the member's own block still wins over their Settings.
    expect(tenantSkipPerm(build("codex", { permission_mode: "ask" }, { agent: "codex", permission_mode: "skip" }), "codex", settings())).toBe(true);
    s.agentLaunchDefaults.codex = { ...s.agentLaunchDefaults.codex, skipPermissions: false, model: "mine" };
    expect(tenantSkipPerm(tenantState("codex", { permission_mode: "skip" }), "codex", s)).toBeUndefined();
    expect(tenantModel(st, "codex", s)).toBeUndefined();
    expect(tenantBudget(tenantState("codex", { budget_usd: 5 }), "codex", { ...settings(), sessionSpendCapUsd: 2 })).toBe("");
  });

  describe("driver", () => {
    afterEach(() => act(() => setKindCatalog(null)));
    it("never holds Terminal for a managed-only kind, and needs the catalog for a choice (C5)", async () => {
      await act(async () =>
        setKindCatalog(
          parseKindCatalog({
            kinds: [
              { kind: "claude", managedOnly: false, managedDefault: true, mcpClient: "yes" },
              { kind: "muse", managedOnly: true, managedDefault: true, mcpClient: "yes" },
            ],
          }),
        ),
      );
      const st = tenantState("muse", { driver: "terminal" });
      expect(tenantDriver(st, "muse")).toBeNull();
      expect(tenantDriver(tenantState("claude", { driver: "terminal" }), "claude")).toBe("");
      expect(tenantDriver(tenantState("claude", { driver: "managed" }), "claude")).toBe("managed");
    });
  });

  it("lists team repositories first, each group keeping its order", () => {
    const a = { name: "a", remotePath: "Acme/Api" };
    const b = { name: "b", remotePath: "other/x" };
    const c = { name: "c", remotePath: "acme/web" };
    const d = { name: "d" } as { name: string; remotePath?: string };
    const { rows, team } = teamFirst([b, a, d, c], ["acme/web", "acme/api"]);
    expect(rows.map((r) => r.name)).toEqual(["a", "c", "b", "d"]);
    expect(team.has(a) && team.has(c) && !team.has(b)).toBe(true);
  });

  it("takes the clone host only from exactly one connected Git provider", () => {
    expect(teamCloneHost({ github: { connected: true } })).toBe("github.com");
    expect(teamCloneHost({ bitbucket: { connected: true }, github: { connected: false } })).toBe("bitbucket.org");
    expect(teamCloneHost({ github: { connected: true }, bitbucket: { connected: true } })).toBe("");
    expect(teamCloneHost({ internal: { connected: true } })).toBe("");
    expect(teamCloneHost(null)).toBe("");
  });

  it("offers only uncloned team repositories, matched on the remote path like teamFirst", () => {
    const gh = { github: { connected: true } };
    const bases = [{ name: "api-copy", remotePath: "Acme/Api" }, { name: "web", remotePath: "other/web" }];
    const out = teamCloneTargets(bases, ["acme/api", "acme/docs", "ACME/docs", "bad entry", "acme/web"], gh);
    expect(out).toEqual([
      { path: "acme/docs", host: "github.com", cloneUrl: "https://github.com/acme/docs.git" },
      // "web" is taken by another repository: the clone would collide, so no shortcut
    ]);
    expect(teamCloneTargets(bases, ["acme/docs"], { github: { connected: true }, bitbucket: { connected: true } })).toEqual([]);
    expect(teamCloneTargets(bases, undefined, gh)).toEqual([]);
  });

  it("says why a dialog value cannot be saved as the member's default (C6)", () => {
    const ok = ["claude", "codex"];
    expect(ownLaunchProblem("codex", ok, "gpt-x", 5)).toBe("");
    expect(ownLaunchProblem("agy", ok, "", null)).toBe("agent");
    expect(ownLaunchProblem("codex", ok, "has space", null)).toBe("model");
    expect(ownLaunchProblem("codex", ok, "provider/m#high", null)).toBe("");
    expect(ownLaunchProblem("codex", ok, "", 12000)).toBe("budget");
    expect(ownLaunchProblem("codex", ok, "", Number.NaN)).toBe("budget");
  });
});

describe("admin budget text (C1)", () => {
  it("keeps what is typed and accepts only a bounded, two-decimal number", () => {
    expect(parseBudgetText("")).toBeNull();
    expect(parseBudgetText("1.")).toBe(1);
    expect(parseBudgetText("1.25")).toBe(1.25);
    expect(parseBudgetText("0")).toBe(0);
    expect(parseBudgetText("1.256")).toBeUndefined();
    expect(parseBudgetText("-1")).toBeUndefined();
    expect(parseBudgetText("1e3")).toBeUndefined();
    expect(parseBudgetText("10001")).toBeUndefined();
    expect(parseBudgetText("abc")).toBeUndefined();
  });
});
