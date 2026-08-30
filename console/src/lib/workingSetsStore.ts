// 作業グループ (docs/log/52) — the settings-backed half: which sets exist
// (`workingSets`, server-synced ui-prefs) and which one THIS device is scoped
// to (`workingSetActive`, DEVICE_LOCAL). Split from workingSets.ts so the pure
// membership predicates stay importable under the node vitest project (this
// module's settings import drags in core/api/client, which needs a browser).
// Re-exports the pure API so consumers import one module.
import { getSettings, setSetting, useSettings } from "./settings.ts";
import type { Settings } from "./settings.ts";
import { newWorkingSetId, normalizeWorkingSets } from "./workingSets.ts";
import type { WorkingSet, WorkingSetField } from "./workingSets.ts";

export * from "./workingSets.ts";

/** The well-formed working-set list of a settings snapshot. */
export function workingSetList(s: Settings): WorkingSet[] {
  return normalizeWorkingSets(s.workingSets);
}

/** The set this device is currently scoped to, or null = "すべて" (no filter).
 * A dangling selection (set deleted on another device) resolves to null instead
 * of an empty view. */
export function activeWorkingSet(s: Settings): WorkingSet | null {
  const id = s.workingSetActive;
  if (!id) return null;
  return workingSetList(s).find((w) => w.id === id) || null;
}

/** Reactive form of activeWorkingSet for rail sections. */
export function useActiveWorkingSet(): WorkingSet | null {
  return activeWorkingSet(useSettings());
}

export function setActiveWorkingSet(id: string): void {
  setSetting("workingSetActive", id);
}

export function createWorkingSet(name: string): string {
  const id = newWorkingSetId();
  setSetting("workingSets", [
    ...workingSetList(getSettings()),
    { id, name, repos: [], convs: [], sessions: [], schedules: [] },
  ]);
  return id;
}

export function renameWorkingSet(id: string, name: string): void {
  setSetting(
    "workingSets",
    workingSetList(getSettings()).map((w) => (w.id === id ? { ...w, name } : w)),
  );
}

/** Delete the group definition only — its members (repos/convs/sessions) are
 * untouched. A stale device-local selection of the deleted id falls back to
 * "すべて" via activeWorkingSet. */
export function deleteWorkingSet(id: string): void {
  setSetting(
    "workingSets",
    workingSetList(getSettings()).filter((w) => w.id !== id),
  );
  if (getSettings().workingSetActive === id) setSetting("workingSetActive", "");
}

export function toggleWorkingSetMember(id: string, field: WorkingSetField, key: string): void {
  setSetting(
    "workingSets",
    workingSetList(getSettings()).map((w) => {
      if (w.id !== id) return w;
      const has = w[field].includes(key);
      return { ...w, [field]: has ? w[field].filter((k) => k !== key) : [...w[field], key] };
    }),
  );
}

/** docs/log/52 §1: anything created WHILE a group is selected joins that group —
 * without this, a fresh clone / conversation / repo-less session would vanish
 * from the filtered rail the moment it appears. No-op when no group is active. */
export function autoAddToActiveWorkingSet(field: WorkingSetField, key: string): void {
  if (!key) return;
  const active = activeWorkingSet(getSettings());
  if (!active || active[field].includes(key)) return;
  toggleWorkingSetMember(active.id, field, key);
}

/** Cycle すべて → set1 → set2 → … → すべて (the keyboard command). Returns the
 * newly active set, or null for すべて. */
export function cycleActiveWorkingSet(): WorkingSet | null {
  const s = getSettings();
  const list = workingSetList(s);
  if (list.length === 0) return null;
  const i = list.findIndex((w) => w.id === s.workingSetActive);
  const next = i < 0 ? list[0] : list[i + 1] || null; // last set wraps to すべて
  setSetting("workingSetActive", next ? next.id : "");
  return next;
}
