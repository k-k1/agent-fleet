// Kiro and Muse Code install / update live on their Agents cards (ADR 0138 D16); the sign-in stays
// on Model credentials. What would go wrong silently: a second install started on a remount, an
// install button drawn before the state is known, an update notice that is not reported by the Agent.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

const { KiroBehavior, MuseBehavior } = await import("./AgentBehaviors.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const text = () => host?.textContent || "";
const buttonWith = (s: string) => [...(host?.querySelectorAll("button") ?? [])].find((b) => (b.textContent || "").includes(s));
const posts = () => api.mock.calls.filter((c) => c[1]?.method === "POST");

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}
const unmount = () => {
  act(() => root?.unmount());
  host?.remove();
};

// One reply per (kind, state) so each test states the Agent's answer in one line.
function agent(kind: string, get: () => Record<string, unknown>) {
  api.mockImplementation((path: string, opts?: { method?: string }) => {
    if (path === `api/connections/${kind}/install`) return Promise.resolve(opts?.method === "POST" ? { state: "installing" } : get());
    return Promise.resolve({});
  });
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  unmount();
  root = null;
  host = null;
  api.mockReset();
  vi.useRealTimers();
});

describe.each([
  ["kiro", "Kiro", "855MB", KiroBehavior],
  ["muse", "Muse Code", "299MB", MuseBehavior],
] as const)("%s install on the Agents card", (kind, name, size, Behavior) => {
  it("offers the install when the CLI is missing, and swaps to nothing once the Agent reports done", async () => {
    let state: Record<string, unknown> = { state: "idle", installed: false, pin: "1.0.0", updateAvailable: false };
    agent(kind, () => state);
    await mount(<Behavior running />);
    const btn = buttonWith(`Install ${name}`) || buttonWith(`${name} を導入`);
    expect(btn).toBeTruthy();
    expect(text()).toContain(size);

    await act(async () => {
      btn!.click();
    });
    expect(posts()).toHaveLength(1);
    expect(text()).toMatch(/Installing|導入中/);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();

    state = { state: "done", installed: true, version: "1.0.0", pin: "1.0.0", updateAvailable: false };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4100);
    });
    expect(text()).not.toMatch(/Installing|導入中/);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();
  });

  // The card can be unmounted mid-download (another tab). The Agent keeps installing and reports
  // state=installing, so the remount must show progress and must not offer a second install.
  it("resumes the progress of a running install on remount, without a second POST", async () => {
    agent(kind, () => ({ state: "installing" }));
    await mount(<Behavior running />);
    expect(text()).toMatch(/Installing|導入中/);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();
    expect(posts()).toHaveLength(0);
  });

  it("draws nothing before the Agent has answered, or when the workspace is stopped", async () => {
    api.mockImplementation(() => new Promise(() => {}));
    await mount(<Behavior running />);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();
    unmount();
    agent(kind, () => ({ state: "idle", installed: false }));
    await mount(<Behavior running={false} />);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();
  });

  it("offers the update only when the Agent reports a version mismatch", async () => {
    agent(kind, () => ({ state: "idle", installed: true, version: "1.2.9", pin: "1.3.0", updateAvailable: true }));
    await mount(<Behavior running />);
    expect(text()).toContain("1.2.9");
    expect(text()).toContain("1.3.0");
    expect(buttonWith(`Update ${name}`) || buttonWith(`${name} を更新`)).toBeTruthy();
    expect(buttonWith(`Install ${name}`)).toBeFalsy();

    unmount();
    agent(kind, () => ({ state: "idle", installed: true, version: "", pin: "1.3.0", updateAvailable: false }));
    await mount(<Behavior running />);
    expect(text()).not.toContain("1.3.0");
    expect(buttonWith(`Update ${name}`)).toBeFalsy();
  });

  // The install GET can fail once (502). The connections answer says the CLI is missing, so the
  // install stays reachable instead of leaving the card empty.
  it("falls back to the connections answer when the install GET fails", async () => {
    api.mockImplementation(() => Promise.resolve({ error: { code: "http_502" } }));
    await mount(<Behavior running supported={false} />);
    expect(buttonWith(`Install ${name}`) || buttonWith(`${name} を導入`)).toBeTruthy();
    unmount();
    await mount(<Behavior running supported />);
    expect(buttonWith(`Install ${name}`)).toBeFalsy();
  });

  // A rejected POST (network drop) must end in the error state with a retry, not stay on "installing".
  it("shows the error and the button again when the install request rejects", async () => {
    api.mockImplementation((path: string, opts?: { method?: string }) =>
      opts?.method === "POST" ? Promise.reject(new Error("offline")) : Promise.resolve({ state: "idle", installed: false }),
    );
    await mount(<Behavior running />);
    await act(async () => {
      (buttonWith(`Install ${name}`) || buttonWith(`${name} を導入`))!.click();
    });
    expect(toast).toHaveBeenCalled();
    expect(text()).not.toMatch(/Installing|導入中/);
    expect(buttonWith(`Install ${name}`) || buttonWith(`${name} を導入`)).toBeTruthy();
  });

  it("keeps the update wording while an update runs", async () => {
    agent(kind, () => ({ state: "idle", installed: true, version: "1.2.9", pin: "1.3.0", updateAvailable: true }));
    await mount(<Behavior running />);
    await act(async () => {
      (buttonWith(`Update ${name}`) || buttonWith(`${name} を更新`))!.click();
    });
    expect(text()).toMatch(/Updating|更新中/);
  });
});
