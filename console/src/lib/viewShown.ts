// Whether the view a component lives in is on screen. The left pane keeps a view mounted
// while another one is shown (ADR 0140), so a mounted component can be invisible; one that
// reads the Workspace's file system on its own must not do so while nobody can see it
// (each read is Agent I/O, and on ecs-ec2 part of the home is on EFS).
//
// The default is true: a component mounted outside a rail view (a pane, a test) is on
// screen whenever it is mounted.
import { createContext, useContext, useEffect, useRef } from "react";

export const ViewShownContext = createContext(true);

export function useViewShown(): boolean {
  return useContext(ViewShownContext);
}

/** `value` as of the last render in which the view was shown. Put it in a loader's deps in
 *  place of `value` and a change made while hidden waits for the next show. */
export function useShownValue<T>(value: T): T {
  const shown = useViewShown();
  const ref = useRef(value);
  if (shown) ref.current = value;
  return ref.current;
}

/** Calls `onShow` when the view comes back on screen, not on mount. */
export function useOnShow(onShow: () => void): void {
  const shown = useViewShown();
  const prev = useRef(shown);
  const cb = useRef(onShow);
  cb.current = onShow;
  useEffect(() => {
    const was = prev.current;
    prev.current = shown;
    if (shown && !was) cb.current();
  }, [shown]);
}
