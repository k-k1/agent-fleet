// 「ターンが終わった」縁の取り方（features/files/sessionRefresh.ts）。
//
// この検出器が間違うと、症状は 2 方向に出る: 取りこぼせば元の「反映されない」に戻り、
// 出しすぎれば作業コピー配下を何度も読み直す。どちらも画面上は静かなので、縁の定義は
// ここで固定しておく。
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { COALESCE_MS, MIN_GAP_MS, WORKING_TICK_MS } from "./refreshPolicy.ts";
import type { Session } from "../../types/session.ts";

// 配線側は sessions ストア → api client（localStorage・document.baseURI・fetch）を
// 引きずるので、先にグローバルを stub してから import する（store.test.ts と同じ流儀）。
// window.setTimeout は「呼ぶ時点の global」へ委譲する — こうしないと偽タイマーが効かない。
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
let hidden = false; // タブが裏かどうか（低頻度更新のゲート）
vi.stubGlobal("document", {
  baseURI: "http://localhost/",
  get hidden() {
    return hidden;
  },
});
vi.stubGlobal("window", {
  fetch: vi.fn(),
  setTimeout: (...a: Parameters<typeof setTimeout>) => setTimeout(...a),
  clearTimeout: (...a: Parameters<typeof clearTimeout>) => clearTimeout(...a),
  setInterval: (...a: Parameters<typeof setInterval>) => setInterval(...a),
  clearInterval: (...a: Parameters<typeof clearInterval>) => clearInterval(...a),
});

let useWorkspaceStore: typeof import("../../core/store/workspace.ts")["useWorkspaceStore"];
let createTurnEndDetector: typeof import("./sessionRefresh.ts")["createTurnEndDetector"];
let isBusySession: typeof import("./sessionRefresh.ts")["isBusySession"];
let sessionPrefix: typeof import("./sessionRefresh.ts")["sessionPrefix"];
let wireFilesSessionRefresh: typeof import("./sessionRefresh.ts")["wireFilesSessionRefresh"];
let useSessionsStore: typeof import("../sessions/store.ts")["useSessionsStore"];
let useFilesStore: typeof import("./store.ts")["useFilesStore"];

beforeAll(async () => {
  ({ createTurnEndDetector, isBusySession, sessionPrefix, wireFilesSessionRefresh } = await import(
    "./sessionRefresh.ts"
  ));
  ({ useSessionsStore } = await import("../sessions/store.ts"));
  ({ useFilesStore } = await import("./store.ts"));
  ({ useWorkspaceStore } = await import("../../core/store/workspace.ts"));
});

const s = (over: Partial<Session>): Session => ({
  name: "s1",
  kind: "claude",
  alive: true,
  repo: "agent-fleet",
  ...over,
});

describe("isBusySession", () => {
  it("走っているのは working / compacting / バックグラウンド作業だけ", () => {
    expect(isBusySession(s({ state: "working" }))).toBe(true);
    expect(isBusySession(s({ state: "compacting" }))).toBe(true);
    // hook 上は idle でも run_in_background がまだ書いているかもしれない。
    expect(isBusySession(s({ state: "idle", backgroundBusy: true }))).toBe(true);
    expect(isBusySession(s({ state: "idle" }))).toBe(false);
    expect(isBusySession(s({ state: "question" }))).toBe(false);
    expect(isBusySession(s({ state: "" }))).toBe(false);
    // 停止した行は state が何であれ走っていない。
    expect(isBusySession(s({ state: "working", alive: false }))).toBe(false);
  });
});

describe("sessionPrefix", () => {
  it("作業コピーのフォルダを home 相対に直す（worktree もフォルダ名そのまま）", () => {
    expect(sessionPrefix({ repo: "agent-fleet" })).toBe("repos/agent-fleet");
    expect(sessionPrefix({ repo: "agent-fleet@wip-a" })).toBe("repos/agent-fleet@wip-a");
    expect(sessionPrefix({ repo: "" })).toBe("");
    expect(sessionPrefix({})).toBe("");
  });
});

