// External-change probe (docs/log/44 §7, Phase 3.5) — polls the metadata-only
// GET (`meta=1`) for the file a pane is showing, so an edit made by an agent,
// a shell, or git is noticed before the save-time CAS would surface it. The
// probe is advisory: it adds early warning, never a guarantee, and it must
// not pre-empt an in-flight PUT (the 200 settles the base itself).
import { useEffect, useRef } from "react";
import { probeFileMeta, type FileProbeResult } from "./api.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";

/** Low-frequency fallback tick. Deliberately much slower than the 4s session
 *  poll (docs/log/44 §7.2) — the immediate triggers (tab return, window focus,
 *  pane activation) are what make the probe feel live. */
export const EXTERNAL_PROBE_INTERVAL_MS = 12_000;

export interface ExternalProbeGates {
  path: string | null;
  documentVisible: boolean;
  paneVisible: boolean;
  workspaceRunning: boolean;
  saving: boolean;
}

/** The probe runs only when every §7.2 condition holds. */
export function shouldProbe(gates: ExternalProbeGates): boolean {
  return (
    !!gates.path &&
    gates.documentVisible &&
    gates.paneVisible &&
    gates.workspaceRunning &&
    !gates.saving
  );
}

export interface ExternalChangeProbeOptions {
  /** Canonical display path of the probed file; null disables the probe
   *  (not loaded, not `editable:true`, image, …). */
  path: string | null;
  /** This pane is the active one. Turning active fires an immediate probe. */
  paneActive: boolean;
  /** The pane element is actually on screen — a phone keeps background
   *  columns mounted but display:none. */
  isPaneVisible(): boolean;
  /** A PUT is in flight; the probe stays quiet until its 200/409 settles. */
  isSaving(): boolean;
  /** Delivered for every non-silent observation. `unavailable` results are
   *  swallowed here (§7.5: probe failures are silent and simply retried). */
  onResult(result: FileProbeResult): void;
}

export function useExternalChangeProbe(options: ExternalChangeProbeOptions): void {
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const inFlightRef = useRef(false);
  const triggerRef = useRef<() => void>(() => {});

  useEffect(() => {
    const path = options.path;
    if (!path) {
      triggerRef.current = () => {};
      return;
    }
    let disposed = false;
    const probe = async () => {
      const opts = optionsRef.current;
      if (disposed || opts.path !== path || inFlightRef.current) return;
      if (
        !shouldProbe({
          path,
          documentVisible: !document.hidden,
          paneVisible: opts.isPaneVisible(),
          workspaceRunning: wsRunning(useWorkspaceStore.getState().state),
          saving: opts.isSaving(),
        })
      ) return;
      inFlightRef.current = true;
      try {
        const result = await probeFileMeta(path);
        const now = optionsRef.current;
        if (disposed || now.path !== path) return;
        // A save that started while the probe was in flight settles the base
        // through its own 200 — the probe must not pre-empt it (§7.2).
        if (now.isSaving()) return;
        if (result.kind === "unavailable") return;
        now.onResult(result);
      } finally {
        inFlightRef.current = false;
      }
    };
    triggerRef.current = () => void probe();
    // Returning to the tab is the moment an external change is most likely to
    // have landed unseen — the immediate triggers lead, the interval backs up.
    const onVisibility = () => {
      if (!document.hidden) void probe();
    };
    const interval = window.setInterval(() => void probe(), EXTERNAL_PROBE_INTERVAL_MS);
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("focus", onVisibility);
    return () => {
      disposed = true;
      triggerRef.current = () => {};
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("focus", onVisibility);
    };
  }, [options.path]);

  const wasActiveRef = useRef(options.paneActive);
  useEffect(() => {
    const was = wasActiveRef.current;
    wasActiveRef.current = options.paneActive;
    if (options.paneActive && !was) triggerRef.current();
  }, [options.paneActive]);
}
