// Per-repo memory of the last kind/model launched, so the launch modal (LaunchModal)
// opens defaulted to what you last used in this repo. Kept in localStorage (not
// derived from the sessions list) so it renders instantly with no fetch, and survives
// session archival. Written through on EVERY launch path — modal, quick dropdown, and
// the right-click menu — so the default tracks real usage, not just modal launches.
import { CLAUDE_MODELS, DEFAULT_MODEL, getSettings } from "./settings.ts";
import { isModelHidden, modelMatchesHidden } from "./modelDeny.ts";
import { repoLaunchKinds } from "../agents/registry.ts";

const KIND_KEY = (repo: string) => "af.repo-lastkind." + repo;
// The model memory is per (kind, repo): a codex model id must never leak into a
// claude launch and vice versa. claude keeps its historical unscoped key so
// existing stored defaults survive this change.
const MODEL_KEY = (repo: string, kind: string) =>
  kind === "claude" ? "af.repo-model." + repo : `af.repo-model.${kind}.` + repo;
const EFFORT_KEY = (repo: string, kind: string) => `af.repo-effort.${kind}.` + repo;
const MODE_KEY = (repo: string, kind: string) => `af.repo-startmode.${kind}.` + repo;
// The working folder inside the repo (Meta.Subdir) is a property of the repo's layout,
// not of the agent, so it is remembered per repo and shared across kinds: someone who
// always works in `console/` wants that folder whichever agent they pick next.
const SUBDIR_KEY = (repo: string) => "af.repo-subdir." + repo;
// Empty string is a real selection (CLI/model default or standard effort). Keep a
// sentinel so it remains distinguishable from "this repo has no remembered value".
const DEFAULT_VALUE = "@af:default";

export interface RepoLast {
  kind?: string;
}

export function readRepoLast(repo: string): RepoLast {
  if (!repo) return {};
  try {
    return { kind: localStorage.getItem(KIND_KEY(repo)) || undefined };
  } catch {
    return {};
  }
}

function readRemembered(key: string): { found: boolean; value: string } {
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) return { found: false, value: "" };
    return { found: true, value: raw === DEFAULT_VALUE ? "" : raw };
  } catch {
    return { found: false, value: "" };
  }
}

// resolveModel picks the initial model for a NEW `kind` session in `repo`, applying the
// one shared priority chain used by every launch entry point (LaunchModal / quick launch;
// NewSessionModal has no repo yet so it passes "" and starts at the default).
//   claude: repo last-used → global default (settings.defaultModel) → DEFAULT_MODEL.
//     The terminal fallback is a concrete tier, never "" — so a legacy stored default of
//     "" (from the removed "default" option) is coerced to DEFAULT_MODEL rather than deferring
//     to claude's release-varying own pick.
//   codex/opencode: repo last-used → "" (default — the CLI's own pick). Their catalogs
//     are release-varying concrete ids, so there is no pinned terminal fallback, and the
//     claude-oriented settings.defaultModel must not apply.
// The caller's own explicit pick (modal selection) and fork/relaunch inheritance sit
// ABOVE this — they overwrite whatever this returns — so this only computes the initial
// default. Only meaningful for kinds with caps.model.
// A model listed under "models to exclude" (settings.hiddenModels) is never adopted at any step of
// the chain. AgentsTab sweeps the stored values on the device where the setting changed, but prefs
// sync per user while localStorage does not, so another device keeps its pre-sweep copy — that
// device alone would default to an excluded model and be refused by the Agent's guard on every
// launch. This is the last line of defence against that.
function visibleOr(kind: string, model: string, fallback: () => string): string {
  const claudeIds = kind === "claude" ? CLAUDE_MODELS.map(([id]) => id) : undefined;
  return isModelHidden(getSettings().hiddenModels, kind, model, claudeIds) ? fallback() : model;
}

export function resolveModel(kind: string, repo: string, defaultModel: string): string {
  const claudeFallback = () => {
    const hidden = getSettings().hiddenModels;
    const ids = CLAUDE_MODELS.map(([id]) => id);
    return ids.find((id) => !isModelHidden(hidden, "claude", id, ids)) || DEFAULT_MODEL;
  };
  const last = repo ? readRemembered(MODEL_KEY(repo, kind)) : { found: false, value: "" };
  if (last.found) {
    // Only when it was excluded, fall through the same path as "nothing remembered" (the default).
    const kept = visibleOr(kind, last.value, () => "");
    if (kept || !last.value) return kept;
  }
  if (kind === "claude") return visibleOr("claude", defaultModel || DEFAULT_MODEL, claudeFallback);
  return visibleOr(kind, defaultModel, () => ""); // dynamic kinds fall back to the default, i.e. the CLI's pick
}

