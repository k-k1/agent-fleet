// features/chat/ttsCache — 合成キャッシュの LRU 本体（純関数モジュール）。
// tts.ts が AudioBuffer を values に使う。DOM に依存しないので vitest から直接テストできる。

// makeAudioLru — 合計 duration（秒）が maxSec を超えたら古いものからエビクトする LRU。
// get で触ったエントリは末尾（最新）へ回る。maxSec を単体で超える値は入れない。
export function makeAudioLru<T extends { duration: number }>(maxSec: number) {
  const m = new Map<string, T>();
  let total = 0;
  return {
    get(key: string): T | undefined {
      const hit = m.get(key);
      if (hit) {
        m.delete(key); // 触ったら末尾へ（Map は挿入順）
        m.set(key, hit);
      }
      return hit;
    },
    put(key: string, v: T): void {
      if (m.has(key) || v.duration > maxSec) return;
      m.set(key, v);
      total += v.duration;
      for (const [k, old] of m) {
        if (total <= maxSec) break;
        m.delete(k);
        total -= old.duration;
      }
    },
    size: () => m.size,
  };
}
