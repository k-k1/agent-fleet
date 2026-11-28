// The device's Back with a session menu open over the schedule detail. backClose counts the
// popstates its own cleanups cause in module state, so this sits in a file of its own: after other
// tests' unmounts a Back pressed here could be taken for one of those echoes.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./api.ts", () => ({ scheduleUpdate: vi.fn(), scheduleApprove: vi.fn() }));
vi.mock("../../core/api/client.ts", () => ({
  api: async (url: string) => (url.includes("agents/kinds") ? { kinds: [{ kind: "codex" }], access: { policyConfirmed: true } } : {}),
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));
// Nothing here presses a menu item; the menu only needs the actions object to exist.
vi.mock("../sessions/useSessionActions.tsx", () => ({
  useSessionActions: () => new Proxy({}, { get: () => async () => {} }),
}));

const { ScheduleDetailModal } = await import("./ScheduleDetailModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root;
let host: HTMLDivElement;
const onClose = vi.fn();
const pinned = "saaaaaa";

const settle = (ms = 30) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));

beforeEach(async () => {
  onClose.mockClear();
  useWorkspaceStore.setState({ state: "running" } as never);
  useSessionsStore.setState({ sessions: [{ name: pinned, kind: "codex", alive: false } as never] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <ToastProvider>
        <ScheduleDetailModal
          s={{ id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", prompt: "hi", enabled: true, agent_kind: "codex", spec_label: "Original", session_mode: "reuse", reuse_target: pinned } as never}
          onClose={onClose}
          onSaved={() => {}}
        />
      </ToastProvider>,
    );
  });
  await settle();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("ScheduleDetailModal session menu and Back", () => {
  it("closes only the menu on the first Back, keeping the draft and the modal", async () => {
    const label = document.querySelector<HTMLInputElement>("#sched-label")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(label, "Draft");
      label.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const link = document.querySelector<HTMLElement>(".sched-dd a.md-session-link")!;
    const entries = history.length;
    await act(async () => void link.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2, clientX: 40, clientY: 50 })));
    expect(document.querySelector(".sess-menu")).not.toBeNull();
    // The menu takes no history entry of its own: an action that navigates or opens a dialog
    // (the gallery, Share) would leave it in front of the entry that action pushes.
    expect(history.length).toBe(entries);

    await act(async () => void history.back());
    await settle();
    expect(document.querySelector(".sess-menu")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector<HTMLInputElement>("#sched-label")?.value).toBe("Draft");

    // The next Back is the modal's own.
    await act(async () => void history.back());
    await settle();
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("closes only the Share dialog on Back, then the modal, with the menu's Share chosen", async () => {
    const label = document.querySelector<HTMLInputElement>("#sched-label")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(label, "Draft");
      label.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const link = document.querySelector<HTMLElement>(".sched-dd a.md-session-link")!;
    await act(async () => void link.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2, clientX: 40, clientY: 50 })));
    const share = [...document.querySelectorAll<HTMLElement>(".sess-menu button")].find((b) => b.textContent?.includes(t("srow.share")))!;
    await act(async () => share.click());
    await settle();
    expect(document.querySelector(".sess-menu")).toBeNull();
    const dialogs = () => document.querySelectorAll(".ui-modal, [role=dialog]").length;
    expect(dialogs()).toBeGreaterThan(1); // the detail and the Share dialog

    await act(async () => void history.back());
    await settle();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector<HTMLInputElement>("#sched-label")?.value).toBe("Draft");
    await act(async () => void history.back());
    await settle();
    expect(onClose).toHaveBeenCalledOnce();
  });
});
