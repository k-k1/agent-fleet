// Pop-out glue (detaching a pane into its own tab) — the DOM half: opener side
// (openPanePopout) and child-boot side (consumePopoutBoot). The pure
// descriptor logic lives in layout/popout.ts; the per-tab mode flag in
// lib/popoutMode.ts.
//
// Move semantics: opening the child tab CLOSES the origin pane (removeOutright,
// so a split collapses in one step). window.open must stay synchronous inside
// the user gesture — a popup blocker / expired user activation returns null,
// in which case the pane is kept and a toast explains.
import { useLayoutStore } from "../../layout/store.ts";
import type { Pane } from "../../layout/types.ts";
import {
  canPopout,
  encodePopoutDescriptor,
  isStalePopoutEntry,
  parsePopoutDescriptor,
  popoutKey,
  POPOUT_KEY_PREFIX,
  POPOUT_NONCE_RE,
  type PopoutDescriptor,
} from "../../layout/popout.ts";
import { getTenant, pinTenantForTab, rel } from "../../core/api/client.ts";
import { setPopoutMode } from "../../lib/popoutMode.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";
import { dirtyPaneIds } from "../editor/dirtyRegistry.ts";

export { canPopout };

function newNonce(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** Tear the pane off into its own browser tab (ui: "popout" = minimal chrome,
 * "full" = a normal console seeded with this one pane). On success the origin
 * pane is removed outright; on failure (popup blocked) it stays put. */
export function openPanePopout(pane: Pane, ui: "popout" | "full"): void {
  // v1 deliberately has no cross-tab buffer transfer. Reject while dirty so a
  // delayed post-save popup cannot lose browser user activation (or leave an
  // explicitly discarded buffer behind when the popup is blocked).
  if (dirtyPaneIds().includes(pane.id)) {
    toast(t("editor.popout_dirty"), { kind: "info" });
    return;
  }
  openPanePopoutUnchecked(pane, ui);
}

function openPanePopoutUnchecked(pane: Pane, ui: "popout" | "full"): void {
  if (!canPopout(pane)) {
    toast(t("popout.cannot"), { kind: "info" });
    return;
  }
  const nonce = newNonce();
  const key = popoutKey(nonce);
  try {
    localStorage.setItem(key, encodePopoutDescriptor(pane, ui, getTenant(), Date.now()));
  } catch {
    toast(t("popout.blocked"), { kind: "error" });
    return;
  }
  // No "noopener" FEATURE here: with it window.open always returns null (spec),
  // which is indistinguishable from a blocked popup — and null is our only
  // blocked signal. Open normally, then sever the reference on success.
  const w = window.open(rel("?pane=" + nonce), "_blank");
  if (!w) {
    try {
      localStorage.removeItem(key);
    } catch {}
    toast(t("popout.blocked"), { kind: "error" });
    return;
  }
  try {
    w.opener = null;
  } catch {}
  useLayoutStore.getState().closePane(pane.id, true);
}

// Child-boot state: consumePopoutBoot() runs once in main.tsx BEFORE createRoot
// (so StrictMode / re-renders can't double-consume), stashing the descriptor
// here for App's boot effect to pick up via takePendingPopout().
let pending: PopoutDescriptor | null = null;
let staleLink = false;

/** Parse-and-strip ?pane=<nonce>, redeem the localStorage handoff, pin the
 * tenant and set the tab mode. Also sweeps abandoned handoff keys. Synchronous;
 * call before the React root mounts. */
export function consumePopoutBoot(): void {
  sweepStaleHandoffs();
  let nonce = "";
  try {
    const u = new URL(location.href);
    nonce = u.searchParams.get("pane") || "";
    if (!nonce) return;
    u.searchParams.delete("pane");
    history.replaceState(history.state, "", u.toString());
  } catch {
    return;
  }
  if (!POPOUT_NONCE_RE.test(nonce)) return;
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(popoutKey(nonce));
    localStorage.removeItem(popoutKey(nonce));
  } catch {}
  const d = parsePopoutDescriptor(raw);
  if (!d) {
    // Reload with the param re-typed / a stale bookmark: boot the normal
    // console and tell the user why (toasted from App once the sink exists).
    staleLink = true;
    return;
  }
  if (d.tenant) pinTenantForTab(d.tenant);
  setPopoutMode(d.ui);
  pending = d;
}

/** The descriptor consumed at boot, handed over exactly once (App's boot
 * effect: seed the layout instead of load()). */
export function takePendingPopout(): PopoutDescriptor | null {
  const d = pending;
  pending = null;
  return d;
}

/** True once when the boot found a ?pane link whose handoff no longer exists. */
export function takeStalePopoutLink(): boolean {
  const s = staleLink;
  staleLink = false;
  return s;
}

/** Drop af.popout.* entries no child ever redeemed (blocked tab, crash). */
function sweepStaleHandoffs(): void {
  try {
    const now = Date.now();
    const dead: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && k.startsWith(POPOUT_KEY_PREFIX) && isStalePopoutEntry(localStorage.getItem(k), now)) dead.push(k);
    }
    for (const k of dead) localStorage.removeItem(k);
  } catch {}
}
