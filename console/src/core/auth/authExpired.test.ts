import { describe, it, expect, vi } from "vitest";
import { signalAuthExpired, subscribeAuthExpired, isAuthExpired } from "./authExpired.ts";

// The latch is module-global and one-way (it mirrors a real page's "your login
// expired" state, which only ends on navigation), so these run in order against the
// single instance: pre-latch behavior first, then the flip, then post-latch.
describe("authExpired latch", () => {
  it("starts un-latched", () => {
    expect(isAuthExpired()).toBe(false);
  });

  it("notifies existing subscribers once on the first signal, and latches", () => {
    const a = vi.fn();
    const b = vi.fn();
    subscribeAuthExpired(a);
    subscribeAuthExpired(b);
    signalAuthExpired();
    signalAuthExpired(); // idempotent — must not double-fire
    expect(isAuthExpired()).toBe(true);
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
  });

  it("fires immediately for a subscriber added after the latch flipped", () => {
    const late = vi.fn();
    subscribeAuthExpired(late);
    expect(late).toHaveBeenCalledTimes(1);
  });

  it("a throwing listener does not block the others", () => {
    const boom = () => {
      throw new Error("nope");
    };
    const ok = vi.fn();
    // Already latched, so both run immediately on subscribe; the throw is swallowed.
    expect(() => subscribeAuthExpired(boom)).not.toThrow();
    expect(() => subscribeAuthExpired(ok)).not.toThrow();
    expect(ok).toHaveBeenCalledTimes(1);
  });
});
