// 操作ビーコンの配線（docs/log/75 P3）。DOM を触るので dom プロジェクト側。
// 純ロジック（shouldBeacon の真偽表）は attention.test.ts にある。
import { describe, expect, it, vi, afterEach } from "vitest";
import { ATTENTION_INTERVAL_MS, shouldBeacon, wireAttentionBeacon } from "./attention.ts";

const attention = vi.fn(async () => null);
vi.mock("../core/api/client.ts", () => ({
  workspaceAttention: () => attention(),
}));

describe("shouldBeacon", () => {
  const now = 1_000_000;

  it("可視 × 実操作 × 間隔が空いている、のときだけ送る", () => {
    expect(shouldBeacon(true, true, now, now - ATTENTION_INTERVAL_MS)).toBe(true);
  });

  it("★裏のタブは送らない（開きっぱなしのタブが温め続けるのを防ぐ本体）", () => {
    expect(shouldBeacon(true, false, now, now - ATTENTION_INTERVAL_MS)).toBe(false);
  });

  it("★合成イベントは人の操作ではない", () => {
    expect(shouldBeacon(false, true, now, now - ATTENTION_INTERVAL_MS)).toBe(false);
  });

  it("★間隔内は送らない（スクロール 1 回ごとに POST しない）", () => {
    expect(shouldBeacon(true, true, now, now - 1000)).toBe(false);
    expect(shouldBeacon(true, true, now, now - ATTENTION_INTERVAL_MS + 1)).toBe(false);
  });
});

describe("wireAttentionBeacon", () => {
  let un: (() => void) | null = null;

  afterEach(() => {
    un?.();
    un = null;
    attention.mockClear();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  const visible = (v: "visible" | "hidden") =>
    Object.defineProperty(document, "visibilityState", { value: v, configurable: true });

  // jsdom の isTrusted は own かつ non-configurable で偽装できない。実操作を模す
  // テストは requireTrusted:false で配線し、「合成イベントを弾く」ことは既定のまま
  // 別ケースで固定する。
  const wireForGestures = () => wireAttentionBeacon({ requireTrusted: false });
  const gesture = (e: Event) => window.dispatchEvent(e);

  it("開いただけでは送らない — 最初の操作から数え始める", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireAttentionBeacon();
    vi.advanceTimersByTime(10 * ATTENTION_INTERVAL_MS);
    expect(attention).not.toHaveBeenCalled();
  });

  it("実操作で送り、間隔内の追加操作は畳む", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);

    // スクロール（＝読んでいる）も操作として数える。打鍵を伴わないので、これが
    // 数えられないと「読んでいるだけの人」が不在に見える。
    gesture(new WheelEvent("wheel"));
    expect(attention).toHaveBeenCalledTimes(1);
    gesture(new WheelEvent("wheel"));
    gesture(new MouseEvent("pointerdown"));
    expect(attention).toHaveBeenCalledTimes(1); // 60 秒間は 1 回だけ

    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    gesture(new KeyboardEvent("keydown"));
    expect(attention).toHaveBeenCalledTimes(2);
  });

  it("★合成イベント（isTrusted=false）は拾わない", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireAttentionBeacon();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    window.dispatchEvent(new WheelEvent("wheel")); // jsdom 既定 = 合成
    expect(attention).not.toHaveBeenCalled();
  });

  it("★裏のタブでは送らない", () => {
    vi.useFakeTimers();
    visible("hidden");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    gesture(new WheelEvent("wheel"));
    expect(attention).not.toHaveBeenCalled();
  });

  it("解除するとイベントを拾わない", () => {
    vi.useFakeTimers();
    visible("visible");
    un = wireForGestures();
    vi.advanceTimersByTime(ATTENTION_INTERVAL_MS + 1);
    un();
    un = null;
    gesture(new WheelEvent("wheel"));
    expect(attention).not.toHaveBeenCalled();
  });
});
