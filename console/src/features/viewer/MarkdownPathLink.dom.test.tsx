// Render tests for MarkdownView's inline-code path auto-linking (linkifyPathRefs): a path
// an agent wrote as `docs/a.md` becomes a link to that file — but only when the agent's
// resolver placed it, and only on a surface that can open one.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (m: unknown) => toasts.push(m),
}));

// The Workspace agent's resolver (POST fs/resolve): every request it is asked, and the
// answers this case wants to give. Keyed by ref, exactly like the real endpoint.
let resolved: Record<string, { path: string; type: string }> = {};
const asked: { cwd: string; refs: string[] }[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async () => ({})),
  apiJSON: vi.fn(async (_path: string, _method: string, body: { cwd: string; refs: string[] }) => {
    asked.push(body);
    const out: Record<string, { path: string; type: string }> = {};
    for (const ref of body.refs) if (resolved[ref]) out[ref] = resolved[ref];
    return { resolved: out };
  }),
  downloadURL: (p: string) => "/dl/" + p,
}));

// Revealing a directory lands in the ファイル rail; the store call is enough to observe.
const revealed: string[] = [];
vi.mock("../files/store.ts", () => ({
  useFilesStore: { getState: () => ({ revealInFiles: (p: string) => revealed.push(p) }) },
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { clearPathRefCache } = await import("./pathResolve.ts");

let host: HTMLDivElement;
let root: Root;
const opened: { path: string; line?: number; openInNew?: boolean }[] = [];

const render = async (source: string, props: Record<string, unknown> = {}) => {
  await act(async () => {
    root.render(
      <MarkdownView
        source={source}
        baseDir="repos/x/sub"
        onOpenFile={(path: string, line?: number, _c?: number, openInNew?: boolean) =>
          opened.push({ path, line, openInNew })
        }
        {...props}
      />,
    );
  });
  // Resolution is async: let the request settle and the links land.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};
const pathLinks = () => [...host.querySelectorAll<HTMLAnchorElement>("a.md-path-link")];
const click = async (a: HTMLAnchorElement, init: MouseEventInit = {}) => {
  await act(async () => {
    a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
    await Promise.resolve();
    await Promise.resolve();
  });
};

beforeEach(() => {
  toasts.length = 0;
  opened.length = 0;
  revealed.length = 0;
  asked.length = 0;
  resolved = {};
  clearPathRefCache();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("MarkdownView path auto-linking", () => {
  it("links a path the agent resolved and leaves an unresolved one as text", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("出力: `docs/a.md`（`docs/nope.md` は無い）");
    const links = pathLinks();
    expect(links.map((a) => a.textContent)).toEqual(["docs/a.md"]);
    // The anchor lives inside the <code> so the chip styling is kept.
    expect(links[0].closest("code")).not.toBeNull();
    // The path shown is the one the agent resolved — note it is NOT under the cwd the
    // Console sent: the repository-root fallback is the agent's, and the Console just
    // uses what came back.
    expect(links[0].title).toContain("repos/x/docs/a.md");
  });

  it("sends the turn's cwd and asks about every candidate at once", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("`docs/a.md` と `b.md` と `docs/nope.md`");
    expect(asked).toEqual([{ cwd: "repos/x/sub", refs: ["docs/a.md", "b.md", "docs/nope.md"] }]);
  });

  it("opens the file on click, at its line, and in a new pane for Ctrl-click", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("`docs/a.md:12` を見て");
    const a = pathLinks()[0];
    expect(a?.textContent).toBe("docs/a.md:12");
    await click(a);
    expect(opened).toEqual([{ path: "repos/x/docs/a.md", line: 12, openInNew: false }]);
    await click(a, { ctrlKey: true });
    expect(opened[1]).toEqual({ path: "repos/x/docs/a.md", line: 12, openInNew: true });
  });

  it("reveals a directory (written with a trailing slash) in the file rail", async () => {
    resolved["_act-parts"] = { path: "repos/x/_act-parts", type: "dir" };
    await render("下読みは `_act-parts/` に置いた");
    const a = pathLinks()[0];
    expect(a?.textContent).toBe("_act-parts/");
    await click(a);
    expect(revealed).toEqual(["repos/x/_act-parts"]);
    expect(opened).toEqual([]);
  });

  it("re-resolves on click, so a file that vanished says so instead of opening a pane", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("`docs/a.md`");
    const a = pathLinks()[0];
    resolved = {}; // deleted between the reply and the click
    await click(a);
    expect(opened).toEqual([]);
    expect(toasts).toHaveLength(1);
  });

  it("does not link — or even ask — on a surface that cannot open a file (the shared view)", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("`docs/a.md`", { onOpenFile: undefined });
    expect(pathLinks()).toHaveLength(0);
    expect(asked).toEqual([]);
  });

  it("never touches a fenced code block, and asks nothing when no token is path-shaped", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("```\ndocs/a.md\n```\n\n`npm run build` と `develop`");
    expect(pathLinks()).toHaveLength(0);
    expect(asked).toEqual([]);
  });

  it("asks once for a path already resolved for the same cwd (turns share the memo)", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("`docs/a.md`");
    await render("`docs/a.md` — 別ターンの本文");
    expect(asked).toHaveLength(1);
    expect(pathLinks()).toHaveLength(1);
  });
});
