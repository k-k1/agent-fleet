// "Recreate working copy" in the session context menu: a stopped session whose worktree folder
// is gone can only be archived or have the folder put back, and the archive shelf was the only
// place that offered the latter. The item is decided by the row alone — dead, a worktree folder
// ("<repo>@<seg>"), and an Agent to ask — and opens the same RecreateWorktreeModal on that dir.
//
// Menu items are addressed by icon, not by label: the labels come from the catalogue.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const modals: { dir: string; sessions: unknown[] }[] = [];
vi.mock("./RecreateWorktreeModal.tsx", () => ({
  RecreateWorktreeModal: (p: { dir: string; sessions: unknown[] }) => {
    modals.push({ dir: p.dir, sessions: p.sessions });
    return <div className="rwt-stub" />;
  },
}));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;

const WT = "/home/dev/repos/agent-fleet@wip-abc1234";
let root: Root | null = null;
let host: HTMLDivElement;

const render = async (over: Partial<Session>, running = true): Promise<void> => {
  const s: Session = { name: "sk7f3q9", kind: "claude", alive: false, resumable: false, dir: WT, ...over };
  await act(async () => {
    root!.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running={running} open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};

const item = () => document.querySelector<HTMLElement>(".sess-menu .codicon-repo")?.closest("button") ?? null;

beforeEach(() => {
  modals.length = 0;
  localStorage.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
});

describe("セッションメニューの「作業コピーを作り直す」", () => {
  it("worktree が消えた停止中セッションに出て、その dir で作り直しモーダルを開く", async () => {
    await render({});
    const btn = item();
    expect(btn).not.toBeNull();
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(modals.at(-1)).toEqual({ dir: WT, sessions: [] });
  });

  it("フォルダが残っている（再開できる）なら出ない", async () => {
    await render({ resumable: true });
    expect(item()).toBeNull();
  });

  it("動いているセッションには出ない", async () => {
    await render({ alive: true, resumable: undefined });
    expect(item()).toBeNull();
  });

  it("worktree でないフォルダ（親クローン）には出ない", async () => {
    await render({ dir: "/home/dev/repos/agent-fleet" });
    expect(item()).toBeNull();
  });

  it("Agent に届かないときは出ない", async () => {
    await render({}, false);
    expect(item()).toBeNull();
  });
});
