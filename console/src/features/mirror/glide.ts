// mirror/glide — the mirror's smooth automatic scroll (#3418).
//
// Native scrollTo({behavior: "smooth"}) is not usable for the mirror. Its intermediate frames
// raise scroll events below the offset useMirrorScroll last wrote, which onBodyScroll reads as the
// reader scrolling up (follow drops mid-glide), and its destination cannot be moved while the tail
// keeps growing under a streaming reply. This glider writes every frame itself (so each one goes
// through noteSelfScroll) and re-reads its target every frame.
//
// The motion is velocity based: accelerate toward the target, cap the speed, and brake along the
// curve that stops exactly on it. A target that moves while we travel is simply chased, and a
// reversal (a send while gliding up to an answer) brakes and turns instead of snapping.

/** Acceleration while speeding up (px/s²). Tuned "snappy": ~0.2 s for 200 px, ~0.45 s for 800 px. */
export const GLIDE_ACCEL = 20000;
/** Deceleration when braking onto the target (px/s²). Lower than the acceleration so it lands softly. */
export const GLIDE_BRAKE = 14000;
/** Speed cap (px/s). */
export const GLIDE_MAX_SPEED = 6000;
/** Never travel more than this many viewports: farther, we jump to this distance and glide the rest,
 *  so a long jump (or a tail that raced ahead while the tab was hidden) does not scroll for seconds. */
export const GLIDE_MAX_VIEWPORTS = 1.5;
/** A frame longer than this (a busy main thread, a tab coming back) is integrated as this long, so
 *  one late frame cannot fling the position past the target. */
const MAX_DT = 0.05;

export interface GlideState {
  pos: number;
  v: number;
}

/** One integration step toward target. done means the position is on the target and at rest. */
export function glideStep(s: GlideState, target: number, dt: number, maxDist: number): GlideState & { done: boolean } {
  let pos = s.pos;
  let d = target - pos;
  if (Math.abs(d) > maxDist) {
    pos = target - Math.sign(d) * maxDist;
    d = target - pos;
  }
  if (Math.abs(d) < 0.5) return { pos: target, v: 0, done: true };
  const dir = Math.sign(d);
  // The fastest speed from which braking still stops on the target.
  const want = dir * Math.min(GLIDE_MAX_SPEED, Math.sqrt(2 * GLIDE_BRAKE * Math.abs(d)));
  let v = s.v;
  if (v * dir < 0) {
    // Still moving away (the target jumped behind us): brake to a stop before turning.
    const speed = Math.abs(v) - GLIDE_BRAKE * dt;
    v = speed > 0 ? -dir * speed : 0;
  } else if (Math.abs(v) < Math.abs(want)) {
    v = dir * Math.min(Math.abs(want), Math.abs(v) + GLIDE_ACCEL * dt);
  } else {
    v = want;
  }
  const next = pos + v * dt;
  // Crossing the target is landing on it.
  if (Math.sign(target - next) !== dir) return { pos: target, v: 0, done: true };
  return { pos: next, v, done: false };
}

/** Whether a glide should be animated at all, asked at the start of each one: the setting, the
 *  OS's reduced-motion preference, and a visible document (a hidden one gets no frames). */
export function glideAllowed(enabled: boolean): boolean {
  if (!enabled) return false;
  if (typeof requestAnimationFrame !== "function") return false;
  if (typeof document !== "undefined" && document.hidden) return false;
  if (typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return false;
  return true;
}

export interface Glider {
  /** Glide toward target (re-read every frame; null ends the glide where it is). Retargets a glide
   *  in flight, keeping its velocity. bottom marks a glide to the end of the transcript. */
  to(target: () => number | null, bottom: boolean): void;
  /** Stop where it is. */
  cancel(): void;
  /** A glide is in flight. */
  active(): boolean;
  /** A glide to the end of the transcript is in flight. */
  toBottom(): boolean;
}

/**
 * The frame loop around glideStep. read() is the element's current scrollTop and write() puts one
 * there (the caller routes it through noteSelfScroll). A position that moved since our last write
 * — the browser clamping to a shorter body, scroll anchoring, a band-above adjustment — is
 * adopted as the new starting point rather than fought.
 */
export function createGlider(
  read: () => number,
  write: (top: number) => void,
  viewport: () => number,
  raf: (cb: (t: number) => void) => number = (cb) => requestAnimationFrame(cb),
  caf: (id: number) => void = (id) => cancelAnimationFrame(id),
): Glider {
  let frame = 0;
  let target: (() => number | null) | null = null;
  let bottom = false;
  let state: GlideState = { pos: 0, v: 0 };
  let written = 0;
  let last = 0;

  const stop = () => {
    if (frame) caf(frame);
    frame = 0;
    target = null;
    bottom = false;
    state = { pos: 0, v: 0 };
  };

  const tick = (now: number) => {
    frame = 0;
    if (!target) return;
    const t = target();
    if (t === null) return stop();
    const dt = last ? Math.min(MAX_DT, Math.max(0, (now - last) / 1000)) : 1 / 60;
    last = now;
    const cur = read();
    if (Math.abs(cur - written) > 1) state = { pos: cur, v: state.v };
    const next = glideStep(state, t, dt, Math.max(1, viewport() * GLIDE_MAX_VIEWPORTS));
    write(next.pos);
    written = read();
    if (next.done) return stop();
    state = { pos: next.pos, v: next.v };
    frame = raf(tick);
  };

  return {
    to(next, toBottom) {
      if (!target) {
        state = { pos: read(), v: 0 };
        written = state.pos;
        last = 0;
      }
      target = next;
      bottom = toBottom;
      if (!frame) frame = raf(tick);
    },
    cancel: stop,
    active: () => target !== null,
    toBottom: () => target !== null && bottom,
  };
}