describe("createTurnEndDetector", () => {
  it("初観測では発火しない（リロード直後に全員ぶん走らせない）", () => {
    const detect = createTurnEndDetector();
    expect(detect([s({ state: "idle" })])).toEqual([]);
    expect(detect([s({ state: "question" })])).toEqual([]);
  });

  it("走っていた → 走っていない で、その作業コピーを 1 件返す", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "idle" })])).toEqual(["repos/agent-fleet"]);
  });

  it("人待ち（question / plan / permission）で止まったターンも終わり", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "question" })])).toEqual(["repos/agent-fleet"]);
  });

  it("停止した（alive が落ちた）セッションも終わり", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "working", alive: false })])).toEqual(["repos/agent-fleet"]);
  });

  it("idle のまま動かない行では発火しない", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "idle" })]);
    expect(detect([s({ state: "idle" })])).toEqual([]);
    expect(detect([s({ state: "question" })])).toEqual([]);
  });

  it("バックグラウンド作業が残っている間は待ち、外れてから発火する", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([s({ state: "idle", backgroundBusy: true })])).toEqual([]);
    expect(detect([s({ state: "idle" })])).toEqual(["repos/agent-fleet"]);
  });

  it("同じ作業コピーで 2 つ終わっても 1 件に畳む", () => {
    const detect = createTurnEndDetector();
    detect([s({ name: "a", state: "working" }), s({ name: "b", state: "working" })]);
    expect(detect([s({ name: "a", state: "idle" }), s({ name: "b", state: "idle" })])).toEqual([
      "repos/agent-fleet",
    ]);
  });

  it("作業コピーが違えば別々に返す", () => {
    const detect = createTurnEndDetector();
    detect([s({ name: "a", state: "working" }), s({ name: "b", repo: "other", state: "working" })]);
    expect(
      detect([s({ name: "a", state: "idle" }), s({ name: "b", repo: "other", state: "idle" })]).sort(),
    ).toEqual(["repos/agent-fleet", "repos/other"]);
  });

  it("作業コピーを持たないセッション（home の shell など）は範囲が引けないので発火しない", () => {
    const detect = createTurnEndDetector();
    detect([s({ repo: null, state: "working" })]);
    expect(detect([s({ repo: null, state: "idle" })])).toEqual([]);
  });

  it("一覧から消えた（削除・アーカイブ）行では発火しない", () => {
    const detect = createTurnEndDetector();
    detect([s({ state: "working" })]);
    expect(detect([])).toEqual([]);
    // 台帳からも落ちているので、同じ名前が idle で戻ってきても初観測扱い。
    expect(detect([s({ state: "idle" })])).toEqual([]);
  });
});

// 配線: セッション一覧の更新 → 合流と最短間隔 → FILES ストアの範囲つき合図。
describe("wireFilesSessionRefresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useSessionsStore.setState({ sessions: [] });
    useFilesStore.setState({ scoped: { prefix: "", n: 0 } });
    useWorkspaceStore.setState({ state: "running" });
    hidden = false;
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const publish = (list: Session[]) => useSessionsStore.setState({ sessions: list });

  it("ターンが終わったら、その作業コピーを名指しで 1 回だけ合図する", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    expect(useFilesStore.getState().scoped.n).toBe(0); // 合流待ち
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped).toEqual({ prefix: "repos/agent-fleet", n: 1 });
    un();
  });

  it("同じ作業コピーの連投は最短間隔まで待って 1 回に畳む", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    // すぐ次のターンが終わっても、間隔が空くまでは撃たない。
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(500);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    vi.advanceTimersByTime(3000);
    expect(useFilesStore.getState().scoped.n).toBe(2);
    un();
  });

  it("解除したら以後は撃たない（予約済みのぶんも取り消す）", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    publish([s({ state: "idle" })]);
    un();
    vi.advanceTimersByTime(5000);
    expect(useFilesStore.getState().scoped.n).toBe(0);
  });

  // ターンの途中でも見に行く人のための低頻度更新（WORKING_TICK_MS）。
  it("走っている間は低頻度で撃ち続け、止まったらタイマーごと畳む", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped).toEqual({ prefix: "repos/agent-fleet", n: 1 });
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(2);

    // ターンが終わる（ここでもう 1 回撃つ）。以後は走っているものが無いので静か。
    publish([s({ state: "idle" })]);
    vi.advanceTimersByTime(MIN_GAP_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(3);
    vi.advanceTimersByTime(WORKING_TICK_MS * 3);
    expect(useFilesStore.getState().scoped.n).toBe(3);
    un();
  });

  it("タブが裏／WS が停止のときは低頻度更新を撃たない（見ている人のための更新なので）", () => {
    const un = wireFilesSessionRefresh();
    publish([s({ state: "working" })]);

    hidden = true;
    vi.advanceTimersByTime(WORKING_TICK_MS * 2 + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(0);

    hidden = false;
    useWorkspaceStore.setState({ state: "stopped" });
    vi.advanceTimersByTime(WORKING_TICK_MS * 2 + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(0);

    // 表に戻り、WS も動いていれば再開する（タイマーは止めていない）。
    useWorkspaceStore.setState({ state: "running" });
    vi.advanceTimersByTime(WORKING_TICK_MS + COALESCE_MS);
    expect(useFilesStore.getState().scoped.n).toBe(1);
    un();
  });
});
