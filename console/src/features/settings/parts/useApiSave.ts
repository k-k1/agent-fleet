// useApiSave — the shared "write to the Control Plane" hook of the settings tabs, the
// counterpart of useApiLoad. It carries the three rules each tab used to repeat by hand:
//   • one write at a time: a second call while one is out is dropped (`busy`), so a double click
//     cannot send the form twice;
//   • `saving` goes false again on every outcome, a rejected request included;
//   • nothing is set after the component unmounted.
// The reply is not interpreted beyond success or failure: the caller decides what to do with
// the value (close the form, reload) and how to word the failure around `error`.
import { useCallback, useEffect, useRef, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk, type Reply } from "../api.ts";

export type SaveResult<T> =
  /** The write answered without an error. `value` is null when the body was JSON `null`. */
  | { status: "ok"; value: T | null }
  /** The write was refused or rejected; `error` is the user-facing text (errText). */
  | { status: "failed"; error: string }
  /** Another write of this hook was still out; nothing was sent. */
  | { status: "busy" };

export function useApiSave<A extends unknown[], T>(
  write: (...args: A) => Promise<Reply<T> | null>,
): { saving: boolean; save: (...args: A) => Promise<SaveResult<T>> } {
  const [saving, setSaving] = useState(false);
  const inFlight = useRef(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const writeRef = useRef(write);
  writeRef.current = write;

  const save = useCallback(async (...args: A): Promise<SaveResult<T>> => {
    if (inFlight.current) return { status: "busy" };
    inFlight.current = true;
    setSaving(true);
    try {
      const res = await writeRef.current(...args);
      if (res && !isOk(res)) return { status: "failed", error: errText(res.error) };
      return { status: "ok", value: res };
    } catch (e) {
      return { status: "failed", error: e instanceof Error ? e.message : String(e) };
    } finally {
      inFlight.current = false;
      if (live.current) setSaving(false);
    }
  }, []);
  return { saving, save };
}
