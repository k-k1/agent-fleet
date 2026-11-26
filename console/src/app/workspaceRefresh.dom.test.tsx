import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../features/browser/service.ts", () => ({ resetBrowserRuntime: vi.fn() }));
const sessionsRefresh = vi.fn(async () => {});
vi.mock("../features/sessions/store.ts", () => ({ useSessionsStore: { getState: () => ({ refresh: sessionsRefresh }) } }));
const reposRefresh = vi.fn(async () => {});
vi.mock("../features/repos/store.ts", () => ({ useReposStore: { getState: () => ({ refresh: reposRefresh }) } }));
vi.mock("../features/files/store.ts", () => ({ useFilesStore: { getState: () => ({ bump: vi.fn() }) } }));
vi.mock("../features/chat/store.ts", () => ({ useChatStore: { getState: () => ({ bumpList: vi.fn() }) } }));

const { useWorkspaceStore } = await import("../core/store/workspace.ts");
const { wireWorkspaceRefresh } = await import("./workspaceRefresh.ts");

let off: () => void;
beforeEach(() => {
  sessionsRefresh.mockClear();
  reposRefresh.mockClear();
  useWorkspaceStore.setState({ state: "running" });
  off = wireWorkspaceRefresh();
});
afterEach(() => off());

describe("wireWorkspaceRefresh", () => {
  it("refetches the session list when the workspace settles on stopped, so a missed push cannot leave the live list on screen", () => {
    useWorkspaceStore.setState({ state: "stopped" });
    expect(sessionsRefresh).toHaveBeenCalledTimes(1);
    expect(reposRefresh).not.toHaveBeenCalled();
  });

  it("refetches the sessions and repos on the running edge, and ignores unsettled states", () => {
    useWorkspaceStore.setState({ state: "stopped" });
    sessionsRefresh.mockClear();
    useWorkspaceStore.setState({ state: "starting" });
    expect(sessionsRefresh).not.toHaveBeenCalled();
    useWorkspaceStore.setState({ state: "running" });
    expect(sessionsRefresh).toHaveBeenCalledTimes(1);
    expect(reposRefresh).toHaveBeenCalledTimes(1);
  });
});
