// Render test for the fleet session graph (ADR 0096). It drives the real pipeline —
// a served ledger page plus the live sessions map, through buildFleetGraph, into the DOM —
// because the two halves disagreeing is exactly the class of bug this feature kept hitting
// (a lane's presence needs both, and the ledger alone or the map alone each look fine).
//
// Scope: every lane presence the contract distinguishes renders, a known lane opens the
// session it names, and the archived toggle writes back through PaneContent (never React
// state — a tab switch would otherwise snap it back to the default, memo
// `sessions-overview-pane`).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Session = import("../../types/session.ts").Session;
type FleetGraphPage = import("../../types/fleetgraph.ts").FleetGraphPage;

const openSessionFromList = vi.fn((_s: Session, _split: boolean, _running: boolean) => true);
vi.mock("../sessions/open.ts", () => ({
  openSessionFromList: (s: Session, split: boolean, running: boolean) => openSessionFromList(s, split, running),
}));

let served: FleetGraphPage | { error: { code: string } } = { error: { code: "test" } };
const fetchFleetGraph = vi.fn((_since: number, _until: number) => Promise.resolve(served));
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

const MIN = 60_000;
// Anchored inside the view's default window (now − 24h .. now) so the figure is not empty.
const NOW = Date.now();
const at = (minutesAgo: number): number => NOW - minutesAgo * MIN;

// fx-root1 is live and fx-child1 is stopped-but-listed: presence needs the live map, so
// both have to be in the sessions store, and the archived lane deliberately is NOT —
// the Agent's list skips archived rows (`if m.Archived { continue }`), which is what made
// the first implementation render every archived lane as "gone".
const sessRoot: Session = { name: "fx-root1", kind: "claude", alive: true, state: "working" } as Session;
const sessChild1: Session = { name: "fx-child1", kind: "codex", alive: false } as Session;

const page = (): FleetGraphPage => ({
  since: at(24 * 60),
  until: NOW,
  now: NOW,
  lineage: [
    { ev: "birth", ts: at(600), name: "fx-root1", kind: "claude", origin: "user", display: "fleet-graph kickoff" },
    { ev: "birth", ts: at(540), name: "fx-child1", kind: "codex", origin: "session", originSession: "fx-root1", display: "S-VIEW lane" },
    { ev: "death", ts: at(300), name: "fx-child1" },
    { ev: "birth", ts: at(520), name: "fx-child2", kind: "opencode", origin: "session", originSession: "fx-child1", display: "S-LOGIC lane" },
    { ev: "death", ts: at(280), name: "fx-child2" },
    { ev: "archived", ts: at(270), name: "fx-child2", archived: true },
    { ev: "birth", ts: at(200), name: "fx-gone1", kind: "claude", origin: "user", display: "throwaway GPU probe" },
    { ev: "death", ts: at(120), name: "fx-gone1", reason: "oom", code: 137 },
    // No birth for fx-ghost-parent anywhere: its child keeps the lineage (decision 9's
    // "the chain stops at the unreachable parent"), and its run never got a death.
    { ev: "birth", ts: at(180), name: "fx-cut1", kind: "claude", origin: "session", originSession: "fx-ghost-parent", display: "unwatched probe" },
  ],
  activity: [
    { ev: "state", ts: at(590), name: "fx-root1", to: "working" },
    { ev: "instruct", ts: at(595), from: "user", to: "fx-root1", source: "", excerpt: "kick it off" },
    { ev: "report", ts: at(305), from: "fx-child1", to: "conv:c1", kind: "answer-ready" },
    // The only trace left of a session someone deleted: its lineage is gone, the activity
    // line naming it is not (decision 6), so it must still hold a row for this arrow.
    { ev: "peer", ts: at(240), from: "fx-child1", to: "fx-erased1", intent: "request" },
  ],
  coverage: { activitySince: at(24 * 60), lineageSince: at(24 * 60) },
});

const render = async (showArchived = true, collapsed: string[] = []): Promise<void> => {
  await act(async () => {
    root!.render(<FleetGraphView paneId="p1" showArchived={showArchived} collapsed={collapsed} />);
  });
  // Flush the fetchFleetGraph microtask the mount effect kicks off.
  await act(async () => {
    await Promise.resolve();
  });
};

const labelTexts = (): (string | null)[] => [...host.querySelectorAll(".fgraph-label-text")].map((el) => el.textContent);

