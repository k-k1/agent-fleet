// A folder-scoped archive shelf also lists the archived children of that folder's sessions,
// which normally live in worktrees of their own; a session-scoped one (opened from a past-session
// search hit) lists that one session. The api client is the only thing swapped out.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const raw = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (...a: unknown[]) => api(...a), raw: (...a: unknown[]) => raw(...a) };
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
  raw.mockReset();
  useSessionsStore.setState({ sessions: [] });
});

const shown = async (live: Session[], archived: Session[], props: { dir?: string; session?: string } = { dir: DIR }) => {
  useSessionsStore.setState({ sessions: live });
  api.mockResolvedValue({ sessions: archived });
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ArchivedModal {...props} />
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

const bulk = (label: RegExp) =>
  [...document.querySelectorAll<HTMLButtonElement>(".ui-modal button")].find((b) => label.test(b.textContent || ""));

describe("ArchivedModal session scope", () => {
  // Same title on purpose: the search hit's title alone cannot pick the row.
  const shelf = [
    s("abc1234", { title: "Review", dir: "/home/dev/repos/app@wip-1" }),
    s("def5678", { title: "Review", dir: "/home/dev/repos/app@wip-2" }),
  ];

  it("lists only the named session, and Show all brings the rest back", async () => {
    const names = await shown([], shelf, { session: "def5678" });
    expect(names).toEqual(["Review"]);
    expect(document.querySelector(".arch-info")?.getAttribute("title")).toBe("ID: def5678");
    const chip = document.querySelector(".arch-scope");
    expect(chip?.textContent).toMatch(/Review/);
    const clear = [...chip!.querySelectorAll("button")].find((b) => /すべて表示|Show all/.test(b.textContent || ""));
    await act(async () => clear!.click());
    expect(document.querySelectorAll(".arch-row").length).toBe(2);
  });

  it("shows no rows, not the whole shelf, once the session is no longer archived", async () => {
    const names = await shown([], shelf, { session: "gone999" });
    expect(names).toEqual([]);
    expect(document.querySelector(".arch-scope")?.textContent).toMatch(/gone999/);
    // Nothing on screen, so nothing for the bulk actions either.
    expect(bulk(/すべて復帰|Restore all/)?.disabled).toBe(true);
    expect(bulk(/古いものを削除|Delete old/)?.disabled).toBe(true);
  });

  it("counts only the narrowed session's old archive for Delete old", async () => {
    const old = "2020-01-01T00:00:00Z";
    await shown([], shelf.map((x) => ({ ...x, createdAt: old })), { session: "def5678" });
    expect(bulk(/古いものを削除|Delete old/)?.textContent).toMatch(/[(（]1[)）]/);
  });

  it("restores only the narrowed session from Restore all", async () => {
    await shown([], shelf, { session: "def5678" });
    raw.mockResolvedValue({ ok: true });
    await act(async () => bulk(/すべて復帰|Restore all/)!.click());
    expect(raw.mock.calls.map((c) => c[0])).toEqual(["api/sessions/def5678/restore"]);
  });
});
