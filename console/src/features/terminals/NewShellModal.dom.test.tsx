// "Start a shell" from the Terminal view's header (ADR 0142 decision 10): each place sends the
// `dir` the Agent resolves against home, the left pane stays where it is, and a failure to load the
// spaces or to create is said and recoverable.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
let spacesAnswer: () => unknown = () => LIST;
const api = vi.fn(async (url: string): Promise<unknown> => (url.startsWith("api/spaces") ? spacesAnswer() : {}));
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
  getUser: () => "",
}));
const startWork = vi.fn(async (..._a: unknown[]): Promise<{ ok: boolean; name?: string }> => ({ ok: true, name: "s1" }));
vi.mock("../repos/useStartWork.ts", () => ({ useStartWork: () => startWork }));

const { NewShellModal } = await import("./NewShellModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useLeftRail } = await import("../../core/store/leftRail.ts");
const { setSettings, getSettings } = await import("../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;
const onClose = vi.fn();
const flush = () => act(async () => void (await new Promise((r) => setTimeout(r, 5))));
const rows = () => [...document.querySelectorAll<HTMLButtonElement>(".start-row")];
const rowFor = (label: string) => rows().find((b) => b.textContent?.includes(label))!;

async function mount(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <NewShellModal onClose={onClose} />
      </ToastProvider>,
    );
  });
  await flush();
}

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  startWork.mockClear();
  onClose.mockClear();
  spacesAnswer = () => LIST;
  setSettings({ workingSets: [], workingSetActive: "" });
  useSpacesStore.getState().clear();
  useReposStore.setState({ repos: [{ name: "app" }, { name: "app@wip-x", worktree: true, parent: "app", branch: "feat/x" }] });
  useLeftRail.setState({ open: true, view: "sessions" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

describe("NewShellModal", () => {
  it("starts a shell in each place with the dir the Agent resolves against home, and leaves the left pane alone", async () => {
    await mount();
    for (const [label, dir] of [
      ["Home", ""],
      ["app", "repos/app"],
      ["Q3 report", "spaces/q3-abcdef"],
    ] as const) {
      startWork.mockClear();
      await act(async () => rowFor(label).click());
      expect(startWork.mock.calls[0][0]).toEqual({ dir, repo: "" });
      expect(startWork.mock.calls[0][1]).toMatchObject({ kind: "shell", worktree: false, subdir: "" });
    }
    // A worktree starts in its own folder.
    startWork.mockClear();
    const wt = rows().find((b) => b.textContent?.includes("feat/x") || b.textContent?.includes("wip-x"))!;
    await act(async () => wt.click());
    expect(startWork.mock.calls[0][0]).toEqual({ dir: "repos/app@wip-x", repo: "" });
    expect(onClose).toHaveBeenCalled();
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
  });

  it("adds a space's shell to the active working set, as a repo-less session", async () => {
    await mount();
    const id = "w1";
    setSettings({ workingSets: [{ id, name: "A", repos: [], convs: [], sessions: [], schedules: [] }], workingSetActive: id });
    await act(async () => rowFor("Q3 report").click());
    expect(getSettings().workingSets?.[0].sessions).toEqual(["s1"]);
  });

  it("stays open when the launch is refused, and says so when the create gets no answer", async () => {
    await mount();
    startWork.mockResolvedValueOnce({ ok: false });
    await act(async () => rowFor("Home").click());
    expect(onClose).not.toHaveBeenCalled();
    startWork.mockRejectedValueOnce(new Error("network"));
    await act(async () => rowFor("Home").click());
    expect(onClose).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("Launch failed");
    // The buttons are back for another try.
    expect(rowFor("Home").disabled).toBe(false);
  });

  it("says the spaces could not be read, and reads them again on request", async () => {
    spacesAnswer = () => ({ error: { code: "http_502", status: 502 } });
    await mount();
    expect(document.body.textContent).toContain("Could not load your spaces");
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(false);
    spacesAnswer = () => LIST;
    const retry = [...document.querySelectorAll<HTMLButtonElement>("button.linklike")].find((b) => b.textContent === "Try again")!;
    await act(async () => retry.click());
    await flush();
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(true);
    expect(document.body.textContent).not.toContain("Could not load your spaces");
  });
});
