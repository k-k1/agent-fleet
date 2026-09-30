import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { advance, caughtUp, retarget, revealed, snapped, TYPEWRITER_IDLE, type Typewriter } from "./typewriter.ts";

/** Frames are throttled to this. Every frame re-parses the Markdown, and at the 400 chars/s cap a
 *  60 Hz frame would show fewer than seven new characters — 30 Hz reads the same and halves the
 *  work. */
export const TYPEWRITER_TICK_MS = 33;

/** useTypewriter returns the text to show for `text`, typed out per `typewriter.ts` while
 *  `enabled`; otherwise `text` itself. `host` is the element whose visibility gates the animation:
 *  an off-screen block, like a background tab (where requestAnimationFrame does not fire),
 *  shows its text whole rather than spending frames nobody sees. The loop runs only while there
 *  is backlog, so a caught-up block costs no frames at all. */
export function useTypewriter(text: string, enabled: boolean, host: RefObject<HTMLElement | null>): string {
  const state = useRef<Typewriter | null>(null);
  if (state.current === null) state.current = enabled ? retarget(TYPEWRITER_IDLE, text) : snapped(text);
  const visible = useRef(true);
  const raf = useRef(0);
  const [shown, setShown] = useState(() => revealed(state.current!));

  const stop = useCallback(() => {
    if (raf.current) cancelAnimationFrame(raf.current);
    raf.current = 0;
  }, []);

  const apply = useCallback((next: Typewriter) => {
    if (next === state.current) return;
    state.current = next;
    setShown(revealed(next));
  }, []);

  const run = useCallback(() => {
    if (raf.current || caughtUp(state.current!)) return;
    let last: number | null = null;
    const tick = (now: number) => {
      raf.current = 0;
      // The first frame only takes the clock; dt is measured between frames, not from whenever
      // the loop was started, so a target that lands mid-frame does not skip ahead.
      if (last !== null && now - last >= TYPEWRITER_TICK_MS) {
        apply(advance(state.current!, now - last));
        last = now;
      } else if (last === null) {
        last = now;
      }
      if (!caughtUp(state.current!)) raf.current = requestAnimationFrame(tick);
    };
    raf.current = requestAnimationFrame(tick);
  }, [apply]);

  useEffect(() => {
    if (!enabled) return;
    apply(visible.current ? retarget(state.current!, text) : snapped(text));
    run();
    return stop;
  }, [text, enabled, apply, run, stop]);

  useEffect(() => {
    const el = host.current;
    if (!enabled || !el || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((entries) => {
      const v = entries[entries.length - 1]?.isIntersecting ?? true;
      visible.current = v;
      if (!v && !caughtUp(state.current!)) {
        stop();
        apply(snapped(state.current!.target));
      }
    });
    io.observe(el);
    return () => io.disconnect();
  }, [enabled, host, apply, stop]);

  return enabled ? shown : text;
}
