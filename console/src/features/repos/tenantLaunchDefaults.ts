// Applies the member's launch defaults (ADR 0114 P3) to the launch dialog and Start. Pure helpers.
// Everything here PREFILLS. Nothing writes the Agent's ui-prefs, which keeps enforcing its own.
//
// Precedence is the CP's: it merges built-in < tenant < the member's own field by field ("" / null =
// not set at that layer) and answers `effective`. This reads that, and decides only where a value
// may be used:
//   - a model id belongs to one agent, so it applies only to the agent of the layer that set it (the
//     member's block when it carries a model, else the tenant's), never to a merged-in other agent;
//   - Settings › Agents values that differ from the built-in ones outrank the TENANT's values. A value
//     equal to the built-in one cannot be told from "never set", so a member who wants it against a
//     team default saves it with "Use as my default" (which is the member layer, above the tenant's).
// An empty model or no budget in the member's block inherits the tenant's, as the CP contract says.
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

const merged = (st: MemberUIState) => st.effective?.launch;

/** The model to start from, or undefined = no opinion (the Settings value / the chain applies). */
export function tenantModel(st: MemberUIState, kind: string, s: Settings): string | undefined {
  const m = merged(st)?.model;
  if (!m) return undefined;
  const memberSet = !!st.launch?.model;
  const source = memberSet ? st.launch?.agent || st.defaults?.agent : st.defaults?.agent;
  return source === kind && (memberSet || !modelChosen(s, kind)) ? m : undefined;
}

/** "managed" / "" (Terminal) for a kind that offers both, or null = follow the kind's own default. */
export function tenantDriver(st: MemberUIState, kind: string): string | null {
  const a = agentOf(kind);
  const d = merged(st)?.driver;
  if (!a.managedDriver || a.terminalDriver === false || !d) return null;
  return d === "managed" ? "managed" : "";
}

/** undefined = leave it to the Agent's default; else the prompt-skipping choice to start from. */
export function tenantSkipPerm(st: MemberUIState, kind: string, s: Settings): boolean | undefined {
  const m = merged(st)?.permission_mode;
  if (!m || (!st.launch?.permission_mode && permChosen(s, kind))) return undefined;
  return m === "skip";
}

/** The budget text to start from; "" when none applies. */
export function tenantBudget(st: MemberUIState, _kind: string, s: Settings): string {
  const b = merged(st)?.budget_usd;
  if (b == null || (st.launch?.budget_usd == null && s.sessionSpendCapUsd > 0)) return "";
  return String(b);
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
