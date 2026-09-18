// What the modal actually SENDS per row. The grading itself is covered by stopTree.test.ts;
// this pins the half that touches the workspace, because the two endpoints are not
// interchangeable: /halt kills the pane now, /stop-after-turn only leaves a flag behind. A
// row that took the wrong one either loses a running turn or never stops at all, and both
// read as "it worked" on screen.
//
// Only the api client and the sessions store's refresh are swapped out — the plan, the
// component's own state and the catalogue are real.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const raw = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, raw: (...a: unknown[]) => raw(...a) };
});

const { StopSessionsModal } = await import("./StopSessionsModal.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
type Repo = import("./store.ts").Repo;
type Session = import("../../types/session.ts").Session;
type RepoTreeNode = import("../../lib/project.ts").RepoTreeNode;

const ok = () => ({ ok: true, json: async () => ({}) });

const repo = (name: string, over: Partial<Repo> = {}): Repo => ({
  name,
  worktree: true,
  parent: "app",
  branch: "temp/" + name.split("@")[1],
  ...over,
});
const node = (r: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo: r, children, spine: "" });
const sess = (name: string, folder: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  repo: folder,
  alive: true,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement;
let done: { now: number; after: number } | null = null;

const render = async (n: RepoTreeNode, sessions: Session[]) => {
  // Both dialogs portal to <body>, so a leftover mount would make every query read the
  // wrong one (the delete modal's test learned this).
  if (root) {
    await act(async () => root?.unmount());
    host.remove();
  }
  useSessionsStore.setState({ sessions });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StopSessionsModal node={n} onClose={() => {}} onDone={(now, after) => (done = { now, after })} />
      </ToastProvider>,
    );
  });
};

/** The dialog portals to <body>, so query there rather than in the host div. */
const rows = () => [...document.querySelectorAll<HTMLInputElement>(".wcstop-head input")];
const rowFor = (name: string) =>
  rows()[[...document.querySelectorAll(".wcstop-name")].findIndex((el) => el.textContent === name)];
const forceBox = () => document.querySelector<HTMLInputElement>(".wcstop-opt input");
const click = async (el: HTMLElement | undefined | null) => {
  await act(async () => el?.click());
};
const runButton = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".wcstop-foot-actions button")].at(-1)!;
/** Calls as "METHOD url" so an assertion reads like the request log. */
const sent = () => raw.mock.calls.map((c) => `${(c[1] as { method: string }).method} ${c[0]}`);

beforeEach(() => {
  raw.mockReset();
  raw.mockResolvedValue(ok());
  done = null;
  // The run refreshes the list (twice — the second on a timer), and a real refresh would
  // reach for the network after the test has ended.
  useSessionsStore.setState({ refresh: async () => {} });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  useSessionsStore.setState({ sessions: [] });
});

describe("配下のセッション一括停止モーダル", () => {
  it("待機中は halt、実行中は stop-after-turn（既定ではターンを切らない）", async () => {
    await render(node(repo("app@a"), [node(repo("app@b"))]), [
      sess("s1", "app@a"),
      sess("s2", "app@b", { state: "working", title: "busy one" }),
    ]);
    await click(runButton());
    expect(sent()).toEqual([
      "POST api/sessions/s1/halt",
      "POST api/sessions/s2/stop-after-turn",
    ]);
    expect(JSON.parse((raw.mock.calls[1][1] as { body: string }).body)).toEqual({ on: true });
    expect(done).toEqual({ now: 1, after: 1 });
  });

  it("「実行中もすぐ停止」を入れると、実行中の行も halt になる", async () => {
    await render(node(repo("app@a")), [sess("s1", "app@a", { state: "working" })]);
    await click(forceBox());
    await click(runButton());
    expect(sent()).toEqual(["POST api/sessions/s1/halt"]);
    expect(done).toEqual({ now: 1, after: 0 });
  });

  it("shell と固定中のセッションは既定で送らず、チェックすれば送る", async () => {
    await render(node(repo("app@a")), [
      sess("sh", "app@a", { kind: "shell", title: "a shell" }),
      sess("s1", "app@a", { title: "an agent" }),
    ]);
    await click(runButton());
    expect(sent()).toEqual(["POST api/sessions/s1/halt"]);

    raw.mockClear();
    await render(node(repo("app@a")), [
      sess("sh", "app@a", { kind: "shell", title: "a shell" }),
      sess("s1", "app@a", { title: "an agent" }),
    ]);
    await click(rowFor("a shell"));
    await click(runButton());
    expect(sent()).toEqual(["POST api/sessions/sh/halt", "POST api/sessions/s1/halt"]);
  });

  it("停止済みのセッションは行にならない（送る先が無い）", async () => {
    await render(node(repo("app@a")), [sess("s1", "app@a", { alive: false }), sess("s2", "app@a")]);
    expect(rows()).toHaveLength(1);
    await click(runButton());
    expect(sent()).toEqual(["POST api/sessions/s2/halt"]);
  });

  it("失敗した行は画面に残り、閉じずに理由を出す", async () => {
    raw.mockImplementation(async (url: string) =>
      url.includes("/s1/") ? { ok: false, json: async () => ({ error: "nope" }) } : ok(),
    );
    await render(node(repo("app@a")), [sess("s1", "app@a", { title: "first" }), sess("s2", "app@a", { title: "second" })]);
    await click(runButton());
    // The run does not abort: the second row is still sent.
    expect(sent()).toEqual(["POST api/sessions/s1/halt", "POST api/sessions/s2/halt"]);
    expect(document.querySelector(".wcstop-result.is-failed")).not.toBeNull();
    expect(done).toBeNull();
  });
});
