import { afterEach, describe, expect, it, vi } from "vitest";

const opened = vi.hoisted(() => ({
  calls: [] as Array<[string, boolean]>,
  menu: null as null | ((name: string, x: number, y: number) => void),
}));
vi.mock("../../sessions/SessionLinkMenu.tsx", () => ({ useSessionLinkMenu: () => opened.menu }));
vi.mock("../../sessions/open.ts", async (orig) => ({
  ...(await orig<typeof import("../../sessions/open.ts")>()),
  openSessionChat: (n: string) => opened.calls.push([n, false]),
  openSessionChatSplit: (n: string) => opened.calls.push([n, true]),
}));

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { answerConfirm, confirmDialog } from "../../../test/confirmDialog.ts";
import { WatchesPanel } from "./WatchesPanel.tsx";
import { t } from "../../../lib/i18n/index.ts";
import { useWorkItemStore } from "../../workitems/store.ts";
import type { WorkItem } from "../../workitems/read.ts";
import { useWorkItemModal } from "../../workitems/modal.ts";
import type { SessionWatch } from "../../../core/api/client.ts";
import { useSessionsStore } from "../../sessions/store.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  useWorkItemModal.getState().close();
  useWorkItemStore.setState({ payload: null });
  useSessionsStore.setState({ sessions: [] });
  opened.calls = [];
  opened.menu = null;
  act(() => root?.unmount());
  vi.useRealTimers();
  host?.remove();
  root = null;
  host = null;
});

const now = Date.parse("2026-11-12T10:00:00Z");
const pr: SessionWatch = {
  id: "w1",
  source: "github",
  ref: "o/r#1",
  types: ["pr.checks.completed", "pr.merged"],
  url: "https://github.com/o/r/pull/1",
};
const timer: SessionWatch = { id: "w2", source: "time", ref: "nightly gate", types: ["time.fired"], due: "2026-11-12T10:10:00Z" };
const run: SessionWatch = { id: "w3", source: "github_actions", ref: "o/r:run/55", types: ["ci.run.completed"], error: "HTTP 502" };

function mount(watches: SessionWatch[], onEnd?: (w: SessionWatch) => void, ending: ReadonlySet<string> = new Set(), liveClock = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ConfirmProvider>
        <WatchesPanel watches={watches} ending={ending} onEnd={onEnd} now={liveClock ? undefined : now} />
      </ConfirmProvider>,
    ),
  );
}
const rows = () => Array.from(document.querySelectorAll<HTMLLIElement>(".mw-item"));

