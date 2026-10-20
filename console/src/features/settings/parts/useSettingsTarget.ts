import { useEffect, type RefObject } from "react";
import { useSettingsUI } from "../store.ts";
import { parseMemoryTarget } from "../memory/memorySearch.ts";

const FLASH_CLASS = "p-card-flash";
const FLASH_MS = 1800;

// Honours `openSettings(section, { target })`: once the section's content exists (`ready`),
// scrolls the element carrying `data-settings-target="<target>"` into view and flashes it.
// The request is consumed either way, so a target that matches nothing (agent not installed or
// hidden) leaves the section at the top, as it was before targets existed, and does not linger
// to fire on a later, unrelated mount. Nothing here expands anything: the connection part of a
// card, where the login lives, is never collapsed.
//
// `ready` must stay false while the target's element may still be about to mount (async load),
// otherwise the request is consumed too early and the scroll is lost.
export function useSettingsTarget(root: RefObject<HTMLElement | null>, ready: boolean): void {
  const raw = useSettingsUI((s) => s.settingsTarget);
  // A memory request is addressed to Agent memory. While Settings switches sections this section is
  // still mounted for a render, and taking the request would drop it before its owner mounts.
  const target = parseMemoryTarget(raw) ? null : raw;
  const clear = useSettingsUI((s) => s.clearSettingsTarget);
  useEffect(() => {
    if (!target || !ready) return;
    clear();
    const el = Array.from(root.current?.querySelectorAll<HTMLElement>("[data-settings-target]") ?? []).find(
      (e) => e.dataset.settingsTarget === target,
    );
    if (!el) return;
    // Smooth scrolling is motion; people who asked for less of it get a jump.
    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    el.scrollIntoView?.({ block: "start", behavior: reduce ? "auto" : "smooth" });
    el.classList.add(FLASH_CLASS);
    // Not tied to the effect: `clear()` above re-runs it, and a cleanup would cut the flash short.
    window.setTimeout(() => el.classList.remove(FLASH_CLASS), FLASH_MS);
  }, [target, ready, root, clear]);
}
