// Per-repo memory of the last kind/model launched, so the 起動 modal (LaunchModal)
// opens defaulted to what you last used in this repo. Kept in localStorage (not
// derived from the sessions list) so it renders instantly with no fetch, and survives
// session archival. Written through on EVERY launch path — modal, quick dropdown, and
// the right-click menu — so the default tracks real usage, not just modal launches.
import { DEFAULT_MODEL } from "./settings.ts";

const KIND_KEY = (repo: string) => "af.repo-lastkind." + repo;
// The model memory is per (kind, repo): a codex model id must never leak into a
// claude launch and vice versa. claude keeps its historical unscoped key so
// existing stored defaults survive this change.
const MODEL_KEY = (repo: string, kind: string) =>
  kind === "claude" ? "af.repo-model." + repo : `af.repo-model.${kind}.` + repo;

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

function readRepoModel(repo: string, kind: string): string {
  if (!repo) return "";
  try {
    return localStorage.getItem(MODEL_KEY(repo, kind)) || "";
  } catch {
    return "";
  }
}

// resolveModel picks the initial model for a NEW `kind` session in `repo`, applying the
// one shared priority chain used by every launch entry point (LaunchModal / quick launch;
// NewSessionModal has no repo yet so it passes "" and starts at the default).
//   claude: repo last-used → global default (settings.defaultModel) → DEFAULT_MODEL.
//     The terminal fallback is a concrete tier, never "" — so a legacy stored default of
//     "" (from the removed "既定" option) is coerced to DEFAULT_MODEL rather than deferring
//     to claude's release-varying own pick.
//   codex/opencode: repo last-used → "" (既定 — the CLI's own default). Their catalogs
//     are release-varying concrete ids, so there is no pinned terminal fallback, and the
//     claude-oriented settings.defaultModel must not apply.
// The caller's own explicit pick (modal selection) and fork/relaunch inheritance sit
// ABOVE this — they overwrite whatever this returns — so this only computes the initial
// default. Only meaningful for kinds with caps.model.
export function resolveModel(kind: string, repo: string, defaultModel: string): string {
  const last = readRepoModel(repo, kind);
  if (kind === "claude") return last || defaultModel || DEFAULT_MODEL;
  return last;
}

// writeRepoLast records the kind, and (when provided) the model under that kind's
// slot — callers pass model only for kinds with caps.model; "" clears the stored model
// (the next launch then falls back to the kind's default via resolveModel).
export function writeRepoLast(repo: string, kind: string, model?: string): void {
  if (!repo || !kind) return;
  try {
    localStorage.setItem(KIND_KEY(repo), kind);
    if (model !== undefined) {
      if (model) localStorage.setItem(MODEL_KEY(repo, kind), model);
      else localStorage.removeItem(MODEL_KEY(repo, kind));
    }
  } catch {
    /* private mode / quota — the default just falls back to the first agent */
  }
}
