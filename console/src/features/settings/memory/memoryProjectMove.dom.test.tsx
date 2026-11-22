// The project move panel (#1984). Pinned here: only orphaned projects are offered as a source and
// never as a target, a taken name blocks the apply until the member picks skip or rename (and the
// preview is re-read under that choice), the apply sends the previewed names in batches of 50 with
// the source project id in the query, and a refused batch stops the run.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ProjectMovePanel, MOVE_BATCH } from "./memoryProjectMove.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { setSetting } from "../../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const proj = (id: string, display: string) => ({ id, root: "/r/" + display, vcs: "git", display });
const rows = [
  { project: proj("old-0123456789ab", "old"), count: 120, orphaned: true, live: false },
  { project: proj("gone-0123456789ab", "gone"), count: 1, orphaned: true, live: false },
  { project: proj("new-0123456789ab", "new"), count: 0, orphaned: false, live: true },
];
const mk = (n: number) => Array.from({ length: n }, (_, i) => ({ name: `mem-${i}`, status: "move", description: "d", revision: 1 }));
const plain = {
  from: proj("old-0123456789ab", "old"),
  to: proj("new-0123456789ab", "new"),
  counts: { move: 120 },
  items: mk(120),
};
const clashing = {
  ...plain,
  counts: { move: 1, collision: 1 },
  needsPolicy: true,
  items: [
    { name: "free", status: "move", description: "d", revision: 1 },
    { name: "taken", status: "collision", description: "d", revision: 2 },
  ],
};
const renamed = {
  ...plain,
  collision: "rename",
  counts: { move: 1, rename: 1 },
  items: [
    { name: "free", status: "move", description: "d", revision: 1 },
    { name: "taken", newName: "taken-moved", status: "rename", description: "d", revision: 2 },
  ],
};

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ProjectMovePanel reload={0} onChanged={() => {}} />
      </ToastProvider>,
    );
  });
  await flush();
}

