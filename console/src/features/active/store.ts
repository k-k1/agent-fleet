// Which parents the member opened in the Active view (ADR 0145). Device-local, like a
// section's fold: it is a detail of how this screen is arranged, not a setting.
import { create } from "zustand";

const KEY = "af-active-expanded";

function read(): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(KEY) || "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

interface ActiveFoldStore {
  expanded: string[];
  /** Open or fold `name`. `known` is every session name there is right now: names that are
   *  gone are dropped on the way, so the stored list never outgrows the sessions. */
  toggle(name: string, known: ReadonlySet<string>): void;
}

export const useActiveFold = create<ActiveFoldStore>((set, get) => ({
  expanded: read(),
  toggle: (name, known) => {
    const cur = get().expanded.filter((n) => known.has(n));
    const expanded = cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name];
    try {
      localStorage.setItem(KEY, JSON.stringify(expanded));
    } catch {}
    set({ expanded });
  },
}));
