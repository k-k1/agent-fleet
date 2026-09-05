// A ref that attaches scroll-position memory to a scroll container (the memory itself is
// in ../scrollMemory.ts).
//
// For the container it is attached to it:
//   - records the position on every reader scroll, and
//   - restores the recorded position when it attaches (a remount on returning from another
//     tab) and when the surface comes back from hidden.
//
// Restoring cannot be a single shot, because the height is settled late: for the Markdown
// preview MarkdownView writes innerHTML in a passive effect, after which highlighting,
// images and fonts each grow it; a PDF only grows its box once every page dimension is
// known. At attach time scrollHeight === clientHeight (nowhere to restore to) is the norm,
// so watch the content grow and keep pushing until the target is reached. There are only
// three ways to stop:
//   1. the target was reached (re-applied one frame later, then done),
//   2. the reader touched it (wheel / touch / pointer / key),
//   3. the budget expired (the content shrank, so the target is unreachable).
// scrollTop diverging from the value we wrote must NOT be read as "the reader touched it":
// the browser's scroll anchoring moves it on every deferred layout, which in the mirror
// pinned the view short of the target ([[mirror-scroll-position]]).
import { useMemo } from "react";
import { loadScrollPos, saveScrollPos } from "../scrollMemory.ts";

/** Budget for waiting on the content to grow. Past it the target counts as unreachable and
 *  the current position is recorded instead. Measured under 1s for Markdown once
 *  highlighting and images have finished. */
const RESTORE_BUDGET_MS = 4000;

/** What "the reader touched it" means for aborting a restore. scroll is deliberately not in
 *  the list: our own write-back and the browser's anchoring arrive in the same shape and
 *  cannot be told apart. */
const USER_EVENTS = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

/** A React 19 ref callback; the return value is the detach cleanup. */
export type ScrollMemoryRef = (el: HTMLElement | null) => (() => void) | void;

/** Returns the ref for each surface ("code" / "preview" / "pdf" …). The same surface must
 *  get the same function object: hand React a new function every render and it detaches and
 *  re-attaches, running the cleanup every render
 *  ([[react-ref-callback-identity-cleanup]]). */
export type ScrollMemoryFactory = (surface: string) => ScrollMemoryRef;

const noop: ScrollMemoryRef = () => {};

export function scrollMemoryRef(key: string | null): ScrollMemoryRef {
  if (!key) return noop;
  return (el) => {
    if (!el) return;
    let target = loadScrollPos(key);
    let restoring = false;
    let timer = 0;
    let settle = 0;
    // When the surface loses its box (hidden behind the edit tab, or a collapsed pane)
    // scrollTop drops to 0. Track visibility so "the box came back" can trigger a restore.
    let boxed = el.clientHeight > 0;

    const remember = () => {
      // A 0 while the box is gone does not mean "scrolled back to the top"; don't record it.
      if (el.clientHeight > 0) saveScrollPos(key, el.scrollTop);
    };

    const finish = () => {
      if (!restoring) return;
      restoring = false;
      window.clearTimeout(timer);
      if (settle) cancelAnimationFrame(settle);
      settle = 0;
      contentRo.disconnect();
      mo.disconnect();
      remember();
    };

    // Move towards the target. If the content is still too short, do nothing; the next
    // growth calls us again.
    const apply = () => {
      if (!restoring || target == null) return;
      const max = el.scrollHeight - el.clientHeight;
      if (max <= 0) return;
      el.scrollTop = Math.min(target, max);
      if (Math.abs(el.scrollTop - target) > 1 || settle) return;
      // Reached. A layout effect running right after this (CodeView's scrollIntoView onto
      // the quoted line) can still overwrite it, so re-apply one frame later before ending.
      settle = requestAnimationFrame(() => {
        settle = 0;
        const room = el.scrollHeight - el.clientHeight;
        if (room > 0 && target != null) el.scrollTop = Math.min(target, room);
        finish();
      });
    };

    const contentRo = new ResizeObserver(() => apply());
    // A RO on the container cannot see the content grow (scrollHeight is not a box size),
    // so observe the children. Some surfaces replace their children wholesale via
    // innerHTML, hence the childList observation too.
    const mo = new MutationObserver(() => {
      watchContent();
      apply();
    });

    const watchContent = () => {
      contentRo.disconnect();
      contentRo.observe(el);
      for (const child of Array.from(el.children)) contentRo.observe(child);
    };

    const arm = () => {
      target = loadScrollPos(key);
      if (target == null || target <= 0) return;
      if (Math.abs(el.scrollTop - target) <= 1) return; // already there
      restoring = true;
      watchContent();
      mo.observe(el, { childList: true });
      window.clearTimeout(timer);
      timer = window.setTimeout(finish, RESTORE_BUDGET_MS);
      apply();
    };

    // The RO that only watches whether the box exists stays attached: a surface returning
    // from hidden (edit vs. view) is not remounted, so this is the only trigger available.
    const boxRo = new ResizeObserver(() => {
      const now = el.clientHeight > 0;
      if (now && !boxed) arm();
      boxed = now;
    });

    const onScroll = () => {
      if (!restoring) remember();
    };
    const onUser = () => finish();

    el.addEventListener("scroll", onScroll, { passive: true });
    for (const type of USER_EVENTS) el.addEventListener(type, onUser, { passive: true });
    boxRo.observe(el);
    arm();

    return () => {
      // Never write back a half-finished restore; it would destroy the recorded target.
      if (!restoring) remember();
      restoring = false;
      window.clearTimeout(timer);
      if (settle) cancelAnimationFrame(settle);
      contentRo.disconnect();
      boxRo.disconnect();
      mo.disconnect();
      el.removeEventListener("scroll", onScroll);
      for (const type of USER_EVENTS) el.removeEventListener(type, onUser);
    };
  };
}

/** Hands out one ref per surface for a single file (the first half of a key). A changed key
 *  hands out fresh refs, so one file never inherits another file's position. */
export function useScrollMemory(baseKey: string | null): ScrollMemoryFactory {
  return useMemo(() => {
    const refs = new Map<string, ScrollMemoryRef>();
    return (surface: string) => {
      let ref = refs.get(surface);
      if (!ref) {
        ref = scrollMemoryRef(baseKey ? `${baseKey}\u0000${surface}` : null);
        refs.set(surface, ref);
      }
      return ref;
    };
  }, [baseKey]);
}
