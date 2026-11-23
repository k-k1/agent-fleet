// useConsolRun, the tidy-up's status and "run now" shared by Settings and the left pane: retried
// through the 5xx window after a start, silent while not active, dropped on a tenant switch, and a
// finished run re-reads memory everywhere (its proposals included).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { useLayoutEffect } from "react";
import { useConsolRun } from "./memoryProposals.tsx";
import { useMemoryStore } from "../../memory/store.ts";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
let seen: ReturnType<typeof useConsolRun> | null = null;

const status = (running: boolean, step = 0) => ({ config: { tokenBudget: 0, minInterval: "1h", idleAfter: "15m" }, trigger: {}, counts: {}, waiting: [], run: { running, step } });
function Probe({ active, tenant, reload }: { active: boolean; tenant: string; reload: number }) {
  seen = useConsolRun(reload, active, tenant);
  return null;
}
// Runs `during` in the layout phase of the commit that re-renders it, after Probe's own layout
// effects and before any passive cleanup: the window in which a timer cleared by useInterval's
// passive cleanup can still fire.
function Layout({ during }: { during?: () => void }) {
  useLayoutEffect(() => {
    during?.();
  }, [during]);
  return null;
}
async function render(active: boolean, tenant = "a", reload = 0, during?: () => void) {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <Probe active={active} tenant={tenant} reload={reload} />
          <Layout during={during} />
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
  apiJSON.mockReset();
  document.body.innerHTML = "";
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

  it("takes the new tenant's first answer after a switch during a run", async () => {
    api.mockResolvedValue(status(true));
    await render(true, "a");
    expect(seen!.running).toBe(true);
    let answerB: (v: unknown) => void = () => {};
    api.mockImplementation(() => new Promise((resolve) => (answerB = resolve)));
    await render(true, "b");
    await act(async () => {});
    await act(async () => answerB(status(false, 7)));
    await act(async () => {});
    expect((seen!.st as unknown as { run: { step: number } } | null)?.run.step).toBe(7);
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
  // Reads overlap: the first read and its retries, the run poll on its fixed period, the re-read
  // after "run now". Only the newest landed answer may set the status.
  it("drops one tenant's read still in flight after a switch", async () => {
    const answers: Array<(v: unknown) => void> = [];
    api.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
    await render(true, "a");
    await render(true, "b");
    expect(answers.length).toBe(2);
    // Tenant a's answer arrives first, while b's own read is still out.
    await act(async () => answers[0](status(true)));
    await act(async () => {});
    expect(seen!.st).toBeNull();
    await act(async () => answers[1](status(false)));
    await act(async () => {});
    expect(seen!.st).not.toBeNull();
    expect(seen!.running).toBe(false);
  });

  it("does not take an older poll answer over a newer one", async () => {
    api.mockResolvedValue(status(true));
    await render(true);
    const answers: Array<(v: unknown) => void> = [];
    api.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
    await act(async () => vi.advanceTimersByTime(3000));
    await act(async () => vi.advanceTimersByTime(3000));
    expect(answers.length).toBe(2);
    await act(async () => answers[1](status(false)));
    await act(async () => {});
    expect(seen!.running).toBe(false);
    await act(async () => answers[0](status(true)));
    await act(async () => {});
    expect(seen!.running).toBe(false);
  });
  it("drops a read whose reload was superseded, even for the same tenant", async () => {
    const answers: Array<(v: unknown) => void> = [];
    api.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
    await render(true, "a", 0);
    await render(true, "a", 1);
    expect(answers.length).toBe(2);
    await act(async () => answers[0](status(true)));
    await act(async () => {});
    expect(seen!.st).toBeNull();
    await act(async () => answers[1](status(false)));
    await act(async () => {});
    expect(seen!.running).toBe(false);
  });

  // Two polls out at once during a run: the earlier says the run is done, the later fails.
  const twoPolls = async () => {
    api.mockResolvedValue(status(true));
    await render(true);
    const answers: Array<(v: unknown) => void> = [];
    api.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
    await act(async () => vi.advanceTimersByTime(3000));
    await act(async () => vi.advanceTimersByTime(3000));
    expect(answers.length).toBe(2);
    const answer = async (i: number, v: unknown) => {
      await act(async () => answers[i](v));
      await act(async () => {});
    };
    return answer;
  };
  const failure = { error: { code: "bad_request", status: 400, message: "no" } };

  it("takes the run ending even after a later poll's failure landed first", async () => {
    const answer = await twoPolls();
    await answer(1, failure);
    expect(seen!.err).toBe(true);
    await answer(0, status(false));
    expect(seen!.running).toBe(false);
    expect(seen!.err).toBe(false);
  });

  it("drops a poll's failure that arrives after the run ending", async () => {
    const answer = await twoPolls();
    await answer(0, status(false));
    expect(seen!.running).toBe(false);
    await answer(1, failure);
    expect(seen!.err).toBe(false);
  });

  it("keeps the later progress when two running answers arrive out of order", async () => {
    const answer = await twoPolls();
    await answer(1, status(true, 2));
    await answer(0, status(true, 1));
    expect((seen!.st as unknown as { run: { step: number } }).run.step).toBe(2);
  });

  it("starts no read from a poll tick due after the view was hidden", async () => {
    api.mockResolvedValue(status(true));
    await render(true);
    const calls = api.mock.calls.length;
    // The tick falls due in the commit that hides the view, before useInterval's passive cleanup.
    await render(false, "a", 0, () => vi.advanceTimersByTime(3000));
    expect(api.mock.calls.length).toBe(calls);
  });

  // "Run now": the re-read after the write belongs to the view the click was made in.
  const openRun = async () => {
    let done: Promise<void> = Promise.resolve();
    await act(async () => {
      done = seen!.runNow(false);
    });
    // Wrapped: an async function returning the promise itself would wait for the whole run.
    return { done };
  };
  const confirm = async () => {
    const ok = Array.from(document.body.querySelectorAll(".ui-confirm-actions button")).pop() as HTMLButtonElement;
    await act(async () => ok.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  };
  const confirmRun = async () => {
    const run = await openRun();
    await confirm();
    return run;
  };

  for (const [what, change] of [
    ["the view is hidden", () => render(false, "a")],
    ["the tenant changes", () => render(true, "b")],
  ] as const) {
    it(`sends no write when ${what} while the dialog is open`, async () => {
      api.mockResolvedValue(status(false));
      await render(true);
      apiJSON.mockResolvedValue({ ok: true });
      const { done } = await openRun();
      await change();
      const calls = api.mock.calls.length;
      await confirm();
      await act(async () => done);
      expect(apiJSON).not.toHaveBeenCalled();
      expect(api.mock.calls.length).toBe(calls);
    });
  }

  it("re-reads after run now", async () => {
    api.mockResolvedValue(status(false));
    await render(true);
    apiJSON.mockResolvedValue({ ok: true });
    const calls = api.mock.calls.length;
    const { done } = await confirmRun();
    await act(async () => done);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(api.mock.calls.length).toBe(calls + 1);
  });

  for (const [what, change] of [
    ["the view is hidden", () => render(false, "a")],
    ["the tenant changes", () => render(true, "b")],
  ] as const) {
    it(`does not re-read after run now when ${what} while the write is out`, async () => {
      api.mockResolvedValue(status(false));
      await render(true);
      let answerPost: (v: unknown) => void = () => {};
      apiJSON.mockImplementation(() => new Promise((resolve) => (answerPost = resolve)));
      const { done } = await confirmRun();
      expect(apiJSON).toHaveBeenCalledTimes(1);
      await change();
      const calls = api.mock.calls.length;
      await act(async () => answerPost({ ok: true }));
      await act(async () => done);
      expect(api.mock.calls.length).toBe(calls);
    });
  }
});
