// A folder-scoped archive shelf also lists the archived children of that folder's sessions,
// which normally live in worktrees of their own. The api client is the only thing swapped out.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (...a: unknown[]) => api(...a) };
});

const { ArchivedModal } = await import("./ArchivedModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("./store.ts");
type Session = import("../../types/session.ts").Session;

const DIR = "/home/dev/repos/app@wip-parent";
const s = (name: string, over: Partial<Session> = {}): Session => ({ name, kind: "claude", title: name, ...over });

let root: Root | null = null;

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
  api.mockReset();
  useSessionsStore.setState({ sessions: [] });
});

const shown = async (live: Session[], archived: Session[]) => {
  useSessionsStore.setState({ sessions: live });
  api.mockResolvedValue({ sessions: archived });
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ArchivedModal dir={DIR} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  return [...document.querySelectorAll(".arch-name")].map((e) => e.textContent).sort();
};

describe("ArchivedModal folder scope", () => {
  it("takes in archived children and grandchildren of a live parent in the folder", async () => {
    const names = await shown(
      [s("parent", { dir: DIR })],
      [
        s("child", { dir: "/home/dev/repos/app@wip-c1", originSession: "parent" }),
        s("grandchild", { dir: "/home/dev/repos/app@wip-c2", originSession: "child" }),
        s("unrelated", { dir: "/home/dev/repos/app@wip-zz" }),
        s("same-folder", { dir: DIR }),
      ],
    );
    expect(names).toEqual(["child", "grandchild", "same-folder"]);
  });

  it("follows an archived parent too", async () => {
    const names = await shown(
      [],
      [s("parent", { dir: DIR }), s("child", { dir: "/home/dev/repos/app@wip-c1", originSession: "parent" })],
    );
    expect(names).toEqual(["child", "parent"]);
  });
});
