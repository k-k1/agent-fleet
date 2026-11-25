// "Start a terminal" from the Terminal view's header (ADR 0142 decision 10): each place sends the
// `dir` the Agent resolves against home, the left pane stays where it is, and a failure to load the
// spaces or to create is said and recoverable.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
let spacesAnswer: () => unknown = () => LIST;
const HOSTS = [{ id: "h1", alias: "bastion", profileId: "p1", instanceId: "i-0abc" }];
const PROFILES = [{ id: "p1", label: "Dev", accountId: "111111111111" }];
const api = vi.fn(async (url: string): Promise<unknown> =>
  url.startsWith("api/spaces") ? spacesAnswer() : url === "api/ssm/hosts" ? HOSTS : url === "api/ssm/profiles" ? PROFILES : {},
);
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: (...a: unknown[]) => apiJSON(...a),
  api: (url: string) => api(url),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
  getUser: () => "",
}));
const apiJSON = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({ name: "ssm1" }));
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
    expect(onClose).toHaveBeenCalled();
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
  });

  it("lists no worktree rows", async () => {
    await mount();
    // Home, the base clone and the one space; the worktree of "app" adds no row (its label is "app" too).
    expect(rows().map((b) => b.querySelector(".start-row-title")?.textContent)).toEqual(["Home", "app", "Q3 report"]);
  });

  it("folds a long list of places, filters it, and keeps every row reachable inside a scrolling body", async () => {
    useReposStore.setState({ repos: Array.from({ length: 30 }, (_, i) => ({ name: `proj${String(i).padStart(2, "0")}` })) });
    await mount();
    expect(rows().some((b) => b.textContent?.includes("proj29"))).toBe(false);
    const fold = document.querySelector<HTMLButtonElement>(".term-new-fold")!;
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    await act(async () => fold.click());
    const last = rowFor("proj29");
    expect(last).toBeTruthy();
    // The rows live in the modal body, which is the scroll container; the sections do not shrink.
    expect(last.closest(".ui-modal-body")).not.toBeNull();
    expect(last.closest(".term-new-sec")).not.toBeNull();
    const filter = document.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(filter, "proj07");
      filter.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(rows().filter((b) => b.textContent?.includes("proj")).map((b) => b.textContent)).toEqual([expect.stringContaining("proj07")]);
  });

  it("lists the Start hub's SSM hosts and starts through the same create, then shows the login", async () => {
    await mount();
    const card = [...document.querySelectorAll<HTMLButtonElement>(".ssm-card")].find((b) => b.textContent?.includes("bastion"))!;
    expect(card).toBeTruthy();
    await act(async () => card.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/sessions", "POST", expect.objectContaining({ kind: "ssm", ssm_host_id: "h1" }));
    expect(document.querySelector(".start-row")).toBeNull();
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

  it("offers the retry for a failed read even when an older list is on screen", async () => {
    await mount();
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(true);
    // A space made in another tab; the read made when the dialog opens again fails once.
    act(() => root!.unmount());
    root = createRoot(host);
    spacesAnswer = () => ({ error: { code: "http_502", status: 502 } });
    await mount();
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(true);
    expect(document.body.textContent).toContain("Could not read your spaces");
    spacesAnswer = () => ({ root: "/home/dev/spaces", spaces: [...LIST.spaces, { folder: "new-zzzzzz", name: "Newer", dir: "/home/dev/spaces/new-zzzzzz" }] });
    const retry = [...document.querySelectorAll<HTMLButtonElement>("button.linklike")].find((b) => b.textContent === "Try again")!;
    await act(async () => retry.click());
    await flush();
    expect(rows().some((b) => b.textContent?.includes("Newer"))).toBe(true);
  });

  it("says the spaces could not be read, and reads them again on request", async () => {
    spacesAnswer = () => ({ error: { code: "http_502", status: 502 } });
    await mount();
    expect(document.body.textContent).toContain("Could not read your spaces");
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(false);
    spacesAnswer = () => LIST;
    const retry = [...document.querySelectorAll<HTMLButtonElement>("button.linklike")].find((b) => b.textContent === "Try again")!;
    await act(async () => retry.click());
    await flush();
    expect(rows().some((b) => b.textContent?.includes("Q3 report"))).toBe(true);
    expect(document.body.textContent).not.toContain("Could not read your spaces");
  });
});
