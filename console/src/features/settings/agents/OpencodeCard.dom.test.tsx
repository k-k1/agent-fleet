// A key or usage change only reaches `opencode serve` through a new process, and taking that
// restart automatically cuts short whatever session is mid-turn. The card therefore says the
// change is not applied yet and offers the restart — so what has to hold is that the notice
// appears only when something really is pending, and that pressing it asks the Agent.
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
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

const { OpencodeCard } = await import("./OpencodeCard.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// Both catalogues name the daemon in the button, so matching on it keeps the test off the
// locale the suite happens to run in.
const restartButton = () =>
  [...(host?.querySelectorAll("button") ?? [])].find((b) => (b.textContent || "").includes("opencode serve"));

async function mount(st: Record<string, unknown>, reload = () => {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <OpencodeCard running st={st} reload={reload} agents={{}} updateAgents={() => {}} />,
    );
  });
}

beforeEach(() => {
  api.mockImplementation(() => Promise.resolve({}));
  apiJSON.mockImplementation(() => Promise.resolve({}));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  toast.mockReset();
});

describe("OpencodeCard restart notice", () => {
  it("stays quiet while the daemon is running what is stored", async () => {
    await mount({ connected: true, envs: ["OPENCODE_API_KEY"] });
    expect(restartButton()).toBeUndefined();
  });

  it("offers the restart when a change is waiting, and asks the Agent for it", async () => {
    const reload = vi.fn();
    await mount(
      {
        connected: true,
        envs: ["OPENCODE_API_KEY"],
        restart_required: { reasons: ["opencode provider key stored: OPENCODE_API_KEY"], since: "2026-09-17T22:00:00+09:00" },
      },
      reload,
    );

    const button = restartButton();
    expect(button).toBeTruthy();

    await act(async () => {
      button!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(apiJSON).toHaveBeenCalledWith("api/connections/opencode/serve/restart", "POST", {});
    expect(reload).toHaveBeenCalled();
  });

  it("says so when the Agent could not replace the daemon", async () => {
    apiJSON.mockImplementationOnce(() =>
      Promise.resolve({ error: { code: "serve_not_owned", message: "not ours to signal" } }),
    );
    const reload = vi.fn();
    await mount(
      { connected: true, restart_required: { reasons: ["opencode usage changed: go → off"], since: "2026-09-17T22:00:00+09:00" } },
      reload,
    );

    await act(async () => {
      restartButton()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(toast).toHaveBeenCalled();
    // A failed restart applied nothing, so the notice must not be cleared by a reload that
    // would make it look applied.
    expect(reload).not.toHaveBeenCalled();
  });
});
