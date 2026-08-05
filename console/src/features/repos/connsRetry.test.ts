// 取り直し（fetchConnsWithRetry）の契約: 1 回の失敗で「使えるエージェントは無い」を
// 確定させないこと、粘りすぎないこと、WS が消えたら即やめること。
import { describe, it, expect } from "vitest";
import { fetchConnsWithRetry } from "./connsRetry.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

const ok = { claude: { connected: true } } as unknown as ConnectionsStatus;

/** 呼ばれた回数と、待たされた間隔を記録するテスト用の依存。 */
function harness(results: (ConnectionsStatus | null)[]) {
  const slept: number[] = [];
  let calls = 0;
  return {
    slept,
    calls: () => calls,
    once: async () => results[Math.min(calls++, results.length - 1)],
    sleep: async (ms: number) => {
      slept.push(ms);
    },
  };
}

describe("fetchConnsWithRetry", () => {
  it("keeps retrying until the check answers", async () => {
    const h = harness([null, null, ok]);
    const d = await fetchConnsWithRetry({ once: h.once, sleep: h.sleep, delays: [1, 2, 3], abort: () => false });
    expect(d).toBe(ok);
    expect(h.calls()).toBe(3);
    expect(h.slept).toEqual([1, 2]); // 失敗のたびに次の間隔だけ待つ
  });

  it("gives up (null) once the schedule is exhausted", async () => {
    const h = harness([null]);
    const d = await fetchConnsWithRetry({ once: h.once, sleep: h.sleep, delays: [1, 2], abort: () => false });
    expect(d).toBeNull();
    expect(h.calls()).toBe(3); // 初回 + 取り直し 2 回
  });

  it("does not retry when the first call already succeeds", async () => {
    const h = harness([ok]);
    await fetchConnsWithRetry({ once: h.once, sleep: h.sleep, delays: [1], abort: () => false });
    expect(h.calls()).toBe(1);
    expect(h.slept).toEqual([]);
  });

  it("stops immediately when abort() says the WS is gone / the series is stale", async () => {
    const h = harness([null]);
    const d = await fetchConnsWithRetry({ once: h.once, sleep: h.sleep, delays: [1, 2], abort: () => true });
    expect(d).toBeNull();
    expect(h.calls()).toBe(1); // 粘らない
    expect(h.slept).toEqual([]);
  });

  it("drops out mid-schedule when the WS goes away while waiting", async () => {
    const h = harness([null]);
    let gone = false;
    const d = await fetchConnsWithRetry({
      once: h.once,
      sleep: async (ms) => {
        h.slept.push(ms);
        gone = true; // 待っている間に WS が停止した
      },
      delays: [1, 2, 3],
      abort: () => gone,
    });
    expect(d).toBeNull();
    expect(h.calls()).toBe(1);
    expect(h.slept).toEqual([1]);
  });
});
