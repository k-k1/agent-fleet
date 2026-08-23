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
let wsBusy: typeof import("./workspace.ts")["wsBusy"];
let wsPowerStops: typeof import("./workspace.ts")["wsPowerStops"];
let wsStartBusy: typeof import("./workspace.ts")["wsStartBusy"];
beforeAll(async () => {
  ({ useWorkspaceStore, wsBusy, wsPowerStops, wsStartBusy } = await import("./workspace.ts"));
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

  // stale（バックエンド更新の未反映）は CP だけが判定する状態。押し付けられた値を
  // そのまま持ち、消えたら消す — クライアント側で覚えておくと、再起動して解消した
  // あとも「要再起動」が残り続ける。
  it("adopts and clears the CP-detected stale flag", () => {
    useWorkspaceStore.getState().applyPush({ state: "running", stale: true });
    expect(useWorkspaceStore.getState().stale).toBe(true);
    useWorkspaceStore.getState().applyPush({ state: "running" });
    expect(useWorkspaceStore.getState().stale).toBe(false);
  });
});

// restart() は「停止→起動」だけ。recreate（~/repos を消す）に化けていないことを
// 呼んだ URL で固定する — 更新の反映で未コミットの作業が消えたら取り返しがつかない。
describe("workspace store restart", () => {
  it("posts stop then start, never recreate", async () => {
    useWorkspaceStore.setState({ state: "running", bootPhase: "", stale: true });
    const calls: string[] = [];
    fetchMock.mockImplementation((...args: unknown[]) => {
      calls.push(String(args[0]));
      return Promise.resolve({
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        json: () => Promise.resolve({ state: "running" }),
      } as unknown as Response);
    });
    await useWorkspaceStore.getState().restart();
    const posts = calls.filter((u) => u.includes("workspace/"));
    expect(posts[0]).toContain("api/workspace/stop");
    expect(posts[1]).toContain("api/workspace/start");
    expect(calls.some((u) => u.includes("recreate") || u.includes("clean-home"))).toBe(false);
  });
});

// 収束しない `starting` から抜ける導線を固定する。ECS でタスクが配置できないと
// desired=1/running=0 のまま State() が永久に "starting" を返す（実測・docs/70
// §70.14.6）。電源トグルが running のときしか停止を出さないと、その状態で UI から
// 出せる操作が「起動」だけになり、CP は starting の Start を no-op で捨てるので
// **Console から停止する手段が一つも無くなる**。
describe("wsPowerStops / wsStartBusy", () => {
  it("stops — not starts — on the server-reported starting", () => {
    expect(wsPowerStops("starting")).toBe(true);
    expect(wsPowerStops("running")).toBe(true);
    expect(wsPowerStops("stopped")).toBe(false);
    expect(wsPowerStops("none")).toBe(false);
  });

  it("leaves the power button clickable while the server says starting", () => {
    // 無効化してよいのは楽観的な "…" だけ。"starting" で disabled にすると、
    // 上の停止導線があってもクリックできず同じ行き止まりに戻る。
    expect(wsBusy("starting")).toBe(false);
    expect(wsBusy("starting…")).toBe(true);
    expect(wsBusy("stopping…")).toBe(true);
  });

  it("still refuses to fire a second START while starting", () => {
    // 停止できるようにしたことで二重起動が復活していないこと。
    expect(wsStartBusy("starting")).toBe(true);
    expect(wsStartBusy("starting…")).toBe(true);
    expect(wsStartBusy("stopped")).toBe(false);
  });
});
