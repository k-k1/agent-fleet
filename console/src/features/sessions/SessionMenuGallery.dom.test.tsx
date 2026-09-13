// "Generated images (N)" in the session context menu (ADR 0080 decision 8).
//
// The item exists because the folder generate_image writes into is named after the session's
// UUID: no one finds it in the file tree. Both the count and the path ride the session wire,
// and an older Agent — the Agent ships separately from the Console — sends neither. So the
// rule under test is that the item is decided by the DATA and nothing else: no capability
// probe, no version compare, no request on click. With no count there is nothing to show,
// and showing it anyway would open an empty pane on a path the Console invented.
//
// Menu items are addressed by icon, not by label: the labels come from the catalogue.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const opened: { path: string; opts?: { focus?: string; session?: string; newPane?: boolean } }[] = [];
vi.mock("../gallery/open.ts", () => ({
  openGallery: (path: string, opts?: { focus?: string; session?: string; newPane?: boolean }) =>
    opened.push({ path, opts }),
}));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

// Nothing in this test presses an action item; the menu only needs the object to exist.
const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;

let root: Root | null = null;
let host: HTMLDivElement;

const render = async (over: Partial<Session>): Promise<void> => {
  const s: Session = { name: "sk7f3q9", kind: "claude", alive: true, state: "idle", title: "絵を描く", ...over };
  await act(async () => {
    root!.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};

const item = () =>
  document.querySelector<HTMLElement>(".sess-menu .codicon-file-media")?.closest("button") ?? null;

beforeEach(() => {
  opened.length = 0;
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

describe("セッションメニューの「生成した画像」", () => {
  it("枚数とパスが届いていれば出て、枚数を見せ、そのフォルダを開く", async () => {
    await render({ generatedImages: 12, generatedImagesPath: ".cache/agent-fleet/generated/uuid-1" });
    const btn = item();
    expect(btn).not.toBeNull();
    expect(btn!.textContent).toContain("12");
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    // The session travels for the pane title only: the folder's own name is a UUID, so the
    // title has to come from somewhere a reader recognises.
    expect(opened).toEqual([
      {
        path: ".cache/agent-fleet/generated/uuid-1",
        opts: { session: "絵を描く", newPane: false },
      },
    ]);
  });

  it("フィールドが無ければ出ない（古い Agent・停止中のワークスペース）", async () => {
    await render({});
    expect(item()).toBeNull();
  });

  it("枚数 0 でも出ない", async () => {
    await render({ generatedImages: 0, generatedImagesPath: ".cache/agent-fleet/generated/uuid-1" });
    expect(item()).toBeNull();
  });

  // A count with no path cannot be opened: the path is the only thing that says where.
  it("パスだけ欠けていても出ない", async () => {
    await render({ generatedImages: 3 });
    expect(item()).toBeNull();
  });

  it("Ctrl/⌘ を押しながらなら別のペインに開く", async () => {
    await render({ generatedImages: 1, generatedImagesPath: "g/uuid-1" });
    await act(async () => {
      item()!.dispatchEvent(new MouseEvent("click", { bubbles: true, metaKey: true }));
    });
    expect(opened[0].opts?.newPane).toBe(true);
  });
});