// forgetHiddenRepoModels drops an id that was added to "models to exclude" from the per-repo
// last-used memory (this localStorage) as well. Sweeping only the settings side (AgentsTab) leaves
// this behind, and that repo's launch path keeps defaulting to the excluded model. Once removed,
// resolveModel falls back to the kind's default.
const OTHER_KIND_PREFIXES = repoLaunchKinds
  .filter((k) => k !== "claude")
  .map((k) => `af.repo-model.${k}.`);

export function forgetHiddenRepoModels(kind: string, hidden: string[]): void {
  if (!kind || !hidden.length) return;
  const prefix = kind === "claude" ? "af.repo-model." : `af.repo-model.${kind}.`;
  try {
    const drop: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key || !key.startsWith(prefix)) continue;
      // claude's memory key carries no kind, so it is a prefix of every other kind's key. When
      // sweeping claude, skip the kind-scoped keys so they are not taken out with it.
      if (kind === "claude" && OTHER_KIND_PREFIXES.some((p) => key.startsWith(p))) continue;
      const value = localStorage.getItem(key) || "";
      if (value && value !== DEFAULT_VALUE && hidden.some((h) => modelMatchesHidden(value, h))) drop.push(key);
    }
    drop.forEach((key) => localStorage.removeItem(key));
  } catch {
    /* private mode — at worst this device keeps its stale last-used value; the Agent still refuses the launch */
  }
}

export function resolveEffort(kind: string, repo: string, defaultEffort = ""): string {
  if (!repo) return defaultEffort;
  const last = readRemembered(EFFORT_KEY(repo, kind));
  return last.found ? last.value : defaultEffort;
}

export function resolveStartMode(kind: string, repo: string, defaultMode: string): "normal" | "plan" {
  if (!repo) return defaultMode === "plan" ? "plan" : "normal";
  try {
    const saved = localStorage.getItem(MODE_KEY(repo, kind));
    if (saved === "plan" || saved === "normal") return saved;
    return defaultMode === "plan" ? "plan" : "normal";
  } catch {
    return defaultMode === "plan" ? "plan" : "normal";
  }
}

// resolveSubdir returns the folder inside `repo` the last launch used ("" = the repo
// root). Only a hint for the launch dialog — the Agent validates the path at create,
// so a folder that has since been deleted or renamed just fails there and the user
// picks another (the field is visible and pre-filled, never applied silently).
export function resolveSubdir(repo: string): string {
  if (!repo) return "";
  try {
    return localStorage.getItem(SUBDIR_KEY(repo)) || "";
  } catch {
    return "";
  }
}

export function writeRepoSubdir(repo: string, subdir: string): void {
  if (!repo) return;
  try {
    if (subdir) localStorage.setItem(SUBDIR_KEY(repo), subdir);
    else localStorage.removeItem(SUBDIR_KEY(repo));
  } catch {
    /* private mode / quota — the next launch just starts at the repo root */
  }
}

// writeRepoLast records the kind, and (when provided) the model under that kind's
// slot — callers pass model only for kinds with caps.model; "" clears the stored model
// (the next launch then falls back to the kind's default via resolveModel).
export function writeRepoLast(repo: string, kind: string, model?: string, effort?: string, startMode?: string): void {
  if (!repo || !kind) return;
  try {
    localStorage.setItem(KIND_KEY(repo), kind);
    if (model !== undefined) {
      localStorage.setItem(MODEL_KEY(repo, kind), model || DEFAULT_VALUE);
    }
    if (effort !== undefined) {
      localStorage.setItem(EFFORT_KEY(repo, kind), effort || DEFAULT_VALUE);
    }
    if (startMode !== undefined) {
      localStorage.setItem(MODE_KEY(repo, kind), startMode === "plan" ? "plan" : "normal");
    }
  } catch {
    /* private mode / quota — the default just falls back to the first agent */
  }
}
