import { describe, expect, it } from "vitest";
import { defaultsBody } from "../settings/tenant/tenantMemberDefaults.tsx";
import { getSettings } from "../../lib/settings.ts";
import { DEFAULT_AGENT_LAUNCH } from "../../lib/settings/normalize.ts";
import type { EffectiveDefaults } from "../../core/store/uiState.ts";
import { defaultKind, tenantBudget, tenantDriver, tenantModel, tenantSkipPerm, teamFirst } from "./tenantLaunchDefaults.ts";

const eff = (l: Partial<EffectiveDefaults["launch"]>, repos: string[] = []): EffectiveDefaults => ({
  launch: { agent: "codex", model: "gpt-x", driver: "terminal", permission_mode: "ask", budget_usd: 5, ...l },
  repos,
  ops: { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" },
});
const settings = () => ({ ...getSettings(), agentLaunchDefaults: structuredClone(DEFAULT_AGENT_LAUNCH), sessionSpendCapUsd: 0 });

describe("tenant launch defaults prefill", () => {
  it("starts on the tenant's agent only when it is launchable", () => {
    expect(defaultKind(eff({}), ["claude", "codex"])).toBe("codex");
    expect(defaultKind(eff({}), ["claude"])).toBeUndefined();
    expect(defaultKind(undefined, ["claude"])).toBeUndefined();
  });
  it("uses the tenant's model only for its own kind and below the member's own choice", () => {
    const s = settings();
    expect(tenantModel(eff({}), "codex", s)).toBe("gpt-x");
    expect(tenantModel(eff({}), "claude", s)).toBe("");
    s.agentLaunchDefaults.codex = { ...s.agentLaunchDefaults.codex, model: "mine" };
    expect(tenantModel(eff({}), "codex", s)).toBe("");
  });
  it("maps driver, permission and budget, and yields to the member's own values", () => {
    const s = settings();
    expect(tenantDriver(eff({}), "codex", true)).toBe("");
    expect(tenantDriver(eff({ driver: "managed" }), "codex", true)).toBe("managed");
    expect(tenantDriver(eff({}), "codex", false)).toBeNull();
    expect(tenantSkipPerm(eff({ permission_mode: "ask" }), "codex", s)).toBe(false);
    expect(tenantSkipPerm(eff({ permission_mode: "" }), "codex", s)).toBeUndefined();
    s.agentLaunchDefaults.codex = { ...s.agentLaunchDefaults.codex, skipPermissions: false };
    expect(tenantSkipPerm(eff({ permission_mode: "skip" }), "codex", s)).toBeUndefined();
    expect(tenantBudget(eff({}), settings())).toBe("5");
    expect(tenantBudget(eff({}), { ...settings(), sessionSpendCapUsd: 2 })).toBe("");
    expect(tenantBudget(eff({ budget_usd: null }), settings())).toBe("");
  });
  it("lists team repositories first, each group keeping its order", () => {
    const a = { name: "a", remotePath: "Acme/Api" };
    const b = { name: "b", remotePath: "other/x" };
    const c = { name: "c", remotePath: "acme/web" };
    const d = { name: "d" };
    const { rows, team } = teamFirst([b, a, d, c], ["acme/web", "acme/api"]);
    expect(rows.map((r) => r.name)).toEqual(["a", "c", "b", "d"]);
    expect(team.has(a) && team.has(c) && !team.has(b)).toBe(true);
  });
});

describe("admin PUT body", () => {
  it("always carries every group so one can be cleared, and never a launch agent", () => {
    const body = defaultsBody(
      "claude",
      { agent: "x", model: "m", driver: "", permission_mode: "", budget_usd: 0 },
      " acme/api \n\nacme/web\n",
      { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" },
    );
    expect(Object.keys(body)).toEqual(["agent", "launch", "repos", "ops"]);
    expect(body.launch.agent).toBe("");
    expect(body.launch.budget_usd).toBe(0);
    expect(body.repos).toEqual(["acme/api", "acme/web"]);
  });
});
