// The right-click menu on file / folder links in the Markdown view: `[text](rel/path)` links
// (wireLinks) and inline-code paths (linkifyPathRefs). Both are DOM-built, so the menu is
// reached through PathLinkMenuHost; a surface without a host or without onOpenFile keeps the
// browser's own menu.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (m: unknown) => toasts.push(m),
}));

// fs listing for `[text](path)` links and the resolver for inline-code paths.
let listing: { name: string; type: string }[] = [];
// When set, every fs answer waits for it (a slow response).
let gate: Promise<void> | null = null;
let resolved: Record<string, { path: string; type: string }> = {};
vi.mock("../../core/api/client.ts", () => ({
  // Read when the preview stores load (MarkdownView links loopback URLs to the workspace preview).
  getTenant: () => "",
  api: vi.fn(async () => {
    if (gate) await gate;
    return { entries: listing };
  }),
  apiJSON: vi.fn(async (_path: string, _method: string, body: { refs: string[] }) => {
    if (gate) await gate;
    const out: Record<string, { path: string; type: string }> = {};
    for (const ref of body.refs) if (resolved[ref]) out[ref] = resolved[ref];
    return { resolved: out };
  }),
  downloadURL: (p: string) => "/dl/" + p,
}));

const revealed: string[] = [];
vi.mock("../files/store.ts", () => ({
  useFilesStore: {
    getState: () => ({ revealInFiles: (path: string) => revealed.push(path) }),
  },
}));

