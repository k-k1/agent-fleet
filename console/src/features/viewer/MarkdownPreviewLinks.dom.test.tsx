// Loopback URLs in Markdown (#2414): classification, the opt-in gate, what a click / Ctrl-click /
// middle-click opens, the inline-code case, the stopped workspace, the menu and the dimming of a
// port nobody listens on.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
// One identity across renders, like the real provider: MarkdownView's render effect depends on it.
const toast = (m: unknown) => toasts.push(m);
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => toast,
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { PortLinkMenuHost } = await import("../preview/PortLinkMenu.tsx");
const { usePreviewStore, currentScope } = await import("../preview/previewState.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { previewURL } = await import("../../core/api/client.ts");
const { loopbackTarget } = await import("./parts/mdPreviewLinks.ts");
const { t } = await import("../../lib/i18n/index.ts");

let host: HTMLDivElement;
let root: Root;
const openTarget = vi.fn();
const openTargetInNew = vi.fn();
const refresh = vi.fn(async () => {});
const original = { openTarget: useLayoutStore.getState().openTarget, openTargetInNew: useLayoutStore.getState().openTargetInNew };
let open: ReturnType<typeof vi.spyOn>;

const render = async (source: string, props: Record<string, unknown> = {}, withHost = false) => {
  const view = <MarkdownView source={source} {...props} />;
  await act(async () => {
    root.render(withHost ? <PortLinkMenuHost>{view}</PortLinkMenuHost> : view);
  });

};
const links = () => [...host.querySelectorAll<HTMLAnchorElement>("a")];
const pv = () => [...host.querySelectorAll<HTMLAnchorElement>("a.preview-link")];
const click = async (a: HTMLElement, init: MouseEventInit = {}) =>
  act(async () => {
    a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
    await Promise.resolve();
    await Promise.resolve();
  });

beforeEach(() => {
  toasts.length = 0;
  openTarget.mockReset();
  openTargetInNew.mockReset();
  refresh.mockClear();
  useLayoutStore.setState({ openTarget, openTargetInNew });
  usePreviewStore.setState({ info: null, scope: "", refresh });
  useWorkspaceStore.setState({ state: "running", browserUnavailable: "" });
  useSessionsStore.setState({ sessions: [], loaded: false });
  open = vi.spyOn(window, "open").mockReturnValue(null);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  open.mockRestore();
  useLayoutStore.setState(original);
  useWorkspaceStore.setState({ state: "", browserUnavailable: "" });
  useSessionsStore.setState({ sessions: [], loaded: false });
});

describe("loopbackTarget", () => {
  it("accepts http loopback hosts and keeps the path", () => {
    expect(loopbackTarget("http://127.0.0.1:8080/hoge/moga.html?a=1#x")).toEqual({ port: 8080, path: "/hoge/moga.html?a=1#x" });
    expect(loopbackTarget("http://localhost:8080/y")).toEqual({ port: 8080, path: "/y" });
    expect(loopbackTarget("http://0.0.0.0:8000/")).toEqual({ port: 8000, path: "/" });
  });
  it("rejects https, the Agent port and other hosts", () => {
    expect(loopbackTarget("https://localhost:8443/")).toBeNull();
    expect(loopbackTarget("http://localhost:7700/")).toBeNull();
    expect(loopbackTarget("http://example.com:8080/")).toBeNull();
    expect(loopbackTarget("http://localhost.example.com:8080/")).toBeNull();
  });
});

describe("MarkdownView previewLinks", () => {
  const src = [
    "http://127.0.0.1:8080/hoge/moga.html",
    "",
    "[x](http://localhost:8081/y)",
    "",
    "<http://0.0.0.0:8082/z>",
  ].join("\n");

  it("wires autolinks, [text](url) and <url> to the preview without touching the text", async () => {
    await render(src, { previewLinks: true });
    expect(pv().map((a) => a.dataset.pvPort)).toEqual(["8080", "8081", "8082"]);
    expect(pv()[0].textContent).toBe("http://127.0.0.1:8080/hoge/moga.html");
    expect(pv()[1].textContent).toBe("x");
    expect(pv()[0].getAttribute("href")).toBe(previewURL(8080, "/hoge/moga.html"));
    expect(pv()[0].getAttribute("target")).toBeNull();
    expect(pv()[0].title).toContain("http://127.0.0.1:8080/hoge/moga.html");
  });

  it("leaves them ordinary external links without the opt-in (the shared view)", async () => {
    await render(src);
    expect(pv()).toHaveLength(0);
    expect(links().every((a) => a.classList.contains("ext-link") && a.target === "_blank")).toBe(true);
    expect(links()[0].getAttribute("href")).toBe("http://127.0.0.1:8080/hoge/moga.html");
  });

  it("does not gate on onOpenFile", async () => {
    await render("http://localhost:3000/", { previewLinks: true });
    expect(pv()).toHaveLength(1);
  });

  it("keeps https loopback, port 7700 and other hosts as external links", async () => {
    await render("[a](https://localhost:8443/) [b](http://localhost:7700/) [c](http://example.com:8080/)", { previewLinks: true });
    expect(pv()).toHaveLength(0);
    expect(links().every((a) => a.classList.contains("ext-link"))).toBe(true);
  });

  it("links inline code that is exactly a loopback URL, keeping the text, but not fenced blocks", async () => {
    await render("`http://localhost:5173/` and `npm run dev` and `see http://localhost:5173/`\n\n```\nhttp://localhost:9000/\n```", { previewLinks: true });
    expect(pv()).toHaveLength(1);
    expect(pv()[0].querySelector("code")?.textContent).toBe("http://localhost:5173/");
    expect(host.querySelector("pre a")).toBeNull();
    // Re-rendering the same source must not stack a second wrapper.
    await render("`http://localhost:5173/` and `npm run dev` and `see http://localhost:5173/`\n\n```\nhttp://localhost:9000/\n```", { previewLinks: true, breaks: true });
    expect(pv()).toHaveLength(1);
    expect(host.querySelectorAll("a a")).toHaveLength(0);
  });

  it("does not link inline code whose URL holds non-ASCII punctuation, but still links letters", async () => {
    await render("`http://127.0.0.1:8080/…` `http://localhost:3000/（例）` `http://localhost:5173/` `http://127.0.0.1:8080/日本語/page.html`", { previewLinks: true });
    expect(pv().map((a) => a.querySelector("code")?.textContent)).toEqual(["http://localhost:5173/", "http://127.0.0.1:8080/日本語/page.html"]);
    expect(host.querySelectorAll("code")).toHaveLength(4);
  });

  it("leaves the attachment action link to its own branch", async () => {
    await render("[b](/open/browser-attachment/abc)", { previewLinks: true });
    expect(host.querySelector("a.action-link")).not.toBeNull();
    expect(pv()).toHaveLength(0);
  });

  it("opens the lightweight preview on a plain click when nothing better is available", async () => {
    useWorkspaceStore.setState({ browserUnavailable: "kubernetes" });
    await render("http://localhost:8080/a?b=1", { previewLinks: true });
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    await act(async () => {
      pv()[0].dispatchEvent(ev);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(ev.defaultPrevented).toBe(true);
    expect(open).toHaveBeenCalledWith(previewURL(8080, "/a?b=1"), "_blank", "noopener");
  });

  it("opens synchronously inside the click handler (popup blockers), for subdomain and lightweight", async () => {
    await render("http://localhost:8080/a", { previewLinks: true });
    // lightweight: no browser pane, no domain
    useWorkspaceStore.setState({ browserUnavailable: "kubernetes" });
    act(() => {
      pv()[0].dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      expect(open).toHaveBeenCalledTimes(1);
    });
    usePreviewStore.setState({
      scope: currentScope(),
      info: { domain: "pv.example.com", urls: { "8080": "https://abc-8080.pv.example.com" }, ports: [8080], tempPorts: [], public: false, tenantShare: false, maxPorts: 8 },
    });
    act(() => {
      pv()[0].dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      expect(open).toHaveBeenLastCalledWith("https://abc-8080.pv.example.com/a", "_blank", "noopener");
    });
    act(() => {
      pv()[0].dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }));
      expect(openTargetInNew).toHaveBeenCalledTimes(0); // pane unavailable: the default opens instead
      expect(open).toHaveBeenCalledTimes(3);
    });
  });

  it("opens the pane on a plain click by default, and a new layout tab on Ctrl/⌘ and middle click", async () => {
    await render("http://localhost:8080/a", { previewLinks: true });
    await click(pv()[0]);
    // Without a preview domain the pane is the default target.
    expect(openTarget).toHaveBeenCalledWith({ content: { kind: "browser", port: 8080, path: "/a" } });
    await click(pv()[0], { ctrlKey: true });
    expect(openTargetInNew).toHaveBeenLastCalledWith({ content: { kind: "browser", port: 8080, path: "/a" } }, true);
    await act(async () => {
      pv()[0].dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(openTargetInNew).toHaveBeenCalledTimes(2);
    expect(open).not.toHaveBeenCalled();
  });

  it("opens the subdomain of a listed port when the preview state is held", async () => {
    usePreviewStore.setState({
      scope: currentScope(),
      info: { domain: "pv.example.com", urls: { "3000": "https://abc-3000.pv.example.com" }, ports: [3000], tempPorts: [], public: false, tenantShare: false, maxPorts: 8 },
    });
    await render("http://localhost:3000/p/q", { previewLinks: true });
    await click(pv()[0]);
    expect(open).toHaveBeenCalledWith("https://abc-3000.pv.example.com/p/q", "_blank", "noopener");
  });

  it("reports a stopped workspace instead of opening anything", async () => {
    await render("http://localhost:8080/", { previewLinks: true });
    useWorkspaceStore.setState({ state: "stopped" });
    await click(pv()[0]);
    expect(toasts).toEqual([t("view.preview_link.not_running")]);
    expect(open).not.toHaveBeenCalled();
    expect(openTarget).not.toHaveBeenCalled();
  });

  it("reads the preview state when a document has such a link and none is held", async () => {
    await render("http://localhost:8080/", { previewLinks: true });
    expect(refresh).toHaveBeenCalled();
    refresh.mockClear();
    await render("no link here", { previewLinks: true });
    expect(refresh).not.toHaveBeenCalled();
  });

  it("opens the port menu on right-click under a host, and keeps the native menu without one", async () => {
    await render("http://localhost:8080/", { previewLinks: true }, true);
    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 });
    await act(async () => void pv()[0].dispatchEvent(ev));
    // The menu component is lazy; wait for it to land.
    await act(async () => void (await import("../preview/LinkPortMenu.tsx")));
    expect(ev.defaultPrevented).toBe(true);
    expect(document.body.querySelector(".port-menu")).not.toBeNull();
    expect(document.body.querySelectorAll(".port-menu-item").length).toBeGreaterThanOrEqual(4);
    await act(async () => root.render(<div />));
    const plain = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    await render("http://localhost:8080/", { previewLinks: true });
    await act(async () => void pv()[0].dispatchEvent(plain));
    expect(plain.defaultPrevented).toBe(false);
  });

  it("dims a port nobody listens on, reactively, and still opens it", async () => {
    await render("http://localhost:8080/ http://localhost:9090/", { previewLinks: true });
    // The session list has not loaded: nothing is dimmed on a guess.
    expect(pv().some((a) => a.classList.contains("pv-idle"))).toBe(false);
    await act(async () => {
      useSessionsStore.setState({ loaded: true, sessions: [{ name: "s", alive: true, ports: [8080] } as never] });
    });
    expect(pv().map((a) => a.classList.contains("pv-idle"))).toEqual([false, true]);
    expect(pv()[1].title).toContain(t("view.preview_link.idle"));
    const before = pv()[0];
    await act(async () => {
      useSessionsStore.setState({ sessions: [{ name: "s", alive: true, ports: [8080, 9090] } as never] });
    });
    expect(pv().map((a) => a.classList.contains("pv-idle"))).toEqual([false, false]);
    expect(pv()[0]).toBe(before); // relinked in place, not re-parsed
    await act(async () => {
      useSessionsStore.setState({ sessions: [] });
    });
    await click(pv()[1]);
    expect(openTarget).toHaveBeenCalled();
  });

  describe("inline code of an idle port (#2426)", () => {
    const code = "`http://localhost:8080/` `http://localhost:9090/`";
    const setSessions = (ports: number[] | null) =>
      act(async () => {
        useSessionsStore.setState(ports === null ? { loaded: false, sessions: [] } : { loaded: true, sessions: [{ name: "s", alive: true, ports } as never] });
      });
    const linked = () => pv().map((a) => a.dataset.pvPort);
    const codes = () => [...host.querySelectorAll("code")].map((c) => c.textContent);

    it("links every loopback code while the session list has not loaded", async () => {
      await render(code, { previewLinks: true });
      expect(linked()).toEqual(["8080", "9090"]);
    });

    it("leaves code of an absent port plain once loaded, and links the listened one", async () => {
      await setSessions([8080]);
      await render(code, { previewLinks: true });
      expect(linked()).toEqual(["8080"]);
      expect(codes()).toEqual(["http://localhost:8080/", "http://localhost:9090/"]);
      expect(host.querySelector("code")!.closest("a")).not.toBeNull();
      expect(host.querySelectorAll("code")[1].closest("a")).toBeNull();
    });

    it("unwraps when the list loads later, relinks when the port appears, unwraps when it goes", async () => {
      await render(code, { previewLinks: true });
      expect(linked()).toEqual(["8080", "9090"]);
      await setSessions([8080]);
      expect(linked()).toEqual(["8080"]);
      const first = pv()[0];
      await setSessions([8080, 9090]);
      expect(linked()).toEqual(["8080", "9090"]);
      expect(pv()[0]).toBe(first); // untouched by the update
      expect(host.querySelectorAll("a a")).toHaveLength(0);
      await setSessions([]);
      expect(linked()).toEqual([]);
      expect(codes()).toEqual(["http://localhost:8080/", "http://localhost:9090/"]);
      await setSessions([9090]);
      await click(pv()[0]);
      expect(openTarget).toHaveBeenCalledTimes(1); // the relinked anchor is wired once
    });

    it("keeps a bare autolink, only dimmed", async () => {
      await setSessions([]);
      await render("http://localhost:8080/", { previewLinks: true });
      expect(pv()).toHaveLength(1);
      expect(pv()[0].classList.contains("pv-idle")).toBe(true);
    });
  });
});
