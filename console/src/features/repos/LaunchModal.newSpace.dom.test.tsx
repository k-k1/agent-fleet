// The new-space dialog (#5233): a handoff from another member's space has no repository to open
// it in, so the dialog starts the session in a new space of the recipient's own. It keeps the
// dialog's prompt box and title, and sends `space` where the home dialog sends nothing.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: async (url: string) => (url.includes("api/ssm/profiles") ? [] : url.includes("api/me/ui-state") ? {} : url.includes("api/env/ui-prefs") ? {} : { branches: [] }),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { setSettings, cancelPrefsTimersForTest } = await import("../../lib/settings.ts");
const { resumePrefsTimersForTest } = await import("../../lib/settings/sync.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { Modal } = await import("../../ui/Modal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const liveKinds = new Map<string, unknown>([["claude", { kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]]);
const access: LaunchAccess = { kinds: liveKinds as LaunchAccess["kinds"], noRoute: false, allOff: false };

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function mount(props: { newSpace?: boolean; home?: boolean; onClose?: () => void; under?: () => void }): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          {/* What the dialog is stacked on (the Start hub, the inbox). */}
          {props.under && <Modal title="under" onClose={props.under}>under</Modal>}
          <LaunchModal newSpace={props.newSpace} home={props.home} repo="" kinds={["claude"]} access={access} initialPrompt="carry on" initialTitle="Carry on" onClose={props.onClose ?? (() => {})} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}
const goButton = () => document.querySelector<HTMLButtonElement>("button.launch-go")!;

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  cancelPrefsTimersForTest();
  resumePrefsTimersForTest();
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  setSettings({});
});

describe("the new-space dialog", () => {
  it("launches into a new space with the seeded text and title, in no worktree", async () => {
    await mount({ newSpace: true });
    expect(document.body.textContent).toContain(t("launch.title_new_space"));
    await act(async () => goButton().click());
    await settle();
    expect(onLaunch).toHaveBeenCalledTimes(1);
    const o = onLaunch.mock.calls[0][0];
    expect(o.space).toEqual({ name: "" });
    expect(o).toMatchObject({ prompt: "carry on", title: "Carry on", worktree: false, subdir: "", newBranch: "" });
  });

  it("is the only dialog that sends a space: the home dialog sends none", async () => {
    await mount({ home: true });
    await act(async () => goButton().click());
    await settle();
    expect(onLaunch.mock.calls[0][0].space).toBeUndefined();
  });

  // Stacked on the hub: while the create is out, Escape must not fall to the hub under it, whose
  // close would take the dialog (and the means to learn the outcome) down with it.
  it("keeps Escape here while the create is out, instead of closing what it is stacked on", async () => {
    let answer!: (r: LaunchResult) => void;
    onLaunch.mockImplementation(() => new Promise<LaunchResult>((r) => (answer = r)));
    const under = vi.fn();
    const onClose = vi.fn();
    await mount({ newSpace: true, under, onClose });
    await act(async () => goButton().click());
    await settle();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(under).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => answer({ ok: false }));
    await settle();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalled();
    expect(under).not.toHaveBeenCalled();
  });
});
