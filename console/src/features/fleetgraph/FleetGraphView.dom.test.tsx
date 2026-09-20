// Render test for the fleet session graph (ADR 0096). Scope: the fixture renders every
// lane presence the contract distinguishes, a known lane opens the session it names, and
// the archived toggle writes back through PaneContent (never React state — a tab switch
// would otherwise snap it back to the default, memo `sessions-overview-pane`).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Session = import("../../types/session.ts").Session;
const openSessionFromList = vi.fn((_s: Session, _split: boolean, _running: boolean) => true);
vi.mock("../sessions/open.ts", () => ({
  openSessionFromList: (s: Session, split: boolean, running: boolean) => openSessionFromList(s, split, running),
}));

const fetchFleetGraph = vi.fn((_since: number, _until: number) => Promise.resolve({ error: { code: "test" } }));
vi.mock("../../core/api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../core/api/client.ts")>();
  return { ...actual, fetchFleetGraph: (...args: [number, number]) => fetchFleetGraph(...args) };
});

const { FleetGraphView } = await import("./FleetGraphView.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const layoutOps = await import("../../layout/ops.ts");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const fxRoot1: Session = { name: "fx-root1", kind: "claude", alive: true, state: "working" } as Session;

const render = async (showArchived = true): Promise<void> => {
  await act(async () => {
    root!.render(<FleetGraphView paneId="p1" showArchived={showArchived} />);
  });
  // Flush the fetchFleetGraph microtask the mount effect kicks off.
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  openSessionFromList.mockClear();
  fetchFleetGraph.mockClear();
  useSessionsStore.setState({ sessions: [fxRoot1] });
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("FleetGraphView", () => {
  it("draws every lane presence the fixture carries", async () => {
    await render();
    const labels = [...host.querySelectorAll(".fgraph-label-text")].map((el) => el.textContent);
    expect(labels).toContain("fleet-graph kickoff"); // live
    expect(labels).toContain("S-VIEW lane"); // stopped
    expect(labels).toContain("S-LOGIC lane"); // archived
    expect(labels).toContain("throwaway GPU probe"); // gone (clean death)
    expect(labels).toContain("unwatched probe"); // gone (cut, hollow ×)
    // Erased lane: the bare slug is never shown alone — composed with the "deleted" wording.
    expect(labels).toContain(t("fgraph.erased_label", { id: "fx-erased1" }));
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-death").length).toBeGreaterThan(0);
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-arrow").length).toBeGreaterThan(0);
  });

  it("sizes the SVG in real pixels matching its own viewBox, not a CSS-scaled percentage", async () => {
    // Regression guard for the label/SVG row-drift bug review caught: the first version had
    // no width/height attributes at all (CSS `width:100%;height:auto` scaled the internal
    // geometry by containerWidth/viewBoxWidth while the label column's fixed 34px rows did
    // not scale with it) — `width` below would have been null under that version.
    await render();
    const svg = host.querySelector<SVGSVGElement>("svg.fgraph-svg")!;
    const width = svg.getAttribute("width");
    const height = svg.getAttribute("height");
    expect(width).toBeTruthy();
    expect(height).toBeTruthy();
    expect(svg.getAttribute("viewBox")).toBe(`0 0 ${width} ${height}`);
    // Same constant the SVG's row math (ROW_H) uses, so the two cannot silently diverge.
    expect(host.querySelector<HTMLElement>(".fgraph-label")!.style.height).toBe("34px");
  });

  it("marks a lane whose immediate parent has no row of its own (decision 9)", async () => {
    await render();
    const labelFor = (text: string) =>
      [...host.querySelectorAll<HTMLElement>(".fgraph-label")].find((el) => el.textContent?.includes(text));
    // fx-cut1's parent ("fx-ghost-parent") never gets a lane in the fixture.
    expect(labelFor("unwatched probe")?.querySelector(".fgraph-parent-gap")).toBeTruthy();
    // fx-child2's parent (fx-child1, "S-VIEW lane") IS drawn — no marker.
    expect(labelFor("S-LOGIC lane")?.querySelector(".fgraph-parent-gap")).toBeFalsy();
  });

  it("draws a hatched 'unknown' tail after a cut run, never the resumable dashed line", async () => {
    await render();
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-cut-unknown").length).toBe(1);
  });

  it("clicking a known lane opens the session it names", async () => {
    await render();
    const row = [...host.querySelectorAll<HTMLElement>(".fgraph-label")].find((el) => el.textContent?.includes("fleet-graph kickoff"));
    expect(row).toBeTruthy();
    await act(async () => row!.click());
    expect(openSessionFromList).toHaveBeenCalledTimes(1);
    expect(openSessionFromList.mock.calls[0][0]).toEqual(fxRoot1);
  });

  it("hides archived lanes when the toggle turns them off, without touching React state", async () => {
    await render(false);
    const labels = [...host.querySelectorAll(".fgraph-label-text")].map((el) => el.textContent);
    expect(labels).not.toContain("S-LOGIC lane"); // archived, hidden
    expect(labels).toContain("fleet-graph kickoff"); // everything else stays
  });

  it("the archived toggle writes PaneContent, not local state", async () => {
    await render(true);
    // The store's action reads layout/ops.ts's pure function fresh at call time (`import *
    // as ops`), so spying there catches the click regardless of when the component captured
    // its own setPaneTarget reference via the hook selector.
    const spy = vi.spyOn(layoutOps, "setPaneTarget");
    const toggle = host.querySelector<HTMLButtonElement>(".fgraph-toggle")!;
    await act(async () => {
      toggle.click();
    });
    expect(spy).toHaveBeenCalledWith(expect.anything(), "p1", { content: { kind: "fleetgraph", showArchived: false } });
    spy.mockRestore();
  });
});
