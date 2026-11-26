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
async function mount(props: { newSpace?: boolean; home?: boolean }): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal {...props} repo="" kinds={["claude"]} access={access} initialPrompt="carry on" initialTitle="Carry on" onClose={() => {}} onLaunch={onLaunch} />
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
});
