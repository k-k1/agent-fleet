// Rendering of the changed-files strip (docs/log/68 §68.5). What is pinned is not the look
// but three promises:
//   - with nothing to show, drop the whole strip rather than render "0 files" (a kind that
//     records no edits and a session that really changed nothing are indistinguishable)
//   - keep rows the working tree has no diff for (dropping them reads as "I just edited that
//     and it is gone")
//   - remember open/closed per session (same manners as the ToDo strip; it must stay folded
//     across a terminal/chat round trip)
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  isTransientErr: (d: unknown) => !!d && typeof d === "object" && "error" in (d as object),
}));

import { FileChangeStrip } from "./FileChangeStrip.tsx";
import type { SessionFile } from "./sessionFiles.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const file = (over: Partial<SessionFile> = {}): SessionFile => ({
  path: "repos/r/src/a.ts",
  repo: "r",
  rel: "src/a.ts",
  verb: "edit",
  added: 4,
  removed: 1,
  count: 1,
  lastIdx: 3,
  lastTs: "2026-08-17T10:00:00Z",
  ...over,
});

async function render(session: string, files: SessionFile[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<FileChangeStrip session={session} files={files} />);
  });
  return host;
}

// Two loads back the strip: the working tree (/fs/changes) and the commits made since the
// session started (/sessions/{name}/committed). Dispatch on the URL.
const route = (changes: unknown[], committed: string[] = []) => (url: string) =>
  Promise.resolve(url.includes("/committed") ? { committed } : { changes });

beforeEach(() => {
  localStorage.clear();
  apiMock.mockReset();
  apiMock.mockImplementation(route([{ path: "repos/r/src/a.ts", repo: "r", index: " ", worktree: "M" }]));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("FileChangeStrip", () => {
  it("renders no strip at all when there is not a single edit", async () => {
    const el = await render("s1", []);
    expect(el.querySelector(".mirror-files")).toBeNull();
  });

  it("is folded by default, showing only the count and the most recent file name", async () => {
    const el = await render("s1", [file()]);
    const strip = el.querySelector(".mirror-files");
    expect(strip).not.toBeNull();
    expect(strip!.classList.contains("open")).toBe(false);
    expect(el.querySelector(".mfl-count")!.textContent).toBe("1");
    expect(el.querySelector(".mfl-lead")!.textContent).toBe("a.ts");
    // The total +/- shows even while folded, so the scale is readable at a glance.
    expect(el.querySelector(".mfl-stat .dv-add")!.textContent).toBe("+4");
  });

  it("shows the rows once opened, with the state badge coming from git", async () => {
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect(el.querySelector(".mirror-files")!.classList.contains("open")).toBe(true);
    const rows = el.querySelectorAll(".mfl-item");
    expect(rows).toHaveLength(1);
    expect(rows[0].classList.contains("mfl-unstaged")).toBe(true);
    expect(el.querySelector(".mfl-name")!.textContent).toBe("a.ts");
    expect(el.querySelector(".mfl-dir")!.textContent).toBe("src");
  });

  it("keeps rows the working tree has no diff for, greyed out", async () => {
    apiMock.mockImplementation(route([]));
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    const row = el.querySelector(".mfl-item")!;
    expect(row.classList.contains("mfl-clean")).toBe(true);
    // It must stay openable — the file itself is still there.
    expect((row.querySelector(".mfl-row") as HTMLButtonElement).disabled).toBe(false);
  });

  it("remembers the open/closed choice per session", async () => {
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect(localStorage.getItem("af.mirror-files-open.s1")).toBe("1");

    act(() => root?.unmount());
    host?.remove();
    const again = await render("s1", [file()]);
    expect(again.querySelector(".mirror-files")!.classList.contains("open")).toBe(true);

    // Another session does not inherit that choice.
    act(() => root?.unmount());
    host?.remove();
    const other = await render("s2", [file()]);
    expect(other.querySelector(".mirror-files")!.classList.contains("open")).toBe(false);
  });

  it("marks a row with no diff but present in a commit as committed (docs/log/68 P2)", async () => {
    apiMock.mockImplementation(route([], ["src/a.ts"]));
    const el = await render("s1", [file()]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    const row = el.querySelector(".mfl-item")!;
    expect(row.classList.contains("mfl-committed")).toBe(true);
    expect(row.classList.contains("mfl-clean")).toBe(false);
  });

  it("still renders the strip when the committed query fails (it only refines rows)", async () => {
    apiMock.mockImplementation((url: string) =>
      url.includes("/committed") ? Promise.reject(new Error("boom")) : Promise.resolve({ changes: [] }),
    );
    const el = await render("s1", [file()]);
    expect(el.querySelector(".mirror-files")).not.toBeNull();
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect(el.querySelector(".mfl-item")!.classList.contains("mfl-clean")).toBe(true);
  });

  it("cannot open the row of a deleted file", async () => {
    apiMock.mockImplementation(route([]));
    const el = await render("s1", [file({ verb: "delete" })]);
    await act(async () => {
      (el.querySelector(".mirror-files-toggle") as HTMLButtonElement).click();
    });
    expect((el.querySelector(".mfl-row") as HTMLButtonElement).disabled).toBe(true);
  });
});
