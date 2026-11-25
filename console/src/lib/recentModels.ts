// The registered models most recently launched with, per agent, for the launch strip's model row.
// Device-wide on purpose (not per repository): the row would otherwise start empty in every
// repository. Newest first, capped at RECENT_MODELS_MAX; localStorage only, so a private-mode
// browser just shows no recents.
export const RECENT_MODELS_MAX = 3;

const KEY = (kind: string) => `af.recent-models.${kind}`;

export function readRecentModels(kind: string): string[] {
  if (!kind) return [];
  try {
    const v: unknown = JSON.parse(localStorage.getItem(KEY(kind)) || "[]");
    if (!Array.isArray(v)) return [];
    return v.filter((x): x is string => typeof x === "string" && x !== "").slice(0, RECENT_MODELS_MAX);
  } catch {
    return [];
  }
}

// rememberRecentModel moves `model` to the front. An empty model (the agent's default) is not a
// registered choice and is never recorded.
export function rememberRecentModel(kind: string, model: string): void {
  if (!kind || !model) return;
  const next = [model, ...readRecentModels(kind).filter((m) => m !== model)].slice(0, RECENT_MODELS_MAX);
  try {
    localStorage.setItem(KEY(kind), JSON.stringify(next));
  } catch {
    /* quota / private mode — the row just has no recents */
  }
}
