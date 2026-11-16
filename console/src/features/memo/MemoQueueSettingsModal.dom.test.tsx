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
vi.mock("./api.ts", () => ({ memoQueuePut: vi.fn(), memoQueueDelete: vi.fn() }));

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
          <MemoQueueSettingsModal categoryName="c" categoryId="cat1" queue={undefined} tenantEnabled onClose={() => {}} onChanged={() => {}} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

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
