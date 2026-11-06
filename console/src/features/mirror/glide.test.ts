import { describe, expect, it } from "vitest";
import { createGlider, glideAllowed, glideStep, GLIDE_MAX_SPEED } from "./glide.ts";

const FRAME = 1 / 60;

function run(from: number, to: number, maxDist = 1e9) {
  let s = { pos: from, v: 0 };
  const speeds: number[] = [];
  for (let i = 0; i < 600; i++) {
    const n = glideStep(s, to, FRAME, maxDist);
    speeds.push(Math.abs(n.v));
    if (n.done) return { frames: i + 1, pos: n.pos, speeds };
    s = n;
  }
  throw new Error("did not land");
}

describe("glideStep", () => {
  it("accelerates, then brakes onto the target exactly", () => {
    const r = run(0, 800);
    expect(r.pos).toBe(800);
    const peak = r.speeds.indexOf(Math.max(...r.speeds));
    // Speed rises frame by frame up to the peak, then falls.
    for (let i = 1; i <= peak; i++) expect(r.speeds[i]).toBeGreaterThanOrEqual(r.speeds[i - 1]);
    for (let i = peak + 1; i < r.speeds.length - 1; i++) expect(r.speeds[i]).toBeLessThanOrEqual(r.speeds[i - 1]);
    // Snappy: 800px well under half a second plus a frame or two.
    expect(r.frames * FRAME).toBeLessThan(0.55);
  });

  it("goes upward too and never overshoots", () => {
    let s = { pos: 1000, v: 0 };
    for (let i = 0; i < 600; i++) {
      const n = glideStep(s, 200, FRAME, 1e9);
      expect(n.pos).toBeGreaterThanOrEqual(200);
      if (n.done) return expect(n.pos).toBe(200);
      s = n;
    }
    throw new Error("did not land");
  });

  it("caps the speed", () => {
    const r = run(0, 20000);
    expect(Math.max(...r.speeds)).toBeLessThanOrEqual(GLIDE_MAX_SPEED);
  });

  it("jumps a long distance down to maxDist before gliding", () => {
    const n = glideStep({ pos: 0, v: 0 }, 10000, FRAME, 900);
    expect(n.pos).toBeGreaterThanOrEqual(9100);
    expect(n.pos).toBeLessThan(9200);
  });

  it("brakes and turns when the target jumps behind it", () => {
    const n = glideStep({ pos: 500, v: 3000 }, 0, FRAME, 1e9);
    expect(n.v).toBeGreaterThan(0); // still moving the old way, slower
    expect(n.v).toBeLessThan(3000);
    expect(n.pos).toBeGreaterThan(500);
  });
});

function harness() {
  let top = 0;
  let cb: ((t: number) => void) | null = null;
  let now = 0;
  const writes: number[] = [];
  const g = createGlider(
    () => top,
    (v) => {
      top = v;
      writes.push(v);
    },
    () => 600,
    (f) => {
      cb = f;
      return 1;
    },
    () => {
      cb = null;
    },
  );
  const frame = () => {
    const f = cb;
    cb = null;
    now += 16;
    f?.(now);
  };
  return { g, frame, writes, get top() { return top; }, set top(v: number) { top = v; } };
}

describe("createGlider", () => {
  it("chases a target that keeps moving and lands on its final value", () => {
    const h = harness();
    let end = 400;
    h.g.to(() => end, true);
    expect(h.g.toBottom()).toBe(true);
    for (let i = 0; i < 10; i++) h.frame();
    end = 900;
    for (let i = 0; i < 200 && h.g.active(); i++) h.frame();
    expect(h.g.active()).toBe(false);
    expect(h.top).toBe(900);
  });

  it("adopts a position moved under it instead of fighting it", () => {
    const h = harness();
    h.g.to(() => 1000, false);
    h.frame();
    h.frame();
    h.top = 700; // the browser clamped, or a band above moved the content
    h.frame();
    expect(h.writes.at(-1)).toBeGreaterThan(700);
    expect(h.writes.at(-1)).toBeLessThan(800);
  });

  it("stops where it is on cancel, and on a target that goes away", () => {
    const h = harness();
    h.g.to(() => 1000, false);
    h.frame();
    h.g.cancel();
    const at = h.top;
    h.frame();
    expect(h.top).toBe(at);
    expect(h.g.active()).toBe(false);

    let gone = false;
    h.g.to(() => (gone ? null : 1000), false);
    h.frame();
    gone = true;
    h.frame();
    expect(h.g.active()).toBe(false);
  });
});

describe("glideAllowed", () => {
  it("is off when the setting is off", () => {
    expect(glideAllowed(false)).toBe(false);
  });
});
