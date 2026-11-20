// useConsolRun, the tidy-up's status and "run now" shared by Settings and the left pane: retried
// through the 5xx window after a start, silent while not active, dropped on a tenant switch, and a
// finished run re-reads memory everywhere (its proposals included).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
}));

import { useConsolRun } from "./memoryProposals.tsx";
import { useMemoryStore } from "../../memory/store.ts";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
let seen: ReturnType<typeof useConsolRun> | null = null;

const status = (running: boolean) => ({ config: { tokenBudget: 0, minInterval: "1h", idleAfter: "15m" }, trigger: {}, counts: {}, waiting: [], run: { running } });
function Probe({ active, tenant }: { active: boolean; tenant: string }) {
  seen = useConsolRun(0, active, tenant);
  return null;
}
async function render(active: boolean, tenant = "a") {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <Probe active={active} tenant={tenant} />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await act(async () => {});
}

beforeEach(() => {
  vi.useFakeTimers();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  vi.useRealTimers();
});

describe("useConsolRun", () => {
  it("retries a 5xx instead of settling with no status", async () => {
    api.mockResolvedValueOnce({ error: { code: "http_502", status: 502 } }).mockResolvedValue(status(false));
    await render(true);
    expect(seen!.st).toBeNull();
    await act(async () => vi.advanceTimersByTime(800));
    await act(async () => {});
    expect(seen!.st).not.toBeNull();
    expect(seen!.err).toBe(false);
  });

  it("reads nothing while not active", async () => {
    api.mockResolvedValue(status(false));
    await render(false);
    expect(api).not.toHaveBeenCalled();
  });

  it("drops one tenant's status on a switch", async () => {
    api.mockResolvedValue(status(true));
    await render(true, "a");
    expect(seen!.running).toBe(true);
    api.mockImplementation(() => new Promise(() => {}));
    await render(true, "b");
    expect(seen!.st).toBeNull();
  });

  it("does not take a tenant switch during a run for the run ending", async () => {
    api.mockResolvedValue(status(true));
    await render(true, "a");
    const before = useMemoryStore.getState().version;
    api.mockImplementation(() => new Promise(() => {}));
    await render(true, "b");
    await act(async () => {});
    expect(useMemoryStore.getState().version).toBe(before);
  });

  it("re-reads memory everywhere once a run finishes", async () => {
    api.mockResolvedValue(status(true));
    await render(true);
    const before = useMemoryStore.getState().version;
    api.mockResolvedValue(status(false));
    await act(async () => vi.advanceTimersByTime(3000));
    await act(async () => {});
    expect(useMemoryStore.getState().version).toBe(before + 1);
  });
});
