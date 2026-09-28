// Transient-notification stack replacing the blocking native window.alert():
//   const toast = useToast();
//   toast("clone failed: …");                // error: auto-dismiss + kept in the notification center
//   toast("Saved", { kind: "success" });     // auto-dismisses, not kept
//   toast("Deleted", { kind: "success", persist: true }); // kept in the notification center
// Errors (role=alert) and any { persist:true } toast are logged to the notification center
// via toastLog so they can be reviewed after leaving the screen; trivial toasts (a copy
// confirmation and the like) stay purely ephemeral.
// On a phone the stack sits below the top bars instead of at the bottom (toastPlacement.ts), and a
// tap anywhere outside it dismisses the timed toasts.
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./Icon.tsx";
import { useT } from "../lib/i18n/index.ts";
import { useIsMobile } from "../lib/device.ts";
import { pushToastLog } from "../lib/toastLog.ts";
import { registerToastSink } from "./toast.ts";
import { measureChromeBottom, touchesChrome } from "./toastPlacement.ts";

export type ToastKind = "error" | "warn" | "info" | "success";

export interface ToastOptions {
  kind?: ToastKind;
  duration?: number; // ms; 0 = sticky (manual close only)
  persist?: boolean; // also keep in the notification center after dismiss (default: errors)
  // key names a toast so its owner can replace it in place or withdraw it (dismissToast)
  // once what it announced is settled; a second toast with the same key replaces the first.
  key?: string;
  // onClose runs when the member closes the toast with its X, not when it is withdrawn.
  onClose?: () => void;
}

interface ToastItem {
  id: number;
  message: ReactNode;
  kind: ToastKind;
  key?: string;
  onClose?: () => void;
  // A sticky toast carries an action (update now, finish a login) and closes only by its X.
  sticky: boolean;
}

type ToastFn = (message: ReactNode, opts?: ToastOptions) => void;

const ToastCtx = createContext<ToastFn | null>(null);

export function useToast(): ToastFn {
  const fn = useContext(ToastCtx);
  if (!fn) throw new Error("useToast must be used within <ToastProvider>");
  return fn;
}

