// What the modal actually SENDS, in what order. The plan's grading is covered by
// deleteTree.test.ts; this pins the half that touches the workspace, because the ordering
// is the safety: a session is archived before its folder disappears, a copy is deleted
// before the branch that is checked out in it, and a child goes before its parent.
//
// Only the api client and the layout store are swapped out — the plan, the stores' shapes
// and the modal's own state are real, so "the tick is what sends force=true" is pinned
// against the component the user clicks.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const raw = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, raw: (...a: unknown[]) => raw(...a) };
});
vi.mock("../../layout/store.ts", () => ({
  useLayoutStore: (sel: (s: unknown) => unknown) => sel({ closeSessionPanes: vi.fn() }),
}));

const { DeleteCopyModal } = await import("./DeleteCopyModal.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("./store.ts");
const { useFilesStore } = await import("../files/store.ts");
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
  integration: { relation: "contained", targetUnique: 5, worktreeUnique: 0 },
  ...over,
});
const node = (r: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo: r, children, spine: "" });
const sess = (name: string, folder: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  repo: folder,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement;

const render = async (n: RepoTreeNode, sessions: Session[]) => {
  // Tests that compare "without the tick" against "with it" render twice; both dialogs
  // portal to <body>, so leaving the first mounted would make every query read the wrong one.
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
        <DeleteCopyModal node={n} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};

/** The dialog portals to <body>, so query there rather than in the host div. */
const rows = () => [...document.querySelectorAll<HTMLInputElement>(".wcdel-head input")];
const rowFor = (name: string) =>
  rows()[[...document.querySelectorAll(".wcdel-name")].findIndex((el) => el.textContent === name)];
const optBoxes = () => [...document.querySelectorAll<HTMLInputElement>(".wcdel-opt input")];
const click = async (el: HTMLElement | undefined) => {
  await act(async () => el?.click());
};
const runButton = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".wcdel-foot-actions button")].at(-1)!;
/** Calls as "METHOD url" so an assertion reads like the request log. */
const sent = () => raw.mock.calls.map((c) => `${(c[1] as { method: string }).method} ${c[0]}`);

beforeEach(() => {
  raw.mockReset();
  raw.mockResolvedValue(ok());
  useReposStore.setState({ repos: [] });
  useFilesStore.setState({ ...useFilesStore.getState() });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  useSessionsStore.setState({ sessions: [] });
});

describe("作業コピー削除モーダル", () => {
  it("安全な行だけ既定でチェックが入り、要確認は空、不可は触れない", async () => {
    await render(
      node(repo("app@a"), [
        node(repo("app@b", { dirty: true })),
        node(repo("app@c", { locked: true })),
      ]),
      [],
    );
    expect(rowFor("app@a").checked).toBe(true);
    expect(rowFor("app@b").checked).toBe(false);
    expect(rowFor("app@c").checked).toBe(false);
    expect(rowFor("app@c").disabled).toBe(true);
  });

  it("停止中のセッションをアーカイブしてから、深い方の作業コピーから消す", async () => {
    await render(node(repo("app@a"), [node(repo("app@b"))]), [sess("s1", "app@a"), sess("s2", "app@b")]);
    await click(runButton());
    expect(sent()).toEqual([
      "POST api/sessions/s2/archive",
      "DELETE api/repos/app%40b",
      "POST api/sessions/s1/archive",
      "DELETE api/repos/app%40a",
    ]);
  });

  it("shell は棚に上げず stop で忘れる", async () => {
    await render(node(repo("app@a")), [sess("s1", "app@a", { kind: "shell" })]);
    await click(runButton());
    expect(sent()[0]).toBe("POST api/sessions/s1/stop");
  });

  it("要確認の行はチェックして初めて実行され、force=true が付く", async () => {
    await render(node(repo("app@a"), [node(repo("app@b", { dirty: true }))]), []);
    await click(runButton());
    expect(sent()).toEqual(["DELETE api/repos/app%40a"]);

    raw.mockClear();
    await render(node(repo("app@a"), [node(repo("app@b", { dirty: true }))]), []);
    await click(rowFor("app@b"));
    await click(runButton());
    expect(sent()).toEqual(["DELETE api/repos/app%40b?force=true", "DELETE api/repos/app%40a"]);
  });

  it("セッションを片付けられなかった作業コピーは削除しない（孤児のメタを残さない）", async () => {
    raw.mockImplementation(async (url: string) =>
      url.startsWith("api/sessions/") ? { ok: false, json: async () => ({ error: "nope" }) } : ok(),
    );
    await render(node(repo("app@a")), [sess("s1", "app@a")]);
    await click(runButton());
    expect(sent()).toEqual(["POST api/sessions/s1/archive"]);
    expect(document.querySelector(".wcdel-result.is-failed")).not.toBeNull();
  });

  it("ブランチはチェックした時だけ、作業コピーを消した後に、親の作業コピーで消す", async () => {
    await render(node(repo("app@a")), []);
    await click(runButton());
    expect(sent()).toEqual(["DELETE api/repos/app%40a"]);

    raw.mockClear();
    await render(node(repo("app@a")), []);
    await click(optBoxes()[0]);
    await click(runButton());
    expect(sent()).toEqual(["DELETE api/repos/app%40a", "DELETE api/repos/app/branch?branch=temp%2Fa"]);
  });

  it("リモートも消すのは二段目のチェックだけで、ブランチを外すと一緒に下りる", async () => {
    await render(node(repo("app@a")), []);
    await click(optBoxes()[0]); // branches
    await click(optBoxes()[1]); // remote
    await click(optBoxes()[0]); // branches off — the remote tick must not survive it
    await click(optBoxes()[0]); // branches on again
    await click(runButton());
    expect(sent().at(-1)).toBe("DELETE api/repos/app/branch?branch=temp%2Fa");

    raw.mockClear();
    await render(node(repo("app@a")), []);
    await click(optBoxes()[0]);
    await click(optBoxes()[1]);
    await click(runButton());
    expect(sent().at(-1)).toBe("DELETE api/repos/app/branch?branch=temp%2Fa&remote=1");
  });

  it("worktree を1つでも残す間は本体クローンを消しに行かない", async () => {
    const base = node(repo("app", { worktree: false, parent: undefined, branch: "develop" }), [
      node(repo("app@b")),
      node(repo("app@c", { dirty: true })),
    ]);
    await render(base, []);
    await click(runButton());
    // app@c is left behind (unticked), so the clone itself cannot go this round — and the
    // row says so rather than failing with has_worktrees halfway through the run.
    expect(sent()).toEqual(["DELETE api/repos/app%40b"]);
  });
});
