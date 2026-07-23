// workspace store の push 適用（通信量削減 P3）の契約テスト。ポーリングと同じ
// 保護則 — 楽観 "…" 遷移中は state を clobber しない — が applyPush にも効く
// ことを固定する（破れると stop 直後の push フレームがボタンを二度押し可能に
// 戻したり、starting ダイアログが閉じたりする）。
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// store は api client（window.fetch 束縛・document.baseURI）を import するので
// 先にグローバルを stub してから import（repos/store.test.ts と同じ流儀）。
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let useWorkspaceStore: typeof import("./workspace.ts")["useWorkspaceStore"];
beforeAll(async () => {
  ({ useWorkspaceStore } = await import("./workspace.ts"));
});

describe("workspace store applyPush", () => {
  beforeEach(() => {
    useWorkspaceStore.setState({ state: "running", bootPhase: "" });
  });

  it("adopts pushed state in steady state", () => {
    useWorkspaceStore.getState().applyPush({ state: "stopped" });
    expect(useWorkspaceStore.getState().state).toBe("stopped");
  });

  it("never clobbers an optimistic transition (settle refresh owns it)", () => {
    useWorkspaceStore.setState({ state: "stopping…" });
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().state).toBe("stopping…");
  });

  it("updates only bootPhase while starting (the starting dialog's live line)", () => {
    useWorkspaceStore.setState({ state: "starting…", bootPhase: "" });
    useWorkspaceStore.getState().applyPush({ state: "running", bootPhase: "boot-install: claude-code@1" });
    expect(useWorkspaceStore.getState().state).toBe("starting…");
    expect(useWorkspaceStore.getState().bootPhase).toBe("boot-install: claude-code@1");
  });

  it("folds a missing pushed state to unknown (poll parity)", () => {
    useWorkspaceStore.getState().applyPush({});
    expect(useWorkspaceStore.getState().state).toBe("unknown");
  });
});