export const TOAST_ICONS: Record<ToastKind, string> = {
  error: "error",
  warn: "warning",
  info: "info",
  success: "pass",
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const tr = useT();
  const [items, setItems] = useState<ToastItem[]>([]);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  const seq = useRef(0);
  // Each shown toast's expiry, by id. A keyed replacement keeps its id, so its old timer has
  // to be cancelled here — left running, it removed the replacement at the FIRST one's deadline.
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());
  // The id each key is shown under, known synchronously (setItems' updater runs later).
  const keyIds = useRef(new Map<string, number>());

  const remove = useCallback((id: number) => {
    const t = timers.current.get(id);
    if (t) clearTimeout(t);
    timers.current.delete(id);
    for (const [k, v] of keyIds.current) if (v === id) keyIds.current.delete(k);
    setItems((xs) => xs.filter((x) => x.id !== id));
  }, []);

  const toast = useCallback<ToastFn>(
    (message, opts) => {
      const kind = opts?.kind ?? "error";
      // A keyed toast already on screen keeps its id, which is the React key: a new one would
      // remount it, dropping focus and a press in progress, and re-announcing it to a screen
      // reader.
      const shown = opts?.key ? keyIds.current.get(opts.key) : undefined;
      const id = shown ?? ++seq.current;
      if (opts?.key) keyIds.current.set(opts.key, id);
      // Errors auto-dismiss rather than sticking (0); the notification center keeps them.
      const duration = opts?.duration ?? (kind === "error" ? 8000 : 4000);
      const item: ToastItem = { id, message, kind, key: opts?.key, onClose: opts?.onClose, sticky: duration <= 0 };
      setItems((xs) => {
        const at = xs.findIndex((x) => x.id === id);
        if (at < 0) return [...xs, item];
        const next = xs.slice();
        next[at] = item;
        return next;
      });
      // Errors (and any opt-in persist) are recorded in the notification center so a failure
      // that scrolled away can still be reviewed. Only string messages can be logged.
      if ((opts?.persist ?? kind === "error") && typeof message === "string") pushToastLog(kind, message);
      const old = timers.current.get(id);
      if (old) clearTimeout(old);
      timers.current.delete(id);
      if (duration > 0) timers.current.set(id, setTimeout(() => remove(id), duration));
    },
    [remove],
  );

  const dismiss = useCallback(
    (key: string) => {
      const id = keyIds.current.get(key);
      if (id != null) remove(id);
    },
    [remove],
  );

  // Expose this provider's toast to non-React callers (keyboard commands) while mounted.
  useEffect(() => {
    registerToastSink(toast, dismiss);
    return () => registerToastSink(null, null);
  }, [toast, dismiss]);

  const phone = useIsMobile();
  const shown = items.length > 0;
  const [top, setTop] = useState<number | null>(null);
  // Measured before paint, so a toast never flashes at the bottom first. Re-measured when the
  // stack changes, when the viewport moves (a soft keyboard shifts the frame, app/viewport.ts),
  // and when the page under the stack changes while it is up: a sticky toast outlives a switch
  // of pane or studio tab, which brings in or takes away a header (measured: the studio's chat
  // tab adds one at 213-249px that a stack placed on the form tab covered). Only mutations that
  // touch a bar count (touchesChrome), coalesced to one measurement per frame; an unchanged top
  // does not re-render.
  useLayoutEffect(() => {
    if (!phone || !shown) return;
    const sync = () => setTop(measureChromeBottom());
    sync();
    let frame = 0;
    const later = () => {
      if (!frame) frame = requestAnimationFrame(() => ((frame = 0), sync()));
    };
    const mo = new MutationObserver((records) => {
      if (touchesChrome(records)) later();
    });
    mo.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["class", "style", "hidden", "open"] });
    const vv = window.visualViewport;
    window.addEventListener("resize", sync);
    vv?.addEventListener("resize", sync);
    vv?.addEventListener("scroll", sync);
    return () => {
      mo.disconnect();
      if (frame) cancelAnimationFrame(frame);
      window.removeEventListener("resize", sync);
      vv?.removeEventListener("resize", sync);
      vv?.removeEventListener("scroll", sync);
    };
  }, [phone, shown, items]);

  // A tap outside the stack dismisses the timed toasts, so one covering the content can be put
  // away without aiming at its X. The tap itself still goes through to what it landed on. Like an
  // expiry, this does not run onClose.
  const stackRef = useRef<HTMLDivElement>(null);
  const dismissible = items.some((t) => !t.sticky);
  useEffect(() => {
    if (!phone || !dismissible) return;
    const onDown = (e: PointerEvent) => {
      if (e.target instanceof Node && stackRef.current?.contains(e.target)) return;
      for (const t of itemsRef.current) if (!t.sticky) remove(t.id);
    };
    document.addEventListener("pointerdown", onDown, true);
    return () => document.removeEventListener("pointerdown", onDown, true);
  }, [phone, dismissible, remove]);

  return (
    <ToastCtx.Provider value={toast}>
      {children}
      {shown && (
        <div
          ref={stackRef}
          className={"ui-toasts" + (phone ? " ui-toasts-phone" : "")}
          style={phone && top != null ? { top: top + 8 } : undefined}
        >
          {items.map((t) => (
            <div
              key={t.id}
              className={"ui-toast ui-toast-" + t.kind}
              role={t.kind === "error" ? "alert" : "status"}
              aria-live={t.kind === "error" ? "assertive" : "polite"}
            >
              <Icon name={TOAST_ICONS[t.kind]} />
              <span className="ui-toast-msg">{t.message}</span>
              <button
                type="button"
                className="ui-toast-x"
                title={tr("ui.close")}
                onClick={() => {
                  remove(t.id);
                  t.onClose?.();
                }}
              >
                <Icon name="close" />
              </button>
            </div>
          ))}
        </div>
      )}
    </ToastCtx.Provider>
  );
}
