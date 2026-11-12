import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { getSettings } from "../../lib/settings.ts";
import { DEFAULT_AGENT_LAUNCH } from "../../lib/settings/normalize.ts";
import { parseKindCatalog, setKindCatalog } from "../../agents/kindCatalog.ts";
import { parseBudgetText } from "../settings/tenant/tenantMemberDefaults.tsx";
import type { EffectiveDefaults, LaunchDefaults, MemberUIState } from "../../core/store/uiState.ts";
import { defaultKind, ownLaunchProblem, tenantBudget, tenantDriver, tenantModel, tenantSkipPerm, teamFirst } from "./tenantLaunchDefaults.ts";

const L = (p: Partial<LaunchDefaults> = {}): LaunchDefaults => ({ agent: "", model: "", driver: "", permission_mode: "", budget_usd: null, ...p });
const tenantState = (agent: string, launch: Partial<LaunchDefaults>): MemberUIState => ({ defaults: { agent, launch: L(launch) } });
const settings = () => ({ ...getSettings(), agentLaunchDefaults: structuredClone(DEFAULT_AGENT_LAUNCH), sessionSpendCapUsd: 0 });

describe("tenant launch defaults prefill", () => {
  it("starts on the merged agent only when it is launchable", () => {
    const eff = (agent: string): EffectiveDefaults => ({ launch: L({ agent }), repos: [], ops: { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" } });
    expect(defaultKind(eff("codex"), ["claude", "codex"])).toBe("codex");
    expect(defaultKind(eff("codex"), ["claude"])).toBeUndefined();
    expect(defaultKind(undefined, ["claude"])).toBeUndefined();
  });

  it("uses the tenant's model only for the tenant's own agent, never a member-chosen other agent (C3)", () => {
    const s = settings();
    const st: MemberUIState = { ...tenantState("claude", { model: "opus" }), launch: L({ agent: "codex", model: "" }) };
    // The CP's merged launch would read {agent: codex, model: opus}; the raw layers must not.
    expect(tenantModel(st, "codex", s)).toBe(""); // the member's explicit "CLI default"
    expect(tenantModel(st, "claude", s)).toBe("opus");
    expect(tenantModel(tenantState("claude", { model: "opus" }), "codex", s)).toBeUndefined();
  });

  it("a member's own saved block is explicit: skip over a tenant ask, budget 0, empty model (C4)", () => {
    const s = settings();
    const st: MemberUIState = { ...tenantState("codex", { permission_mode: "ask", budget_usd: 5, model: "gpt-x" }), launch: L({ agent: "codex", permission_mode: "skip", budget_usd: 0 }) };
    expect(tenantSkipPerm(st, "codex", s)).toBe(true);
    expect(tenantBudget(st, "codex", s)).toBe("0");
    expect(tenantModel(st, "codex", s)).toBe("");
    // Another kind has no own block, so the tenant's non-model fields apply.
    expect(tenantSkipPerm(st, "claude", s)).toBe(false);
    expect(tenantBudget(st, "claude", s)).toBe("5");
  });

  it("yields to Settings values that differ from the built-in ones", () => {
    const s = settings();
    const st = tenantState("codex", { permission_mode: "ask", model: "gpt-x" });
    expect(tenantSkipPerm(st, "codex", s)).toBe(false);
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

  it("says why a dialog value cannot be saved as the member's default (C6)", () => {
    const ok = ["claude", "codex"];
    expect(ownLaunchProblem("codex", ok, "gpt-x", 5)).toBe("");
    expect(ownLaunchProblem("agy", ok, "", null)).toBe("agent");
    expect(ownLaunchProblem("codex", ok, "has space", null)).toBe("model");
    expect(ownLaunchProblem("codex", ok, "provider/m#high", null)).toBe("");
    expect(ownLaunchProblem("codex", ok, "", 12000)).toBe("budget");
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
