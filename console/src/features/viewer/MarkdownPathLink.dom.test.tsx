// Render tests for MarkdownView's inline-code path auto-linking (linkifyPathRefs): a path
// an agent wrote as `docs/a.md` becomes a link to that file — but only when the file is
// really there, and only on a surface that can open one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (m: unknown) => toasts.push(m),
}));

// Every fs/tree listing the view asks for, and the answers this case wants to give.
let tree: Record<string, { name: string; type: string }[] | null> = {};
const asked: string[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    const dir = decodeURIComponent(new URL(path, "http://x/").searchParams.get("path") ?? "");
    asked.push(dir);
    const entries = tree[dir];
    if (!entries) return { error: { code: "http_404", message: "not_dir" } };
    return { entries };
  }),
  downloadURL: (p: string) => "/dl/" + p,
}));

// Revealing a directory lands in the ファイル rail; the store call is enough to observe.
const revealed: string[] = [];
vi.mock("../files/store.ts", () => ({
  useFilesStore: { getState: () => ({ revealInFiles: (p: string) => revealed.push(p) }) },
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { clearDirListingCache } = await import("./dirListing.ts");

let host: HTMLDivElement;
let root: Root;
const opened: { path: string; line?: number; openInNew?: boolean }[] = [];

const render = async (source: string, props: Record<string, unknown> = {}) => {
  await act(async () => {
    root.render(
      <MarkdownView
        source={source}
        baseDir="repos/x"
        onOpenFile={(path: string, line?: number, _c?: number, openInNew?: boolean) =>
          opened.push({ path, line, openInNew })
        }
        {...props}
      />,
    );
  });
  // The existence check is async: let the listing promises settle and the links land.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};
const pathLinks = () => [...host.querySelectorAll<HTMLAnchorElement>("a.md-path-link")];

beforeEach(() => {
  toasts.length = 0;
  opened.length = 0;
  revealed.length = 0;
  asked.length = 0;
  tree = {};
  clearDirListingCache();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("MarkdownView path auto-linking", () => {
  it("links an existing file cited as inline code, and leaves a missing one as text", async () => {
    tree["repos/x/docs"] = [{ name: "a.md", type: "file" }];
    await render("出力: `docs/a.md`（`docs/nope.md` は無い）");
    const links = pathLinks();
    expect(links.map((a) => a.textContent)).toEqual(["docs/a.md"]);
    // The anchor lives inside the <code> so the chip styling is kept.
    expect(links[0].closest("code")).not.toBeNull();
    expect(links[0].title).toContain("repos/x/docs/a.md");
  });

  it("opens the file on click, and a new pane for Ctrl-click", async () => {
    tree["repos/x/docs"] = [{ name: "a.md", type: "file" }];
    await render("`docs/a.md:12` を見て");
    const a = pathLinks()[0];
    expect(a).toBeTruthy();
    await act(async () => {
      a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(opened).toEqual([{ path: "repos/x/docs/a.md", line: 12, openInNew: false }]);
    await act(async () => {
      a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true }));
    });
    expect(opened[1]).toEqual({ path: "repos/x/docs/a.md", line: 12, openInNew: true });
  });

  it("reveals a directory (written with a trailing slash) in the file rail", async () => {
    tree["repos/x"] = [{ name: "_act-parts", type: "dir" }];
    await render("下読みは `_act-parts/` に置いた");
    const a = pathLinks()[0];
    expect(a?.textContent).toBe("_act-parts/");
    await act(async () => {
      a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(revealed).toEqual(["repos/x/_act-parts"]);
    expect(opened).toEqual([]);
  });

  it("says so instead of opening an empty pane when the file vanished after rendering", async () => {
    tree["repos/x/docs"] = [{ name: "a.md", type: "file" }];
    await render("`docs/a.md`");
    const a = pathLinks()[0];
    tree["repos/x/docs"] = []; // deleted between the reply and the click
    clearDirListingCache();
    await act(async () => {
      a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(opened).toEqual([]);
    expect(toasts).toHaveLength(1);
  });

  it("does not link on a surface that cannot open a file (the shared view)", async () => {
    tree["repos/x/docs"] = [{ name: "a.md", type: "file" }];
    await render("`docs/a.md`", { onOpenFile: undefined });
    expect(pathLinks()).toHaveLength(0);
    expect(asked).toEqual([]); // and asks the filesystem nothing
  });

  it("never touches a fenced code block, and asks for no listing when nothing is path-shaped", async () => {
    tree["repos/x/docs"] = [{ name: "a.md", type: "file" }];
    await render("```\ndocs/a.md\n```\n\n`npm run build` と `develop`");
    expect(pathLinks()).toHaveLength(0);
    expect(asked).toEqual([]);
  });

  it("asks for one listing per directory however many paths cite it", async () => {
    tree["repos/x/docs"] = [
      { name: "a.md", type: "file" },
      { name: "b.md", type: "file" },
    ];
    await render("`docs/a.md` と `docs/b.md`");
    expect(pathLinks()).toHaveLength(2);
    expect(asked).toEqual(["repos/x/docs"]);
  });
});
