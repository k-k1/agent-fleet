// Rail filter — the ProjectTree search box's state (zustand, so the repo tree
// and the other-sessions section follow the same query) plus the pure match
// helpers. Matching is a case-insensitive substring test over what identifies a
// row, displayed or not: for a working copy its folder name, current branch and
// (SVN) URL; for a session its display name, slug, label, working-copy folder,
// branch (launch-time and current) and working-dir basename — so a slug, a
// branch or a directory name finds both the repo node and the session rows in
// it, even when every session carries a hand-written title.
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

/** Last path segment — full paths would make every row match "repos" or "home". */
const pathTail = (p: string | null | undefined) => (p || "").split("/").filter(Boolean).pop() || "";

export function sessionMatches(s: Session, nq: string): boolean {
  if (!nq) return true;
  const hay = [
    displayName(s),
    s.name,
    s.label || "",
    s.repo || "",
    s.branch || "",
    s.currentBranch || "",
    pathTail(s.dir || s.path),
  ]
    .join("\n")
    .toLowerCase();
  return hay.includes(nq);
}

export function repoMatches(r: Repo, nq: string): boolean {
  if (!nq) return true;
  const hay = [r.name, r.branch || "", r.url || ""].join("\n").toLowerCase();
  return hay.includes(nq);
}
