// useLiveHold's own clock: an unchanged poll does not re-render the mirror, so the hold has to
// expire on a timer; and a held text never crosses into another session.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { LIVE_HOLD_MS, useLiveHold } from "./liveHold.ts";
import type { Turn } from "./transcript/types.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let shown = "";

function Probe({ raw, turns, scope }: { raw: string; turns: Turn[]; scope: string }) {
  shown = useLiveHold(raw, turns, scope);
  return null;
}

const render = (raw: string, turns: Turn[], scope = "s1") =>
  act(() => {
    root!.render(<Probe raw={raw} turns={turns} scope={scope} />);
  });

beforeEach(() => {
  vi.useFakeTimers();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
});

const turns: Turn[] = [{ role: "assistant", parts: [{ kind: "text", text: "Reading." }] } as Turn];

describe("useLiveHold", () => {
  it("holds a dropped text, then expires it with no further render", () => {
    render("All done.", turns);
    render("", turns);
    expect(shown).toBe("All done.");
    act(() => {
      vi.advanceTimersByTime(LIVE_HOLD_MS - 10);
    });
    expect(shown).toBe("All done.");
    act(() => {
      vi.advanceTimersByTime(20);
    });
    expect(shown).toBe("");
  });

  it("drops a held text when the session changes", () => {
    render("All done.", turns);
    render("", turns);
    render("", turns, "s2");
    expect(shown).toBe("");
  });
});