const selects = () => Array.from(host!.querySelectorAll<HTMLSelectElement>("select"));
async function pick(sel: HTMLSelectElement, v: string) {
  await act(async () => {
    sel.value = v;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}
async function pickBoth() {
  await pick(selects()[0], "old-0123456789ab");
  await pick(selects()[1], "new-0123456789ab");
}
const applyButton = () => host!.querySelector<HTMLButtonElement>(".mem-ci-actions button")!;

let previewFor: (path: string) => unknown;

beforeEach(() => {
  setSetting("locale", "en");
  previewFor = () => plain;
  api.mockImplementation((path: string) =>
    Promise.resolve(path.startsWith("api/agents/memory/entries/project-move-preview") ? previewFor(path) : { projects: rows }),
  );
  apiJSON.mockImplementation((_p: string, _m: string, body: { items: { name: string }[] }) =>
    Promise.resolve({ results: body.items.map((i) => ({ name: i.name, result: "moved" })) }),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

describe("ProjectMovePanel", () => {
  it("offers orphaned projects as the source and the others as the target", async () => {
    await mount();
    const from = Array.from(selects()[0].options).map((o) => o.value);
    expect(from).toEqual(["", "old-0123456789ab", "gone-0123456789ab"]);
    await pick(selects()[0], "old-0123456789ab");
    const to = Array.from(selects()[1].options).map((o) => o.value);
    expect(to).toEqual(["", "new-0123456789ab"]);
  });

  it("suggests the live project with the same remote and only fills the pickers", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.startsWith("api/agents/memory/entries/project-move-preview")
          ? plain
          : { projects: [{ ...rows[0], mergeInto: rows[2].project }, rows[1], rows[2]] },
      ),
    );
    await mount();
    const text = host!.querySelector(".mem-pm-suggest")!.textContent ?? "";
    expect(text).toContain("old has memories but no working copy");
    expect(text).toContain("new is a checkout of the same repository");
    expect(host!.querySelector(".mem-pm-suggest li")).not.toBeNull();
    expect(host!.querySelectorAll(".mem-pm-suggest li")).toHaveLength(1);
    expect(apiJSON).not.toHaveBeenCalled();
    await act(async () => host!.querySelector<HTMLButtonElement>(".mem-pm-suggest button")!.click());
    await flush();
    expect(selects()[0].value).toBe("old-0123456789ab");
    expect(selects()[1].value).toBe("new-0123456789ab");
    // Still a preview: nothing is written until the member applies it.
    expect(apiJSON).not.toHaveBeenCalled();
    expect(applyButton().textContent).toContain("120");
  });

  it("shows no suggestion when the Agent names none", async () => {
    await mount();
    expect(host!.querySelector(".mem-pm-suggest")).toBeNull();
  });

  it("says so when nothing is orphaned", async () => {
    api.mockResolvedValue({ projects: [rows[2]] });
    await mount();
    expect(host!.querySelector("select")).toBeNull();
    expect(host!.textContent).toContain("still has its working copy");
  });

  it("sends the previewed names in batches of 50 with the source id in the query", async () => {
    await mount();
    await pickBoth();
    expect(applyButton().textContent).toContain("120");
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(Math.ceil(120 / MOVE_BATCH));
    for (const call of apiJSON.mock.calls) {
      expect(call[0]).toBe("api/agents/memory/entries/project-move?project=old-0123456789ab");
      expect(call[1]).toBe("POST");
      expect(call[2].items.length).toBeLessThanOrEqual(MOVE_BATCH);
      expect(call[2]).toMatchObject({ from: "old-0123456789ab", to: "new-0123456789ab", collision: "" });
    }
    expect(apiJSON.mock.calls.flatMap((c) => c[2].items)).toHaveLength(120);
    // Each item carries what was previewed.
    expect(apiJSON.mock.calls[0][2].items[0]).toMatchObject({ name: "mem-0", status: "move", revision: 1 });
    // The preview is re-read afterwards.
    expect(api.mock.calls.filter(([p]) => String(p).startsWith("api/agents/memory/entries/project-move-preview")).length).toBe(2);
  });

  it("blocks the apply while a name is taken, and previews again under the chosen policy", async () => {
    previewFor = (path) => (path.includes("collision=rename") ? renamed : clashing);
    await mount();
    await pickBoth();
    expect(applyButton().disabled).toBe(true);
    expect(host!.textContent).toContain("Name taken in the target");
    const radio = host!.querySelector<HTMLInputElement>('input[value="rename"]')!;
    await act(async () => radio.click());
    await flush();
    expect(api.mock.calls.some(([p]) => String(p).endsWith("&collision=rename"))).toBe(true);
    expect(host!.textContent).toContain("taken-moved");
    expect(applyButton().disabled).toBe(false);
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0][2]).toMatchObject({
      collision: "rename",
      items: [
        { name: "free", status: "move", revision: 1 },
        { name: "taken", newName: "taken-moved", status: "rename", revision: 2 },
      ],
    });
  });

  it("stops at the first refused batch", async () => {
    apiJSON.mockResolvedValueOnce({ error: { code: "memory_conflict", message: "busy" } });
    await mount();
    await pickBoth();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("counts the batches not sent as not tried, and says a partial memory is in both projects", async () => {
    // 120 previewed: the first batch of 50 stops at mem-1 (48 left in it), 70 are never sent.
    apiJSON.mockResolvedValueOnce({
      error: { code: "snapshot_failed", message: "the move stopped" },
      results: [
        { name: "mem-0", result: "moved" },
        { name: "mem-1", result: "partial", commit: "abc1234" },
      ],
      stopped: "mem-1",
      pending: Array.from({ length: 48 }, (_, i) => `mem-${i + 2}`),
    });
    await mount();
    await pickBoth();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const text = document.body.textContent ?? "";
    expect(text).toContain("stopped at mem-1");
    expect(text).toContain("after moving 1 memories");
    expect(text).toContain("118 were not tried");
    expect(text).toContain("mem-1 is now in both projects");
  });

  it("says a failed memory was not moved, and keeps the count of earlier batches", async () => {
    apiJSON
      .mockResolvedValueOnce({ results: Array.from({ length: 50 }, (_, i) => ({ name: `mem-${i}`, result: "moved" })) })
      .mockResolvedValueOnce({
        error: { code: "snapshot_failed", message: "the move stopped" },
        results: [{ name: "mem-50", result: "failed" }],
        stopped: "mem-50",
        pending: Array.from({ length: 49 }, (_, i) => `mem-${i + 51}`),
      });
    await mount();
    await pickBoth();
    await act(async () => applyButton().click());
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(2);
    const text = document.body.textContent ?? "";
    expect(text).toContain("after moving 50 memories");
    expect(text).toContain("69 were not tried"); // 49 in the batch + 20 never sent
    expect(text).toContain("mem-50 was not moved and is still in the old project");
    expect(text).not.toContain("in both projects");
  });

  it("ignores a slow preview for a choice that was replaced", async () => {
    const resolvers: Record<string, (v: unknown) => void> = {};
    api.mockImplementation((path: string) => {
      if (!path.startsWith("api/agents/memory/entries/project-move-preview")) return Promise.resolve({ projects: rows });
      const f = new URLSearchParams(path.split("?")[1]).get("from")!;
      return new Promise((r) => (resolvers[f] = r));
    });
    await mount();
    await pick(selects()[0], "gone-0123456789ab");
    await pick(selects()[1], "new-0123456789ab");
    await pick(selects()[0], "old-0123456789ab");
    await pick(selects()[1], "new-0123456789ab");
    await act(async () => resolvers["old-0123456789ab"](plain));
    await act(async () => resolvers["gone-0123456789ab"]({ ...plain, items: mk(1), counts: { move: 1 } }));
    await flush();
    expect(applyButton().textContent).toContain("120");
  });

  it("leaves the preview unloaded when the Agent answers a JSON null body", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path.startsWith("api/agents/memory/entries/project-move-preview") ? null : { projects: rows }),
    );
    await mount();
    await pick(selects()[0], "old-0123456789ab");
    await pick(selects()[1], "new-0123456789ab");
    await flush();
    expect(document.body.textContent).not.toContain("undefined");
    expect(selects().length).toBe(2); // the panel survived: no throw, pickers still there
    expect(document.body.textContent).not.toContain("0 memories");
  });
});
