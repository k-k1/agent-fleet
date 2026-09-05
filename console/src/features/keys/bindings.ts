// User keybinding resolution — the app-binding layer between the pure registry
// (lib/keys/registry.ts) and the store (lib/settings.ts `keybindings`). Everything that
// decides "what chord fires this action" reads through here, so a rebind takes effect for
// the dispatcher, which-key, palette, cheat-sheet and button tooltips at once. See
// docs/log/29 + ADR-0017.
//
// Two kinds of rebindable action share one override map (Settings.keybindings, id → chord):
//   - registry commands that carry a direct accelerator (`keys`) — overridden via
//     applyOverrides, which the dispatcher/overlays already consume as a Command[].
//   - the three reserved dispatcher chords that are NOT registry commands (the leader, the
//     palette, the cheat-sheet) — resolved by id through boundChord().
// Leader SEQUENCES (p r, w t …) are structural and deliberately not rebindable.
import { useSyncExternalStore } from "react";
import { getSettings, setSettings, subscribe } from "../../lib/settings.ts";
import { canonical } from "../../lib/keys/chords.ts";
import { applyOverrides } from "../../lib/keys/registry.ts";
import type { Command } from "../../lib/keys/registry.ts";
import { ALL_COMMANDS } from "./commands.ts";

// Synthetic ids for the reserved dispatcher-level chords (kept distinct from any command
// id by the "app." prefix). Their defaults live here — the single source the dispatcher
// falls back to when the user hasn't rebound them.
export const APP_LEADER = "app.leader";
export const APP_PALETTE = "app.palette";
export const APP_CHEAT = "app.cheatsheet";

export const APP_DEFAULTS: Record<string, string> = {
  [APP_LEADER]: "mod+k",
  [APP_PALETTE]: "mod+p",
  [APP_CHEAT]: "shift+/",
};

/** i18n keys for the three app chords' titles (rebind UI + cheat-sheet). Resolve with
 * cmdLabel (labels.ts) at the display site. */
export const APP_TITLES: Record<string, string> = {
  [APP_LEADER]: "keys.app.leader",
  [APP_PALETTE]: "keys.app.palette",
  [APP_CHEAT]: "keys.app.cheatsheet",
};

const EMPTY: Record<string, string> = {}; // stable fallback so the effectiveCommands cache key doesn't churn

/** The raw override map (id → chord; "" = explicitly unbound). Always an object. */
export function overrides(): Record<string, string> {
  const kb = getSettings().keybindings;
  return kb && typeof kb === "object" ? kb : EMPTY;
}

/** Effective, canonicalized chord for a reserved app id (respecting any override). An
 * empty override string means "unbound" and yields "" — the dispatcher then never
 * matches it (used e.g. to free the leader entirely for a 100% pure terminal). */
export function boundChord(id: string): string {
  const ov = overrides()[id];
  const raw = ov !== undefined ? ov : APP_DEFAULTS[id];
  return raw ? canonical(raw) : "";
}

// Memoize on the overrides object identity. The settings store swaps `keybindings` to a
// fresh object only when a binding actually changes, so between unrelated settings updates
// the SAME reference comes back and we return the SAME array — essential for
// useEffectiveCommands (useSyncExternalStore treats a new snapshot as a change and would
// otherwise re-render forever; React #185).
let cacheKey: Record<string, string> | null = null;
let cacheVal: Command[] = ALL_COMMANDS;

/** ALL_COMMANDS with the user's direct-accelerator overrides applied (referentially
 * stable while the overrides object is unchanged). */
export function effectiveCommands(): Command[] {
  const ov = overrides();
  if (ov !== cacheKey) {
    cacheKey = ov;
    cacheVal = applyOverrides(ALL_COMMANDS, ov);
  }
  return cacheVal;
}

/** Persist (or clear) one override. chord=null resets the action to its default; ""
 * explicitly unbinds it. Writes through setSettings so it syncs like every other pref. */
export function setBinding(id: string, chord: string | null): void {
  const cur = { ...overrides() };
  if (chord === null) delete cur[id];
  else cur[id] = chord ? canonical(chord) : "";
  setSettings({ keybindings: cur });
}

/** Reset every override at once (the rebind tab's "reset all to defaults"). */
export function resetBindings(): void {
  setSettings({ keybindings: {} });
}

// ---- Rebind UI model ----------------------------------------------------------------

export interface Rebindable {
  id: string; // command id or app.* synthetic id
  /** i18n key for the action name — resolve with cmdLabel at the display site. */
  title: string;
  /** The chord as currently bound (override or default; "" = unbound). */
  chord: string;
  /** The default chord, for the "reset to default" affordance / dirty check. */
  def: string;
  /** true when a user override is in effect (differs from default OR unbound). */
  overridden: boolean;
}

interface RebindSection {
  /** i18n key for the section header — resolve with cmdLabel at the display site. */
  title: string;
  items: Rebindable[];
}

function rebindable(id: string, title: string, def: string): Rebindable {
  const ov = overrides()[id];
  const chord = ov !== undefined ? (ov ? canonical(ov) : "") : canonical(def);
  return { id, title, chord, def: canonical(def), overridden: ov !== undefined };
}

/** The rebindable actions grouped for the settings tab: the three app chords, then every
 * registry command that has a direct accelerator (leader-only commands are omitted — they
 * have no rebindable key). Grouped by the same leader groups the cheat-sheet uses. */
export function rebindSections(): RebindSection[] {
  const secs: RebindSection[] = [
    {
      title: "keys.kt.secApp",
      items: [APP_LEADER, APP_PALETTE, APP_CHEAT].map((id) => rebindable(id, APP_TITLES[id], APP_DEFAULTS[id])),
    },
  ];
  const withKeys = ALL_COMMANDS.filter((c) => c.keys && c.keys.length);
  const groups: { title: string; test: (id: string) => boolean }[] = [
    { title: "keys.grp.pane", test: (id) => id.startsWith("pane.") },
    { title: "keys.kt.secRegion", test: (id) => id.startsWith("region.") },
    { title: "keys.grp.workspace", test: (id) => id.startsWith("workspace.") },
  ];
  for (const g of groups) {
    const items = withKeys.filter((c) => g.test(c.id)).map((c) => rebindable(c.id, c.title, c.keys![0]));
    if (items.length) secs.push({ title: g.title, items });
  }
  // Anything with a direct key that didn't land in a group above.
  const claimed = new Set(secs.flatMap((s) => s.items.map((i) => i.id)));
  const rest = withKeys.filter((c) => !claimed.has(c.id)).map((c) => rebindable(c.id, c.title, c.keys![0]));
  if (rest.length) secs.push({ title: "keys.kt.secOther", items: rest });
  return secs;
}

/** chord → ids that currently resolve to it, for every id in {app chords} ∪ {direct-key
 * commands}. Only entries with 2+ ids are conflicts. Unbound ("") is excluded. */
export function bindingConflicts(): Map<string, string[]> {
  const byChord = new Map<string, string[]>();
  for (const sec of rebindSections()) {
    for (const it of sec.items) {
      if (!it.chord) continue;
      const arr = byChord.get(it.chord) || [];
      arr.push(it.id);
      byChord.set(it.chord, arr);
    }
  }
  const out = new Map<string, string[]>();
  for (const [chord, ids] of byChord) if (ids.length > 1) out.set(chord, ids);
  return out;
}

/** Subscribe a React component to keybinding changes and return the live command list.
 * A tiny wrapper over the settings store so overlays re-render when a binding changes. */
export function useEffectiveCommands(): Command[] {
  return useSyncExternalStore(subscribe, effectiveCommands, effectiveCommands);
}
