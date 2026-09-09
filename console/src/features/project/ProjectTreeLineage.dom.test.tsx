// Render test for the rail's spawn-lineage layout (ADR 0073): a worktree nests under
// the working copy the session that made it was spawned from, and every copy of one
// family carries the same coloured 2px spine.
//
// The two things nesting CANNOT express are the reason the colour exists, so both are
// asserted here: a child started in another repository (its worktree hangs under that
// other base) and a folded ancestor (its descendants are not in the DOM at all).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

let served: Repo[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => {
    if (url.startsWith("api/repos")) return { repos: served };
    if (url.startsWith("api/connections")) return {};
    return {};
  }),
  isTransientErr: () => false,
}));

const { ProjectTree } = await import("./ProjectTree.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useProjectFilter } = await import("./filter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const at = (n: number) => `2026-09-0${n}T00:00:00Z`;
const wt = (name: string, parent: string, createdAt: string): Repo => ({ name, worktree: true, parent, createdAt });
// title = name so a row can be found by the text it actually shows (displayName).
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });

// A parent session in one worktree, its child in a second, and a grandchild whose
// worktree lives under a DIFFERENT base clone (create_session picks the repo).
const REPOS: Repo[] = [
  { name: "af", branch: "develop" },
  wt("af@p", "af", at(1)),
  wt("af@c", "af", at(2)),
  { name: "other", branch: "main" },
  wt("other@g", "other", at(3)),
];
const SESSIONS: Session[] = [
  sess("parent", { repo: "af@p", createdAt: at(1) }),
  sess("child", { repo: "af@c", createdAt: at(2), originSession: "parent" }),
  sess("grandkid", { repo: "other@g", createdAt: at(3), originSession: "child" }),
  sess("loner", { repo: "af", createdAt: at(4) }), // no family — no spine
];

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectTree />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

/** The folder a node's own header row names (children's rows are excluded by :scope). */
const nodeName = (li: Element) =>
  li.querySelector(":scope > .proj-node-head [data-rail-repo]")?.getAttribute("data-rail-repo") ?? "";

/** [folder, [children…]] for one node — a failure then prints the whole shape. */
const shape = (li: Element): unknown => [
  nodeName(li),
  [...li.querySelectorAll(":scope > ul.proj-children > li.proj-node")].map(shape),
];
const forest = () => [...host.querySelectorAll<HTMLLIElement>("ul.proj-tree > li.proj-node")].map(shape);

const nodeFor = (name: string) =>
  [...host.querySelectorAll<HTMLLIElement>("li.proj-node")].find((li) => nodeName(li) === name);
/** The declared spine colour, or "" when the copy belongs to no family. */
const spine = (name: string) => nodeFor(name)?.style.getPropertyValue("--proj-lineage").trim() ?? "";

const sessionRow = (name: string) =>
  [...host.querySelectorAll<HTMLLIElement>("li.sess-row")].find(
    (li) => li.querySelector(".sess-l1")?.textContent === name,
  );

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
  useProjectFilter.setState({ q: "" });
  served = REPOS;
  useReposStore.setState({ repos: REPOS });
  useSessionsStore.setState({ sessions: SESSIONS });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("nesting by spawn lineage", () => {
  it("hangs a worktree under the copy its owner's parent runs in, recursively", async () => {
    await render();
    expect(forest()).toEqual([
      // af@c holds the child, spawned from the session in af@p.
      ["af", [["af@p", [["af@c", []]]]]],
      // The grandchild was started in ANOTHER repository, so its worktree can only sit
      // under that base — the family survives in the colour, asserted below.
      ["other", [["other@g", []]]],
    ]);
  });

  it("paints one family one colour, in both repositories", async () => {
    await render();
    const c = spine("af@p");
    expect(c).toMatch(/^var\(--lineage-[0-5]\)$/);
    expect(spine("af@c")).toBe(c);
    expect(spine("other@g")).toBe(c); // the cross-repository half of the family
    expect(nodeFor("af@p")!.className).toContain("lineage");
  });

  it("still shows the family on a node whose descendants are folded away", async () => {
    await render();
    const caret = nodeFor("af@p")!.querySelector<HTMLButtonElement>(":scope > .proj-node-head .proj-node-caret")!;
    await act(async () => caret.click());
    expect(nodeFor("af@c")).toBeUndefined(); // the nesting is gone from the DOM entirely
    expect(nodeFor("af@p")!.className).toContain("lineage");
    expect(spine("af@p")).toBe(spine("other@g")); // …and the relation is still readable
  });

  it("marks the session rows of a family and leaves an unrelated session bare", async () => {
    await render();
    const parent = sessionRow("parent")!;
    expect(parent.className).toContain("lineage");
    expect(parent.style.getPropertyValue("--sess-lineage")).toBe(spine("af@p"));
    // A session in the same working copy as a family member, but in no family itself.
    expect(sessionRow("loner")!.className).not.toContain("lineage");
    expect(sessionRow("loner")!.style.getPropertyValue("--sess-lineage")).toBe("");
  });
});

describe("indentation depth cap", () => {
  it("stops indenting past three levels and lets the spine carry the rest", async () => {
    // A five-deep handoff chain, each link in its own worktree of the same base.
    const chain = [1, 2, 3, 4, 5];
    served = [{ name: "af", branch: "develop" }, ...chain.map((i) => wt(`af@${i}`, "af", at(i)))];
    useReposStore.setState({ repos: served });
    useSessionsStore.setState({
      sessions: chain.map((i) =>
        sess(`s${i}`, { repo: `af@${i}`, createdAt: at(i), originSession: i === 1 ? undefined : `s${i - 1}` }),
      ),
    });
    await render();
    expect(forest()).toEqual([["af", [["af@1", [["af@2", [["af@3", [["af@4", [["af@5", []]]]]]]]]]]]]);
    // Levels 1-3 indent; the fourth and everything under it stay put.
    const indented = (parent: string) =>
      !nodeFor(parent)!.querySelector<HTMLElement>(":scope > ul.proj-children")!.classList.contains("flat");
    expect([indented("af"), indented("af@1"), indented("af@2")]).toEqual([true, true, true]);
    expect([indented("af@3"), indented("af@4")]).toEqual([false, false]);
    // Deep or not, the whole chain is one family and says so.
    expect(spine("af@5")).toBe(spine("af@1"));
  });
});
