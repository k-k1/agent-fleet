// revealRepoInRail — put a working copy on screen in the project tree and focus its
// row. Used by the command palette's repo rows (Enter = focus the left rail). Docks the
// rail open, then fires the reveal signal the tree's RepoNodes listen for (expand the
// node — and its base, for a worktree — then scroll + focus). Kept out of the palette so
// the store wiring lives with the repos feature.
import { useLeftRail } from "../../core/store/leftRail.ts";
import { synthContextMenu } from "../project/contextMenuKey.ts";
import { useRepoReveal } from "./store.ts";

export function revealRepoInRail(name: string): void {
  useLeftRail.getState().ensureOpen();
  useRepoReveal.getState().reveal(name);
}

// How long a revealed row may take to mount: the expand is a React render away, and a
// worktree's row mounts only after its base node has expanded too.
const ROW_WAIT_MS = 1000;

const rowSelector = (name: string) =>
  `[data-rail-repo="${typeof CSS !== "undefined" && CSS.escape ? CSS.escape(name) : name}"]`;

// openRepoMenuInRail reveals a working copy's row and opens that row's own context menu
// next to it, so the session menu can lead to the repository menu without a second copy
// of it (issue #1557). Resolves false when no row appeared in time (the working set or the
// rail search keeps it out of the tree) and the caller says so; nothing opens in that case.
//
// Once the row is mounted it is scrolled to and focused here rather than waiting for the
// node's own reveal to do it: the menu anchors at the row's position, and a keyboard user
// closing the menu gets focus back on the row. The menu opens a frame later, after the
// scroll has landed.
export function openRepoMenuInRail(name: string): Promise<boolean> {
  revealRepoInRail(name);
  const deadline = performance.now() + ROW_WAIT_MS;
  return new Promise((resolve) => {
    const tick = () => {
      const el = document.querySelector<HTMLElement>(rowSelector(name));
      if (el) {
        el.scrollIntoView?.({ block: "nearest" });
        el.focus();
        requestAnimationFrame(() => {
          if (!el.isConnected) return resolve(false);
          synthContextMenu(el);
          resolve(true);
        });
        return;
      }
      if (performance.now() >= deadline) return resolve(false);
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}
