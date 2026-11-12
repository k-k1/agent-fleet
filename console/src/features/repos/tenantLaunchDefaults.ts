// Applies the CP's effective launch defaults (ADR 0114 P3) to the launch dialog and Start. Pure
// helpers: precedence was decided by the CP, and this only chooses where a value may be used.
// Everything here PREFILLS. Nothing writes the Agent's ui-prefs, which keeps enforcing its own.
import type { EffectiveDefaults, LaunchDefaults } from "../../core/store/uiState.ts";
import { agentLaunchDefault } from "../../lib/settings.ts";
import { DEFAULT_AGENT_LAUNCH } from "../../lib/settings/normalize.ts";
import type { Settings } from "../../lib/settings.ts";

/** The tenant's agent when it is among the launchable kinds, else undefined. */
export function defaultKind(eff: EffectiveDefaults | undefined, kinds: string[]): string | undefined {
  const a = eff?.launch.agent;
  return a && kinds.includes(a) ? a : undefined;
}

// Settings › Agents always holds a value per kind (the built-in one until the member changes it),
// so "the member chose this" means "differs from the built-in": their choice outranks the tenant's
// and is what the Agent enforces.
const modelChosen = (s: Settings, kind: string) => agentLaunchDefault(s, kind).model !== DEFAULT_AGENT_LAUNCH[kind]?.model;
const permChosen = (s: Settings, kind: string) =>
  agentLaunchDefault(s, kind).skipPermissions !== (DEFAULT_AGENT_LAUNCH[kind]?.skipPermissions ?? true);

/** The model to start from: only for the kind the tenant's default names, since a model id belongs
 *  to one kind, and never over the member's own per-kind setting. */
export function tenantModel(eff: EffectiveDefaults | undefined, kind: string, s: Settings): string {
  const l = eff?.launch;
  return l && l.agent === kind && !modelChosen(s, kind) ? l.model : "";
}

/** "managed" / "" (Terminal) for a kind that has both, or null = follow the kind's own default. */
export function tenantDriver(eff: EffectiveDefaults | undefined, kind: string, hasManaged: boolean): string | null {
  const d = eff?.launch.driver;
  if (!hasManaged || !d) return null;
  return d === "managed" ? "managed" : "";
}

/** undefined = leave it to the Agent's default; else the prompt-skipping choice to start from. */
export function tenantSkipPerm(eff: EffectiveDefaults | undefined, kind: string, s: Settings): boolean | undefined {
  const m = eff?.launch.permission_mode;
  if (!m || permChosen(s, kind)) return undefined;
  return m === "skip";
}

/** The budget text to start from; "" when none applies or the member has a budget of their own. */
export function tenantBudget(eff: EffectiveDefaults | undefined, s: Settings): string {
  const b = eff?.launch.budget_usd;
  return b == null || s.sessionSpendCapUsd > 0 ? "" : String(b);
}

/** The launch block a member saves as their own default. */
export function ownLaunch(p: Partial<LaunchDefaults>): LaunchDefaults {
  return { agent: "", model: "", driver: "", permission_mode: "", budget_usd: null, ...p };
}

/** Team repositories first, each group keeping its order. Matches on the remote path
 *  ("owner/name", case-insensitive), which every working copy of one repository shares. */
export function teamFirst<T extends { remotePath?: string }>(bases: T[], teamRepos: string[] | undefined): { rows: T[]; team: Set<T> } {
  const want = new Set((teamRepos || []).map((r) => r.toLowerCase()));
  const team = new Set(bases.filter((r) => r.remotePath && want.has(r.remotePath.toLowerCase())));
  return { rows: [...bases.filter((r) => team.has(r)), ...bases.filter((r) => !team.has(r))], team };
}
