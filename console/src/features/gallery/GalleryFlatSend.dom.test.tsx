// The gallery's two P1 leftovers from ADR 0080 that a screenshot cannot pin: "include
// subfolders" reads a different endpoint and keeps each picture's own folder (a rename must not
// move a picture up into the folder on screen), and "send" goes out through the file pane's own
// two paths — a session's input, or an assistant chat with the file attached — and nowhere else.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
  size?: number;
  mtime?: number;
}

/** What fs/tree answers, and what fs/images answers. */
let tree: Entry[] = [];
let flatAnswer: { entries: Entry[]; truncated?: boolean } = { entries: [] };
let requests: { url: string; method: string; body?: string }[] = [];

const respond = (body: unknown) =>
  ({
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => null },
    text: async () => JSON.stringify(body),
    json: async () => body,
  }) as unknown as Response;

const fetchMock = vi.fn(async (url: string, opts?: RequestInit) => {
  const u = String(url);
  requests.push({ url: u, method: String(opts?.method || "GET"), body: opts?.body ? String(opts.body) : undefined });
  if (u.includes("fs/images")) return respond(flatAnswer);
  if (u.includes("fs/tree")) return respond({ entries: tree });
  if (u.includes("api/assistants")) return respond({ assistants: [{ id: "a1", name: "Painter" }] });
  if (u.includes("api/chat/conversations")) return respond({ id: "conv-1" });
  if (u.includes("/input")) return respond({ ok: true });
  return respond([]);
});
vi.stubGlobal("fetch", fetchMock);

const { GalleryView } = await import("./GalleryView.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { allViews, freshLayout } = await import("../../layout/ops.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { clearGalleryCache } = await import("./galleryCache.ts");

let host: HTMLDivElement;
let root: Root;

const img = (name: string, mtime = 100): Entry => ({ name, type: "file", size: 1000, mtime });

const galleryContent = () => allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "gallery")!;

const render = async (path = "gen", flat?: boolean) => {
  useLayoutStore.getState().openTarget({ content: { kind: "gallery", galleryPath: path, ...(flat ? { flat } : {}) } });
  const paneId = galleryContent().id;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <GalleryView paneId={paneId} path={path} flat={flat} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
};

const cards = () => [...host.querySelectorAll<HTMLElement>(".gal-card:not(.folder)")];
const itemFor = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".gal-ctxmenu .ui-menu-item")].find((b) => b.textContent?.includes(label));

const click = async (el: Element | null | undefined) => {
  if (!el) throw new Error("not in the DOM");
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};
const openMenu = async (n = 0) => {
  await act(async () => {
    cards()[n].dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 40 }));
  });
};
/** Let the modal's own reads (assistants, memo categories) land. */
const settle = () => act(async () => new Promise((r) => setTimeout(r, 0)));

beforeEach(() => {
  setLocale("en");
  requests = [];
  fetchMock.mockClear();
  clearGalleryCache();
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
  useLayoutStore.setState({ layout: freshLayout() });
  useSessionsStore.setState({ sessions: [] });
  tree = [img("top.png"), { name: "sub", type: "dir" }];
  flatAnswer = { entries: [img("sub/deep/a.png", 300), img("top.png", 100)] };
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
  vi.stubGlobal("fetch", fetchMock);
});

describe("include subfolders", () => {
  it("reads fs/images, not fs/tree, and each card keeps its own folder", async () => {
    await render("gen", true);
    expect(requests.some((r) => r.url.includes("api/fs/images?path=gen&"))).toBe(true);
    expect(requests.some((r) => r.url.includes("fs/tree"))).toBe(false);
    expect(cards().map((c) => c.querySelector(".gal-name")?.textContent)).toEqual(["sub/deep/a.png", "top.png"]);
    expect(host.querySelector(".gal-card.folder")).not.toBeNull(); // "Up" only — no subfolder cards
    expect(host.querySelectorAll(".gal-card.folder")).toHaveLength(1);
    const thumb = cards()[0].querySelector(".gal-name")?.getAttribute("title");
    expect(thumb).toBe("gen/sub/deep/a.png");
  });

  it("says when the Agent's walk left pictures out", async () => {
    flatAnswer = { ...flatAnswer, truncated: true };
    await render("gen", true);
    expect(host.querySelector(".gal-count")?.textContent).toContain("newest 2 only");
  });

  it("the toggle writes flat into the pane content, and off writes it as absent", async () => {
    await render("gen");
    const toggle = host.querySelector<HTMLButtonElement>(".gal-flat")!;
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    await click(toggle);
    expect(galleryContent().content).toEqual({ kind: "gallery", galleryPath: "gen", flat: true });
  });

  it("a rename stays in the picture's own folder, not the one on screen", async () => {
    vi.stubGlobal("prompt", () => "renamed.png");
    await render("gen", true);
    await openMenu(0);
    await click(itemFor("Rename the file"));
    const rename = requests.find((r) => r.url.includes("fs/rename"));
    expect(rename?.url).toContain("from=" + encodeURIComponent("gen/sub/deep/a.png"));
    expect(rename?.url).toContain("to=" + encodeURIComponent("gen/sub/deep/renamed.png"));
  });
});

describe("send", () => {
  const session = { name: "s1", kind: "claude", title: "Painter session", alive: true, state: "idle" };

  it("is offered for a picture and not for a folder", async () => {
    await render("gen");
    await openMenu(0);
    expect(itemFor("Send to a session")).toBeDefined();
    await act(async () => {
      host
        .querySelectorAll<HTMLElement>(".gal-card.folder")[1]
        .dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    expect(itemFor("Send to a session")).toBeUndefined();
  });

  it("to a session: the picture's path goes to that session's input", async () => {
    useSessionsStore.setState({ sessions: [session] as never });
    await render("gen");
    await openMenu(0);
    await click(itemFor("Send to a session"));
    await settle();
    const form = document.querySelector<HTMLFormElement>(".send-modal")!;
    expect(form).not.toBeNull();
    await act(async () => {
      form.requestSubmit();
    });
    await settle();
    const sent = requests.find((r) => r.url.includes("api/sessions/s1/input"));
    expect(sent?.method).toBe("POST");
    expect(JSON.parse(sent!.body!).prompt).toContain("~/gen/top.png");
  });

  it("to an assistant: a chat is created with the picture attached and opened", async () => {
    await render("gen");
    await openMenu(0);
    await click(itemFor("Send to a session"));
    await settle();
    // No session runs, so the assistant is the default destination.
    const select = document.querySelector<HTMLSelectElement>(".send-modal select")!;
    expect(select.value).toBe("assistant:a1");
    await act(async () => {
      document.querySelector<HTMLFormElement>(".send-modal")!.requestSubmit();
    });
    await settle();
    const created = requests.find((r) => r.url.includes("api/chat/conversations") && r.method === "POST");
    expect(JSON.parse(created!.body!)).toMatchObject({ assistant_id: "a1", attach_path: "gen/top.png" });
    const chat = allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "chat");
    expect(chat?.content).toMatchObject({ kind: "chat", conversationId: "conv-1" });
    expect(requests.some((r) => r.url.includes("/input"))).toBe(false);
  });
});
