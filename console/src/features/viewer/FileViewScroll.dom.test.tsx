// Returning to the reading position after switching tabs and coming back (scrollMemory).
//
// A tabbed cell draws only the one selected view (PaneHost), so moving to another tab unmounts
// FileView. That is exactly what is reproduced here: not "re-render with the same props" but
// detach and attach again.
//
// jsdom has no layout, so only the scroll container's dimensions are stubbed. The real height
// build-up (Markdown innerHTML, highlighting, images) cannot be stubbed and is visible only in a
// real browser (src/test/domSetup.ts).
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { clearScrollPos } from "./scrollMemory.ts";

const CODE = Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join("\n");

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const filePath = decodeURIComponent(path.split("path=")[1] || "");
    return {
      path: filePath,
      size: CODE.length,
      binary: false,
      truncated: false,
      editable: false,
      editabilityReason: "read_only_root",
      content: CODE,
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));

const { FileView } = await import("./FileView.tsx");

const VIEW_H = 400;
const CONTENT_H = 6000;

let root: Root | null = null;
let host: HTMLDivElement;

// Stub: only the scroll container (.codeview) gets content taller than the viewport.
const scrollable = (el: HTMLElement) => el.classList?.contains("codeview");
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollable(this) ? VIEW_H : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollable(this) ? CONTENT_H : 0;
    },
  });
});
afterAll(() => {
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientHeight;
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollHeight;
});

beforeEach(() => {
  clearScrollPos();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  clearScrollPos();
});

async function open(props: { paneId?: string; filePath?: string } = {}): Promise<HTMLElement> {
  await act(async () => {
    root!.render(<FileView filePath={props.filePath ?? "repos/x/main.go"} paneId={props.paneId ?? "pane-1"} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
  return host.querySelector(".codeview") as HTMLElement;
}

/** Moving to another tab, i.e. this view is unmounted. */
async function leave(): Promise<void> {
  await act(async () => {
    root!.render(<div />);
  });
}

/** Simulates the reader scrolling there; the scroll event comes from the container. */
function scrollTo(el: HTMLElement, top: number): void {
  el.scrollTop = top;
  act(() => {
    el.dispatchEvent(new Event("scroll"));
  });
}

it("returns to the reading position after switching tabs and coming back", async () => {
  const first = await open();
  expect(first).not.toBeNull();
  scrollTo(first, 1800);

  await leave();
  const again = await open();

  expect(again).not.toBe(first); // really re-attached, i.e. the path that would otherwise reset to 0
  expect(again.scrollTop).toBe(1800);
});

it("someone who scrolled back to the top before leaving comes back to the top", async () => {
  const first = await open();
  scrollTo(first, 1800);
  scrollTo(first, 0);

  await leave();
  expect((await open()).scrollTop).toBe(0);
});

it("does not leak the position across panes or files", async () => {
  scrollTo(await open({ paneId: "pane-1", filePath: "repos/x/main.go" }), 1800);
  await leave();

  // the same file in another pane
  expect((await open({ paneId: "pane-2", filePath: "repos/x/main.go" })).scrollTop).toBe(0);
  await leave();
  // another file in the same pane
  expect((await open({ paneId: "pane-1", filePath: "repos/x/other.go" })).scrollTop).toBe(0);
  await leave();
  // the original pair is still remembered
  expect((await open({ paneId: "pane-1", filePath: "repos/x/main.go" })).scrollTop).toBe(1800);
});

it("stops at the lowest reachable point when the content has become shorter", async () => {
  // The position is in px, so if the file shrank while away the target no longer exists.
  const first = await open();
  scrollTo(first, 5000);
  await leave();

  const shorter = 900;
  const spy = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return scrollable(this) ? shorter : 0;
  });
  try {
    expect((await open()).scrollTop).toBe(shorter - VIEW_H);
  } finally {
    spy.mockRestore();
  }
});