const galleries: { path: string; focus?: string; newPane?: boolean }[] = [];
vi.mock("../gallery/open.ts", () => ({
  openGallery: (path: string, o: { focus?: string; newPane?: boolean } = {}) => galleries.push({ path, ...o }),
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { PathLinkMenuHost } = await import("./PathLinkMenu.tsx");
const { clearPathRefCache } = await import("./pathResolve.ts");

let host: HTMLDivElement;
let root: Root;
const opened: { path: string; openInNew?: boolean }[] = [];

const render = async (source: string, opts: { menu?: boolean; open?: boolean } = {}) => {
  const view = (
    <MarkdownView
      source={source}
      basePath="repos/x/README.md"
      baseDir="repos/x"
      onOpenFile={
        opts.open === false
          ? undefined
          : (path: string, _l?: number, _c?: number, openInNew?: boolean) => opened.push({ path, openInNew })
      }
    />
  );
  await act(async () => {
    root.render(opts.menu === false ? view : <PathLinkMenuHost>{view}</PathLinkMenuHost>);
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};
const settle = () =>
  act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
const contextMenu = async (a: Element) => {
  const e = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 });
  await act(async () => {
    a.dispatchEvent(e);
  });
  await settle();
  return e;
};
const menuEl = () => document.body.querySelector<HTMLElement>("ul.files-ctxmenu");
const labels = () => [...(menuEl()?.querySelectorAll(".ui-menu-item") ?? [])].map((b) => b.textContent?.trim());
const repoLink = () => host.querySelector<HTMLAnchorElement>("a.repo-link")!;
const pathLink = () => host.querySelector<HTMLAnchorElement>("a.md-path-link")!;

beforeEach(() => {
  toasts.length = 0;
  opened.length = 0;
  revealed.length = 0;
  galleries.length = 0;
  listing = [];
  gate = null;
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

describe("[text](path) link menu", () => {
  it("opens the menu on right click and opens the file in a new pane from it", async () => {
    listing = [{ name: "a.md", type: "file" }];
    await render("[doc](docs/a.md)");
    const e = await contextMenu(repoLink());
    expect(e.defaultPrevented).toBe(true);
    expect(menuEl()).not.toBeNull();
    expect(labels()).toEqual(expect.arrayContaining(["Open in pane", "Open in new pane", "Show in Files"]));
    // Destructive actions stay out of a rendered document.
    expect(labels().join("|")).not.toMatch(/Rename|Delete|New file/i);
    const newPane = [...menuEl()!.querySelectorAll("button")].find((b) => b.textContent?.includes("Open in new pane"))!;
    await act(async () => newPane.click());
    expect(opened).toEqual([{ path: "repos/x/docs/a.md", openInNew: true }]);
    expect(menuEl()).toBeNull();
  });

  it("opens from the keyboard (Shift+F10)", async () => {
    listing = [{ name: "a.md", type: "file" }];
    await render("[doc](docs/a.md)");
    await act(async () => {
      repoLink().dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true }));
    });
    await settle();
    expect(menuEl()).not.toBeNull();
  });

  it("offers reveal but no open item for a folder", async () => {
    listing = [{ name: "docs", type: "dir" }];
    await render("[docs](docs)");
    await contextMenu(repoLink());
    const l = labels();
    expect(l).toContain("Show in Files");
    expect(l).not.toContain("Open in pane");
    const reveal = [...menuEl()!.querySelectorAll("button")].find((b) => b.textContent?.includes("Show in Files"))!;
    await act(async () => reveal.click());
    expect(revealed).toEqual(["repos/x/docs"]);
  });

  it("toasts instead of opening a menu when the target is gone", async () => {
    listing = [];
    await render("[doc](docs/a.md)");
    await contextMenu(repoLink());
    expect(menuEl()).toBeNull();
    expect(String(toasts[0])).toContain("repos/x/docs/a.md");
  });

  it("leaves the native menu alone without a host", async () => {
    await render("[doc](docs/a.md)", { menu: false });
    expect((await contextMenu(repoLink())).defaultPrevented).toBe(false);
    expect(menuEl()).toBeNull();
  });

  it("leaves the native menu alone without onOpenFile (the shared view)", async () => {
    await render("[doc](docs/a.md)", { open: false });
    expect((await contextMenu(repoLink())).defaultPrevented).toBe(false);
    expect(menuEl()).toBeNull();
  });

  it("keeps left click opening the file", async () => {
    listing = [{ name: "a.md", type: "file" }];
    await render("[doc](docs/a.md)");
    await act(async () => {
      repoLink().dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    await settle();
    expect(opened).toEqual([{ path: "repos/x/docs/a.md", openInNew: false }]);
    expect(menuEl()).toBeNull();
  });
});

describe("inline-code path link menu", () => {
  it("opens the menu with file actions", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`");
    const e = await contextMenu(pathLink());
    expect(e.defaultPrevented).toBe(true);
    expect(labels()).toEqual(
      expect.arrayContaining(["Open in pane", "Open in new pane", "Show in Files", "Copy the name", "Copy the relative path", "Download"]),
    );
  });

  const item = (text: string) => [...menuEl()!.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;

  it("offers Open folder and Open folder in gallery for a non-image file", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`");
    await contextMenu(pathLink());
    expect(labels()).toEqual(expect.arrayContaining(["Open the folder", "Open folder in gallery"]));
    await act(async () => item("Open the folder").click());
    expect(revealed).toEqual(["repos/x/docs"]);
    await contextMenu(pathLink());
    await act(async () => item("Open folder in gallery").click());
    // No image to focus: the folder itself, in the current pane.
    expect(galleries).toEqual([{ path: "repos/x/docs", newPane: false }]);
  });

  it("opens the folder gallery in a new pane on Ctrl-click", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`");
    await contextMenu(pathLink());
    await act(async () => {
      item("Open folder in gallery").dispatchEvent(new MouseEvent("click", { bubbles: true, ctrlKey: true }));
    });
    expect(galleries).toEqual([{ path: "repos/x/docs", newPane: true }]);
  });

  it("keeps the focused gallery item for an image instead of a second folder item", async () => {
    resolved["docs/p.png"] = { path: "repos/x/docs/p.png", type: "file" };
    await render("see `docs/p.png`");
    await contextMenu(pathLink());
    expect(labels()).toContain("Open in gallery");
    expect(labels()).not.toContain("Open folder in gallery");
    expect(labels()).toContain("Open the folder");
  });

  it("offers no folder items for a file directly in home or outside it, nor for a folder", async () => {
    resolved["a.md"] = { path: "a.md", type: "file" };
    await render("see `a.md`");
    await contextMenu(pathLink());
    expect(labels()).not.toContain("Open the folder");
    expect(labels()).not.toContain("Open folder in gallery");
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    resolved["/scratch/out.txt"] = { path: "/scratch/out.txt", type: "file" };
    await render("see `/scratch/out.txt`");
    await contextMenu(pathLink());
    expect(labels()).not.toContain("Open the folder");
    expect(labels()).not.toContain("Open folder in gallery");
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    listing = [{ name: "docs", type: "dir" }];
    await render("[docs](docs)");
    await contextMenu(repoLink());
    expect(labels()).not.toContain("Open the folder");
  });

  it("limits a path outside home to open, copy path and download", async () => {
    resolved["/scratch/out.txt"] = { path: "/scratch/out.txt", type: "file" };
    await render("see `/scratch/out.txt`");
    await contextMenu(pathLink());
    const l = labels();
    expect(l).toEqual(expect.arrayContaining(["Open in pane", "Copy the path", "Download"]));
    expect(l).not.toContain("Show in Files");
    expect(l).not.toContain("Copy the name");
  });

  it("toasts when the file vanished after the link was rendered", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`");
    clearPathRefCache();
    resolved = {};
    await contextMenu(pathLink());
    expect(menuEl()).toBeNull();
    expect(String(toasts[0])).toContain("repos/x/docs/a.md");
  });

  it("does not suppress the native menu without a host", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`", { menu: false });
    expect((await contextMenu(pathLink())).defaultPrevented).toBe(false);
  });
});

describe("lifetime of a menu launch", () => {
  const slow = () => {
    let release!: () => void;
    gate = new Promise<void>((r) => (release = r));
    return release;
  };

  it("does not show a stale document's menu after the document changed (markdown link)", async () => {
    listing = [{ name: "a.md", type: "file" }];
    await render("[doc](docs/a.md)");
    const release = slow();
    await contextMenu(repoLink());
    await render("plain text");
    release();
    await settle();
    expect(menuEl()).toBeNull();
  });

  it("does not show a stale document's menu after the document changed (inline code)", async () => {
    resolved["docs/a.md"] = { path: "repos/x/docs/a.md", type: "file" };
    await render("see `docs/a.md`");
    const link = pathLink();
    const release = slow();
    await act(async () => {
      link.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    await render("plain text");
    release();
    await settle();
    expect(menuEl()).toBeNull();
  });

  it("drops a pending long press whose link was replaced", async () => {
    vi.useFakeTimers();
    try {
      listing = [{ name: "a.md", type: "file" }];
      const press = async (replace: boolean) => {
        await render("[doc](docs/a.md)");
        const touches = [{ clientX: 5, clientY: 5 }] as unknown as Touch[];
        await act(async () => {
          const a = repoLink();
          // A touch pointerdown precedes the touchstart; the long-press gate needs to have seen it.
          a.dispatchEvent(new Event("pointerdown", { bubbles: true }));
          a.dispatchEvent(new TouchEvent("touchstart", { bubbles: true, touches }));
        });
        if (replace) await render("plain text");
        await act(async () => {
          await vi.advanceTimersByTimeAsync(800);
        });
      };
      // Control: an untouched link does open the menu from a long press.
      await press(false);
      expect(menuEl()).not.toBeNull();
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
      expect(menuEl()).toBeNull();
      await press(true);
      expect(menuEl()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("focus after the menu closes", () => {
  it("returns to the link when Escape closes a keyboard-opened menu", async () => {
    listing = [{ name: "a.md", type: "file" }];
    await render("[doc](docs/a.md)");
    const link = repoLink();
    link.focus();
    await act(async () => {
      link.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true }));
    });
    await settle();
    // Roving focus skips items jsdom reports as not laid out; put focus on an item by hand.
    menuEl()!.querySelector<HTMLElement>(".ui-menu-item")!.focus();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(menuEl()).toBeNull();
    expect(document.activeElement).toBe(link);
  });
});
