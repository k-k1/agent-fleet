// The memo queue's agent picker lists only the kinds the credential policy allows and the member
// has not switched off (ADR 0138 D10a), like the schedule editor; a kind with nothing usable is out.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let liveKinds: Record<string, unknown>[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: async (url: string) => (url.includes("agents/kinds") ? { kinds: liveKinds, access: { policyConfirmed: true } } : {}),
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));
const memoQueuePut = vi.fn(async (_id: string, _body: unknown) => ({}));
vi.mock("./api.ts", () => ({ memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body), memoQueueDelete: vi.fn() }));

const { MemoQueueSettingsModal } = await import("./MemoQueueSettingsModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoQueueSettingsModal categoryName="c" categoryId="cat1" queue={undefined} tenantEnabled destinations={[]} onClose={() => {}} onChanged={() => {}} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const acceptBox = () => {
  const label = [...document.querySelectorAll("label")].find((l) => l.textContent?.includes("承認なしで受け付ける") || l.textContent?.includes("without approval"));
  return label?.querySelector<HTMLInputElement>("input[type=checkbox]") ?? null;
};

const agentOptions = () => [...document.querySelectorAll<HTMLOptionElement>("#memo-q-agent option")].map((o) => o.value);

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("MemoQueueSettingsModal agent picker", () => {
  it("lists only allowed kinds with something usable", async () => {
    liveKinds = [{ kind: "claude", usableRoutes: ["anthropic-api"] }, { kind: "codex", usableRoutes: [] }, { kind: "agy" }];
    await render();
    const opts = agentOptions();
    expect(opts).toContain("agy");
    expect(opts).not.toContain("codex");
    expect(opts).not.toContain("cursor");
  });
});

describe("MemoQueueSettingsModal accept-from-own-sessions switch", () => {
  it("is off by default, carries the warning text, and goes into the PUT body when turned on", async () => {
    liveKinds = [{ kind: "claude", usableRoutes: ["anthropic-api"] }];
    memoQueuePut.mockClear();
    await render();
    const box = acceptBox();
    expect(box).not.toBeNull();
    expect(box!.checked).toBe(false);
    expect(box!.closest("label")!.textContent).toMatch(/なりすま|pass itself off/);
    const save = async () => {
      await act(async () => {
        document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      });
      for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    };
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("cat1", expect.objectContaining({ acceptOwnSessions: false }));
    await act(async () => {
      box!.click();
    });
    expect(acceptBox()!.checked).toBe(true);
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("cat1", expect.objectContaining({ acceptOwnSessions: true }));
  });
});