beforeEach(() => {
  openSessionFromList.mockClear();
  fetchFleetGraph.mockClear();
  served = page();
  useSessionsStore.setState({ sessions: [sessRoot, sessChild1] });
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
  it("draws every lane presence the contract distinguishes", async () => {
    await render();
    const labels = labelTexts();
    expect(labels).toContain("fleet-graph kickoff"); // live (in the sessions map, alive)
    expect(labels).toContain("S-VIEW lane"); // stopped (died, still listed)
    expect(labels).toContain("S-LOGIC lane"); // archived (died, NOT listed — the list skips archived)
    expect(labels).toContain("throwaway GPU probe"); // gone (clean death, not listed)
    expect(labels).toContain("unwatched probe"); // gone (run never closed → cut, hollow ×)
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
    // fx-cut1's parent ("fx-ghost-parent") has no birth anywhere, so it never gets a lane.
    expect(labelFor("unwatched probe")?.querySelector(".fgraph-parent-gap")).toBeTruthy();
    // fx-child2's parent (fx-child1, "S-VIEW lane") IS drawn — no marker.
    expect(labelFor("S-LOGIC lane")?.querySelector(".fgraph-parent-gap")).toBeFalsy();
  });

  it("draws a hatched 'unknown' tail after a cut run, never the resumable dashed line", async () => {
    await render();
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-cut-unknown").length).toBe(1);
  });

  it("never draws a family arrow's missing-parent end as an external-actor edge glyph", async () => {
    // A family edge's ends are ALWAYS sessions, so a null row there means decision 9's
    // "missing parent" — drawing the top-edge glyph would misattribute the launch to a
    // conversation or a person, and would duplicate the mark the child's label carries.
    await render();
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-arrow.family .fgraph-edge-pt").length).toBe(0);
    // Round-trip arrows (instruct/report/peer) still use the edge glyph: the instruct comes
    // from `user` and the report goes to `conv:c1`, neither of which ever has a lane.
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-edge-pt").length).toBeGreaterThan(0);
  });

  it("the NAME opens the session — the label column is the way in (ADR 0096 decision 17)", async () => {
    await render();
    const row = [...host.querySelectorAll<HTMLElement>(".fgraph-label")].find((el) => el.textContent?.includes("fleet-graph kickoff"));
    expect(row).toBeTruthy();
    await act(async () => row!.click());
    expect(openSessionFromList).toHaveBeenCalledTimes(1);
    expect(openSessionFromList.mock.calls[0][0]).toEqual(sessRoot);
  });

  it("clicking the LANE in the figure opens nothing — the canvas is a surface you grab", async () => {
    await render();
    const lanes = [...host.querySelectorAll<SVGGElement>("svg.fgraph-svg .fgraph-lane-hit")];
    expect(lanes.length).toBeGreaterThan(0); // the rows are drawn; it is the click that is gone
    await act(async () => {
      for (const g of lanes) g.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      // The wide invisible band is what a finger actually lands on, so press that too.
      for (const hit of host.querySelectorAll("svg.fgraph-svg .fgraph-hit")) {
        hit.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      }
    });
    expect(openSessionFromList).not.toHaveBeenCalled();
  });

  it("an archived lane draws nothing past its × — no dashed tail, no grey band", async () => {
    await render();
    // fx-child2 is the archived lane in the fixture (death, then archived:true).
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-tail.archived")).toHaveLength(0);
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-seg.archived")).toHaveLength(0);
    // A STOPPED lane keeps its dashed tail: the amendment is about archived alone.
    expect(host.querySelectorAll("svg.fgraph-svg .fgraph-tail.stopped").length).toBeGreaterThan(0);
  });

  it("the activity band is coloured by STATE, with no per-kind colour written onto it", async () => {
    await render();
    const bands = [...host.querySelectorAll<SVGRectElement>("svg.fgraph-svg .fgraph-seg")];
    expect(bands.length).toBeGreaterThan(0);
    // The kind colour used to arrive as an inline `--seg-color` custom property on the
    // "active" band. Its absence is what says the band now reads from the state palette
    // (.fgraph-seg.active → --accent), the same one the row's chip uses.
    for (const b of bands) expect(b.getAttribute("style")).toBeNull();
  });

  it("hides archived lanes when the toggle turns them off, without touching React state", async () => {
    await render(false);
    const labels = labelTexts();
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
    expect(spy).toHaveBeenCalledWith(expect.anything(), "p1", {
      content: { kind: "fleetgraph", showArchived: false, collapsed: [] },
    });
    spy.mockRestore();
  });

  it("keeps the figure on screen when a refresh fails", async () => {
    // A failed load must not blank a figure that is already drawn: the window did not move,
    // so the history it showed is still the history.
    await render();
    expect(labelTexts()).toContain("fleet-graph kickoff");
    served = { error: { code: "boom" } };
    const zoomIn = [...host.querySelectorAll<HTMLButtonElement>(".fgraph-nav button")].find(
      (b) => b.title === t("fgraph.zoom_in"),
    );
    expect(zoomIn).toBeTruthy(); // the click below is the whole point of the test
    await act(async () => {
      zoomIn!.click();
    });
    // The refetch is debounced (FETCH_SETTLE_MS): a pan/zoom gesture moves the window on
    // every wheel notch and every pointer move, and the first version asked the server for
    // a page on each one. Nothing is requested until the gesture settles — which is also
    // why this test has to let the timer run.
    expect(fetchFleetGraph).toHaveBeenCalledTimes(1);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 300));
    });
    expect(fetchFleetGraph).toHaveBeenCalledTimes(2); // the window moved, so it really refetched
    expect(host.querySelector(".fgraph-err")).toBeTruthy();
    expect(labelTexts()).toContain("fleet-graph kickoff");
  });

  it("the fold takes the family's rows away, says how many, and writes PaneContent", async () => {
    await render();
    expect(labelTexts()).toContain("S-VIEW lane"); // fx-child1, under fx-root1
    const spy = vi.spyOn(layoutOps, "setPaneTarget");
    // The first label is fx-root1's row (family order puts a parent above its children).
    const fold = host.querySelector<HTMLButtonElement>(".fgraph-label .fgraph-fold")!;
    await act(async () => {
      fold.click();
    });
    expect(spy).toHaveBeenCalledWith(expect.anything(), "p1", {
      content: { kind: "fleetgraph", showArchived: true, collapsed: ["fx-root1"] },
    });
    spy.mockRestore();

    // The fold lives on PaneContent, so the pane re-renders with it — which is what the
    // figure actually draws from (React state would snap back on a tab switch).
    await render(true, ["fx-root1"]);
    const labels = labelTexts();
    expect(labels).toContain("fleet-graph kickoff"); // the parent stays
    expect(labels).not.toContain("S-VIEW lane"); // the child is gone
    expect(labels).not.toContain("S-LOGIC lane"); // and so is the grandchild
    expect(host.querySelector(".fgraph-hidden")?.textContent).toBe(t("fgraph.hidden_children", { n: 2 }));
  });

  it("the label column shows the live state through stateInfo, not a second derivation", async () => {
    await render();
    // fx-root1 is alive and working in the sessions store; the chip is the SAME component
    // the rail row and the overview card render (ADR 0078 decision 5 / 0096 decision 15).
    const chips = [...host.querySelectorAll(".fgraph-label .session-state")];
    expect(chips.length).toBeGreaterThan(0);
    expect(chips.some((c) => c.textContent?.includes(t("state.working")))).toBe(true);
    // A lane the live list does not carry (archived / deleted) says what the line style
    // already says, rather than guessing at a state nobody reported.
    expect(chips.some((c) => c.textContent?.includes(t("fgraph.presence_short_gone")))).toBe(true);
  });

  it("switching to the sessions overview swaps this pane, and Ctrl opens a new one", async () => {
    await render();
    const spy = vi.spyOn(layoutOps, "setPaneTarget");
    const btn = host.querySelector<HTMLButtonElement>(".fgraph-switch")!;
    await act(async () => {
      btn.click();
    });
    expect(spy).toHaveBeenCalledWith(expect.anything(), "p1", {
      content: { kind: "sessions", showStopped: false, collapsed: [] },
    });
    spy.mockRestore();
  });

  it("the time axis is a sticky strip above the rows, not the foot of the canvas", async () => {
    await render();
    const axis = host.querySelector(".fgraph-axis");
    expect(axis).toBeTruthy();
    expect(axis!.querySelectorAll("text.fgraph-axis-label").length).toBeGreaterThan(0);
    // The captions are NOT inside the scrolling canvas any more — that is the whole point:
    // past a screenful of lanes, a scale drawn at the bottom is only readable after
    // scrolling to it.
    expect(host.querySelectorAll("svg.fgraph-svg text.fgraph-axis-label")).toHaveLength(0);
    // The body is the scroll box and the strip is its first child, so `position: sticky`
    // has something to stick to.
    expect(host.querySelector(".fgraph-body")!.firstElementChild).toBe(axis);
  });
});
