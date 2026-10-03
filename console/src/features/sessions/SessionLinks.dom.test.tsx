// Render test for a rail row's link line (#1062): the PR of the session's branch and the ports
// its processes listen on, each opening the browser pane.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { SessionRow } = await import("./SessionRow.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
type Session = import("../../types/session.ts").Session;

let root: Root | null = null;
let host: HTMLDivElement;
const openTarget = vi.fn();
const openTargetInNew = vi.fn();
const original = { openTarget: useLayoutStore.getState().openTarget, openTargetInNew: useLayoutStore.getState().openTargetInNew };

const render = async (over: Partial<Session>, running = true): Promise<void> => {
  const s: Session = { name: "s1", kind: "claude", alive: true, state: "idle", title: "作業", ...over };
  await act(async () => {
    root!.render(
      <ul>
        <SessionRow s={s} selected={false} opens={[]} multi={false} running={running} readOnly />
      </ul>,
    );
  });
};

const line = () => host.querySelector<HTMLElement>(".sess-links");
const prLink = () => host.querySelector<HTMLAnchorElement>("a.sess-pr");
const ports = () => [...host.querySelectorAll<HTMLButtonElement>(".sess-port")];

beforeEach(() => {
  localStorage.clear();
  openTarget.mockReset();
  openTargetInNew.mockReset();
  useLayoutStore.setState({ openTarget, openTargetInNew });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
  useLayoutStore.setState(original);
});

describe("SessionRow link line", () => {
  it("stays a one-line row when there is nothing to link", async () => {
    await render({});
    expect(line()).toBeNull();
    expect(host.querySelector("li")?.className).not.toContain("sess-has-links");
  });

  it("links the branch's open PR with its CI state", async () => {
    await render({ pr: { number: 1062, state: "open", url: "https://github.com/o/r/pull/1062", checks: "failure" } });
    const a = prLink()!;
    expect(a.getAttribute("href")).toBe("https://github.com/o/r/pull/1062");
    expect(a.target).toBe("_blank");
    expect(a.rel).toContain("noopener");
    expect(a.className).toContain("pr-open");
    expect(a.textContent).toContain("#1062");
    expect(a.querySelector(".sess-pr-ci.ci-failure")).not.toBeNull();
    expect(a.title).toBe(
      t("srow.pr_title", { n: 1062, state: t("srow.pr_state.open"), checks: t("srow.pr_checks.failure") }),
    );
    expect(host.querySelector("li")?.className).toContain("sess-has-links");
  });

  it("names a draft and a merged PR, and drops CI once merged", async () => {
    await render({ pr: { number: 7, state: "open", draft: true, url: "https://github.com/o/r/pull/7" } });
    expect(prLink()?.className).toContain("pr-draft");
    expect(prLink()?.querySelector(".sess-pr-ci")).toBeNull(); // no checks is not green
    await render({ pr: { number: 7, state: "merged", url: "https://github.com/o/r/pull/7", checks: "failure" } });
    expect(prLink()?.className).toContain("pr-merged");
    expect(prLink()?.querySelector(".sess-pr-ci")).toBeNull();
  });

  it("refuses a PR URL that is not a GitHub page", async () => {
    await render({ pr: { number: 7, state: "open", url: "javascript:alert(1)" } });
    expect(line()).toBeNull();
  });

  it("opens a listening port in the browser pane, Ctrl-click in a new one", async () => {
    await render({ ports: [5173, 7700, 8080] });
    // 7700 is the Agent's own port, which the browser pane refuses.
    expect(ports().map((b) => b.textContent)).toEqual([":5173", ":8080"]);
    expect(ports()[0].title).toBe(t("srow.port_title", { port: 5173 }));
    await act(async () => ports()[0].click());
    expect(openTarget).toHaveBeenCalledWith({ content: { kind: "browser", port: 5173, path: "/" } });
    await act(async () => {
      ports()[1].dispatchEvent(new MouseEvent("click", { bubbles: true, ctrlKey: true }));
    });
    expect(openTargetInNew).toHaveBeenCalledWith({ content: { kind: "browser", port: 8080, path: "/" } }, true);
  });

  it("folds ports past the third behind +N, which unfolds them", async () => {
    await render({ ports: [4000, 4001, 4002, 4003, 4004] });
    expect(ports().map((b) => b.textContent)).toEqual([":4000", ":4001", ":4002", "+2"]);
    expect(ports()[3].title).toBe(t("srow.ports_more", { n: 2 }));
    await act(async () => ports()[3].click());
    expect(ports().map((b) => b.textContent)).toEqual([":4000", ":4001", ":4002", ":4003", ":4004"]);
    expect(openTarget).not.toHaveBeenCalled();
  });

  it("offers no port of a stopped session or a stopped Workspace", async () => {
    await render({ alive: false, ports: [5173] });
    expect(ports()).toHaveLength(0);
    await render({ ports: [5173] }, false);
    expect(ports()).toHaveLength(0);
  });
});
