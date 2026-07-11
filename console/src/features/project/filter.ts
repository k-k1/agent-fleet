// Rail filter — the ProjectTree search box's state (zustand, so the repo tree
// and the その他のセッション section follow the same query) plus the pure match
// helpers. Matching is a case-insensitive substring test over what the rows
// actually display: repo/folder name + current branch, session display name +
// session id.
import { create } from "zustand";
import { displayName } from "../../lib/sessionview.ts";
import type { Session } from "../../types/session.ts";
import type { Repo } from "../repos/store.ts";

interface ProjectFilterStore {
  q: string;
  setQ(q: string): void;
}

export const useProjectFilter = create<ProjectFilterStore>((set) => ({
  q: "",
  setQ: (q) => set({ q }),
}));

/** The comparable form of the query — "" means "no filter". */
export const normQuery = (q: string) => q.trim().toLowerCase();

export function sessionMatches(s: Session, nq: string): boolean {
  if (!nq) return true;
  return displayName(s).toLowerCase().includes(nq) || s.name.toLowerCase().includes(nq);
}

export function repoMatches(r: Repo, nq: string): boolean {
  if (!nq) return true;
  return r.name.toLowerCase().includes(nq) || (r.branch || "").toLowerCase().includes(nq);
}
