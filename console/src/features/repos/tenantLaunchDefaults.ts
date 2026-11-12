// Applies the member's launch defaults (ADR 0114 P3) to the launch dialog and Start. Pure helpers.
// Everything here PREFILLS. Nothing writes the Agent's ui-prefs, which keeps enforcing its own.
//
// Two layers are read, each on its own terms:
//   - the member's own saved block (`state.launch`, written by "Use as my default"): explicit and
//     complete for ITS agent, so for that kind it is used as is. An empty model there means "the
//     CLI's own default", not "unset", and a budget of 0 means "none".
//   - the tenant's block (`state.defaults`), for every other case. A model id belongs to one agent,
//     so the tenant's model is used only for the tenant's own agent (the CP's merged
//     `effective.launch.agent` can name a member-chosen agent beside a tenant model).
// Settings › Agents values that differ from the built-in ones also outrank the tenant's block. A
// value equal to the built-in one cannot be told from "never set", so a member who wants it against
// a team default saves it with "Use as my default".
import type { EffectiveDefaults, LaunchDefaults, MemberUIState } from "../../core/store/uiState.ts";
import { agentOf } from "../../agents/registry.ts";
import { agentLaunchDefault } from "../../lib/settings.ts";
import { DEFAULT_AGENT_LAUNCH } from "../../lib/settings/normalize.ts";
import type { Settings } from "../../lib/settings.ts";

/** The agent to start on when it is among the launchable kinds, else undefined. */
export function defaultKind(eff: EffectiveDefaults | undefined, kinds: string[]): string | undefined {
  const a = eff?.launch.agent;
  return a && kinds.includes(a) ? a : undefined;
}

const modelChosen = (s: Settings, kind: string) => agentLaunchDefault(s, kind).model !== DEFAULT_AGENT_LAUNCH[kind]?.model;
const permChosen = (s: Settings, kind: string) =>
  agentLaunchDefault(s, kind).skipPermissions !== (DEFAULT_AGENT_LAUNCH[kind]?.skipPermissions ?? true);

/** The member's own saved block when it is for this kind. */
const own = (st: MemberUIState, kind: string): LaunchDefaults | undefined => (st.launch && st.launch.agent === kind ? st.launch : undefined);
const tenant = (st: MemberUIState) => st.defaults?.launch;

/** The model to start from, or undefined = no opinion (the Settings value / the chain applies). */
export function tenantModel(st: MemberUIState, kind: string, s: Settings): string | undefined {
  const o = own(st, kind);
  if (o) return o.model;
  const t = tenant(st);
  return t?.model && st.defaults?.agent === kind && !modelChosen(s, kind) ? t.model : undefined;
}

/** "managed" / "" (Terminal) for a kind that offers both, or null = follow the kind's own default. */
export function tenantDriver(st: MemberUIState, kind: string): string | null {
  const a = agentOf(kind);
  const d = own(st, kind)?.driver || tenant(st)?.driver;
  if (!a.managedDriver || a.terminalDriver === false || !d) return null;
  return d === "managed" ? "managed" : "";
}

/** undefined = leave it to the Agent's default; else the prompt-skipping choice to start from. */
export function tenantSkipPerm(st: MemberUIState, kind: string, s: Settings): boolean | undefined {
  const o = own(st, kind);
  const m = o ? o.permission_mode : permChosen(s, kind) ? "" : tenant(st)?.permission_mode;
  return m ? m === "skip" : undefined;
}

/** The budget text to start from; "" when none applies. */
export function tenantBudget(st: MemberUIState, kind: string, s: Settings): string {
  const o = own(st, kind);
  const b = o ? o.budget_usd : s.sessionSpendCapUsd > 0 ? null : tenant(st)?.budget_usd;
  return b == null ? "" : String(b);
}

export const MAX_OWN_BUDGET = 10000;
const MODEL_RE = /^[A-Za-z0-9][A-Za-z0-9._:/@#[\]-]{0,99}$/;

/** Why the dialog's current values cannot be saved as the member's default (the CP would answer
 *  400), or "" when they can. Keeps the button honest instead of failing silently. */
export function ownLaunchProblem(kind: string, memberAgents: string[], model: string, budget: number | null): "agent" | "model" | "budget" | "" {
  if (!memberAgents.includes(kind)) return "agent";
  if (model && !MODEL_RE.test(model)) return "model";
  if (budget != null && (!Number.isFinite(budget) || budget < 0 || budget > MAX_OWN_BUDGET)) return "budget";
  return "";
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
