// Per-repo memory of the last kind/model launched, so the 起動 modal (LaunchModal)
// opens defaulted to what you last used in this repo. Kept in localStorage (not
// derived from the sessions list) so it renders instantly with no fetch, and survives
// session archival. Written through on EVERY launch path — modal, quick dropdown, and
// the right-click menu — so the default tracks real usage, not just modal launches.
const KIND_KEY = (repo: string) => "af.repo-lastkind." + repo;
const MODEL_KEY = (repo: string) => "af.repo-model." + repo;

export interface RepoLast {
  kind?: string;
  model?: string;
}

export function readRepoLast(repo: string): RepoLast {
  if (!repo) return {};
  try {
    return {
      kind: localStorage.getItem(KIND_KEY(repo)) || undefined,
      model: localStorage.getItem(MODEL_KEY(repo)) || undefined,
    };
  } catch {
    return {};
  }
}

// resolveModel picks the initial claude model for a NEW session in `repo`, applying the
// one shared priority chain used by every launch entry point (LaunchModal / quick launch;
// NewSessionModal has no repo yet so it passes "" and starts at the default tier):
//   repo last-used  →  global default (settings.defaultModel)  →  ""  (claude's own,
//   release-varying default; reached only when the global default is itself "既定"/"").
// The caller's own explicit pick (modal selection) and fork/relaunch inheritance sit ABOVE
// this — they overwrite whatever this returns — so this only computes the initial default.
// Only meaningful for claude (caps.model); other agents pass "" straight through.
export function resolveModel(repo: string, defaultModel: string): string {
  return readRepoLast(repo).model || defaultModel || "";
}

// writeRepoLast records the kind, and (when provided) the model — model is only
// meaningful for claude, so callers pass it only then; "" clears it back to 既定.
export function writeRepoLast(repo: string, kind: string, model?: string): void {
  if (!repo || !kind) return;
  try {
    localStorage.setItem(KIND_KEY(repo), kind);
    if (model !== undefined) {
      if (model) localStorage.setItem(MODEL_KEY(repo), model);
      else localStorage.removeItem(MODEL_KEY(repo));
    }
  } catch {
    /* private mode / quota — the default just falls back to the first agent */
  }
}
