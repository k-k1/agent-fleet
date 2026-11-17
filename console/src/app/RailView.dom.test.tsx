// One view of the left pane (ADR 0140): what a lazy view mounts, and what its content is told
// about being on screen. The Files view is lazy because its tree reads the Workspace's file
// system on mount, and a load that paid for it on every Console open stalled the terminal's
// WebSocket (#4426).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./WorkingSetBar.tsx", () => ({ WorkingSetBar: () => null }));

import { RailView } from "./RailView.tsx";
import type { RailView as RailViewId } from "../core/store/leftRail.ts";
import { useViewShown } from "../lib/viewShown.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement;

let mounts = 0;
function Probe() {
  const shown = useViewShown();
  useEffect(() => {
    mounts++;
  }, []);
  return <span className="probe" data-shown={String(shown)} />;
}

async function render(active: RailViewId, shown: boolean, lazy = true) {
  await act(async () => {
    root!.render(
      <RailView id="files" active={active} shown={shown} lazy={lazy}>
        <Probe />
      </RailView>,
    );
  });
}
const probe = () => host.querySelector<HTMLSpanElement>(".probe");

beforeEach(() => {
  mounts = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("RailView", () => {
  it("mounts a lazy view on its first show and keeps it mounted after", async () => {
    await render("sessions", true);
    expect(probe()).toBeNull();

    await render("files", true);
    expect(probe()?.dataset.shown).toBe("true");

    await render("sessions", true);
    expect(probe()?.dataset.shown).toBe("false"); // still mounted, told it is hidden
    expect(mounts).toBe(1);
  });

  it("does not count a view chosen behind a folded pane as shown", async () => {
    await render("files", false);
    expect(probe()).toBeNull();

    await render("files", true);
    expect(probe()?.dataset.shown).toBe("true");
    await render("files", false);
    expect(probe()?.dataset.shown).toBe("false");
  });

  it("mounts a view that is not lazy at once, hidden", async () => {
    await render("sessions", true, false);
    expect(probe()?.dataset.shown).toBe("false");
    expect(host.querySelector<HTMLDivElement>(".rail-view")?.hidden).toBe(true);
  });
});
