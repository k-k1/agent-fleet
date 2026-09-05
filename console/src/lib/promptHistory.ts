// Per-repo recent first-prompt history for the launch modal — a lightweight companion to
// the .claude/commands / skills / file template sources. Stored in localStorage (like
// repoLast) so it's instant and survives session archival. Newest first, deduped, capped.
const KEY = (repo: string) => "af.repo-prompts." + repo;
const MAX = 8;

export function readPromptHistory(repo: string): string[] {
  if (!repo) return [];
  try {
    const raw = localStorage.getItem(KEY(repo));
    const arr = raw ? JSON.parse(raw) : [];
    return Array.isArray(arr) ? arr.filter((s) => typeof s === "string") : [];
  } catch {
    return [];
  }
}

// pushPromptHistory records a just-launched prompt at the front, dropping any earlier
// identical entry so re-running the same prompt doesn't pile up duplicates.
export function pushPromptHistory(repo: string, prompt: string): void {
  const p = (prompt || "").trim();
  if (!repo || !p) return;
  try {
    const next = [p, ...readPromptHistory(repo).filter((s) => s !== p)].slice(0, MAX);
    localStorage.setItem(KEY(repo), JSON.stringify(next));
  } catch {
    /* private mode / quota — history is best-effort */
  }
}