describe("WatchesPanel", () => {
  it("renders nothing without watches", () => {
    mount([]);
    expect(document.querySelector(".mirror-watches")).toBeNull();
  });

  it("shows a PR, a timer and an Actions run as three rows", () => {
    mount([pr, timer, run], vi.fn());
    expect(rows()).toHaveLength(3);
    expect(rows()[0].textContent).toContain(`${t("mirror.watch_type_checks_completed")} · ${t("mirror.watch_type_merged")}`);
    expect(rows()[0].querySelector(".codicon-git-pull-request")).not.toBeNull();
    expect(rows()[1].textContent).toContain(t("mirror.watch_in", { time: "10m" }));
    expect(rows()[1].querySelector(".codicon-clock")).not.toBeNull();
    expect(rows()[2].classList.contains("failing")).toBe(true);
    expect(rows()[2].querySelector(".mw-warn")?.textContent).toContain("HTTP 502");
    expect(rows()[0].classList.contains("failing")).toBe(false);
  });

  it("links only a github.com or bitbucket.org URL and renders ref as text", () => {
    const evil: SessionWatch = { id: "w9", source: "ext.x", ref: "<img src=x onerror=alert(1)>", types: [], url: "https://evil.example/x" };
    mount([pr, evil], vi.fn());
    expect(rows()[0].querySelector("a")?.getAttribute("href")).toBe("https://github.com/o/r/pull/1");
    expect(rows()[1].querySelector("a")).toBeNull();
    expect(rows()[1].querySelector("img")).toBeNull();
    expect(rows()[1].querySelector(".mw-ref")?.textContent).toBe("<img src=x onerror=alert(1)>");
  });

  it("hides the end button for a read-only viewer but keeps the rows", () => {
    mount([pr, timer]);
    expect(rows()).toHaveLength(2);
    expect(document.querySelector(".mw-end")).toBeNull();
  });

  it("ends only after the member confirms", async () => {
    const onEnd = vi.fn();
    mount([pr], onEnd);
    const btn = document.querySelector<HTMLButtonElement>(".mw-end")!;
    await act(async () => btn.click());
    expect(confirmDialog()!.querySelector("pre.ui-confirm-code")?.textContent).toBe("o/r#1");
    await answerConfirm(false);
    expect(onEnd).not.toHaveBeenCalled();
    await act(async () => btn.click());
    await answerConfirm(true);
    expect(onEnd).toHaveBeenCalledExactlyOnceWith(pr);
  });

  it("does not call for a watch that went while the dialog was open", async () => {
    const onEnd = vi.fn();
    mount([pr], onEnd);
    await act(async () => document.querySelector<HTMLButtonElement>(".mw-end")!.click());
    act(() =>
      root!.render(
        <ConfirmProvider>
          <WatchesPanel watches={[timer]} ending={new Set()} onEnd={onEnd} now={now} />
        </ConfirmProvider>,
      ),
    );
    await answerConfirm(true);
    expect(onEnd).not.toHaveBeenCalled();
  });

  it("disables the button while that watch is being ended", () => {
    mount([pr], vi.fn(), new Set(["w1"]));
    expect(document.querySelector<HTMLButtonElement>(".mw-end")!.disabled).toBe(true);
  });

  it("ticks a timer down on its own clock", () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    mount([timer], vi.fn(), new Set(), true);
    expect(rows()[0].textContent).toContain(t("mirror.watch_in", { time: "10m" }));
    act(() => vi.advanceTimersByTime(5 * 60_000));
    expect(rows()[0].textContent).toContain(t("mirror.watch_in", { time: "5m" }));
    act(() => vi.advanceTimersByTime(6 * 60_000));
    expect(rows()[0].textContent).toContain(t("mirror.watch_due"));
  });

  describe("ticket links (#4010)", () => {
    const click = (a: Element, init: MouseEventInit = {}) => {
      const ev = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init });
      act(() => {
        a.dispatchEvent(ev);
      });
      return ev;
    };
    const link = (i: number) => rows()[i].querySelector<HTMLAnchorElement>("a.mw-ref")!;

    it("opens the work item modal on a plain click and keeps the URL on the anchor", () => {
      mount([pr]);
      expect(link(0).getAttribute("href")).toBe("https://github.com/o/r/pull/1");
      const ev = click(link(0));
      expect(ev.defaultPrevented).toBe(true);
      const detail = useWorkItemModal.getState().detail;
      expect(detail?.item).toMatchObject({ provider: "github", key: "o/r#1" });
      expect(detail?.reference).toBe(true);
    });

    it("keeps the linked pull request URL on the stand-in of an uncached ticket", () => {
      mount([pr]);
      click(link(0));
      const detail = useWorkItemModal.getState().detail;
      expect(detail?.reference).toBe(true);
      expect(detail?.item.url).toBe("https://github.com/o/r/pull/1");
    });

    it("opens the cached row, read at click time, when the ticket is in the inbox", () => {
      mount([pr]);
      const row = { id: "x", queryId: "q", provider: "github", kind: "pr", key: "o/r#1", title: "Cached PR", state: "open", url: "https://github.com/o/r/pull/1", assignee: "", labels: [], labelColors: {}, repo: "o/r", updatedAt: "" } as unknown as WorkItem;
      // Arrives after the panel was drawn: the lookup must not be frozen at render.
      act(() => useWorkItemStore.setState({ payload: { items: [row], queries: [], fetchedAt: "", running: false, sessions: [] } }));
      click(link(0));
      const detail = useWorkItemModal.getState().detail;
      expect(detail?.reference).toBe(false);
      expect(detail?.item.title).toBe("Cached PR");
    });

    it("opens a Bitbucket pull request the same way", () => {
      mount([{ ...pr, source: "bitbucket", ref: "o/r#2", url: "https://bitbucket.org/o/r/pull-requests/2" }]);
      click(link(0));
      expect(useWorkItemModal.getState().detail?.item).toMatchObject({ provider: "bitbucket", key: "o/r#2" });
    });

    it("leaves a Ctrl/Cmd/Shift click to the tracker page", () => {
      mount([pr]);
      for (const init of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }]) {
        expect(click(link(0), init).defaultPrevented).toBe(false);
      }
      expect(useWorkItemModal.getState().detail).toBeNull();
    });

    it("keeps the external link for a URL that names no ticket", () => {
      mount([{ ...pr, url: "https://github.com/o/r/compare/a...b" }]);
      expect(click(link(0)).defaultPrevented).toBe(false);
      expect(useWorkItemModal.getState().detail).toBeNull();
    });

    it("keeps a CI run row an external link", () => {
      mount([{ ...run, url: "https://github.com/o/r/actions/runs/55" }]);
      expect(link(0).target).toBe("_blank");
      expect(click(link(0)).defaultPrevented).toBe(false);
      expect(useWorkItemModal.getState().detail).toBeNull();
    });
  });

  describe("a fleet watch's session", () => {
    const fleet: SessionWatch = { id: "w7", source: "fleet", ref: "s4uyskj", types: ["fleet.session.idle"] };
    const session = { name: fleet.ref, kind: "claude", title: "Review checkout", branch: "main" } as const;

    it("links only the slug and shows the display name and launch branch beside it", () => {
      useSessionsStore.setState({ sessions: [session] });
      mount([fleet], vi.fn());
      const row = rows()[0];
      const a = row.querySelector<HTMLAnchorElement>("a.md-session-link")!;
      expect(a.textContent).toBe("s4uyskj");
      expect(row.querySelectorAll("a, [role=link]")).toHaveLength(1);
      expect(row.textContent).toMatch(/s4uyskj\s*Review checkout\s*main\s*fleet\.session\.idle/);
      expect(a.classList.contains("mw-ref")).toBe(true);
      expect(a.tabIndex).toBe(0);
      act(() => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
      act(() => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true })));
      act(() => a.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
      expect(opened.calls).toEqual([
        ["s4uyskj", false],
        ["s4uyskj", true],
        ["s4uyskj", false],
      ]);
    });

    it("updates the name and branch from the catalog without replacing the slug link", () => {
      useSessionsStore.setState({ sessions: [session] });
      mount([fleet]);
      const link = rows()[0].querySelector("a");
      act(() => useSessionsStore.setState({
        sessions: [{ ...session, title: "", label: "[AF:s4uyskj] Review queue", currentBranch: "fix/queue" }],
      }));
      expect(rows()[0].querySelector("a")).toBe(link);
      expect(rows()[0].textContent).toMatch(/s4uyskj\s*Review queue\s*fix\/queue\s*fleet\.session\.idle/);
      expect(rows()[0].textContent).not.toContain("main");
      expect(rows()[0].textContent).not.toContain("Review checkout");
    });

    it("shows no branch when neither branch field is set", () => {
      useSessionsStore.setState({ sessions: [{ ...session, branch: "" }] });
      mount([fleet]);
      expect(rows()[0].textContent).toMatch(/^s4uyskj\s*Review checkout\s*fleet\.session\.idle$/);
    });

    it("leaves a missing target as plain text even when other sessions are present", () => {
      useSessionsStore.setState({ sessions: [{ ...session, name: "sotherx" }] });
      mount([fleet], vi.fn());
      expect(rows()[0].querySelector("a")).toBeNull();
      expect(rows()[0].querySelector(".mw-ref")?.textContent).toBe("s4uyskj");
      expect(rows()[0].textContent).toMatch(/^s4uyskj\s*fleet\.session\.idle/);
    });

    it("follows the session appearing and disappearing while the row stays", () => {
      mount([fleet], vi.fn());
      act(() => useSessionsStore.setState({ sessions: [session] }));
      expect(rows()[0].querySelectorAll("a.md-session-link")).toHaveLength(1);
      expect(rows()[0].textContent).toContain("Review checkout");
      act(() => useSessionsStore.setState({ sessions: [] }));
      expect(rows()[0].querySelector("a")).toBeNull();
      expect(rows()[0].querySelector(".mw-ref")?.textContent).toBe("s4uyskj");
      expect(rows()[0].textContent).toMatch(/^s4uyskj\s*fleet\.session\.idle/);
      act(() => useSessionsStore.setState({ sessions: [session] }));
      const links = rows()[0].querySelectorAll("a.md-session-link");
      expect(links).toHaveLength(1);
      expect(links[0].textContent).toBe("s4uyskj");
    });

    it("opens the session menu on a right click under a host", () => {
      useSessionsStore.setState({ sessions: [session] });
      const menu = vi.fn();
      opened.menu = menu;
      mount([fleet]);
      const a = rows()[0].querySelector<HTMLAnchorElement>("a.md-session-link")!;
      act(() => a.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 6 })));
      expect(menu).toHaveBeenCalledWith("s4uyskj", 5, 6);
    });
  });
});
