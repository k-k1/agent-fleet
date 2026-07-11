// features/chat/ttsCache — 合成キャッシュの LRU 本体（純関数モジュール）。
// tts.ts が AudioBuffer を values に使う。DOM に依存しないので vitest から直接テストできる。

// makeAudioLru — 合計 duration（秒）が上限を超えたら古いものからエビクトする LRU。
// 上限は getter で都度参照する（ユーザー設定 ttsCacheSec の変更に追随するため）。
// get で触ったエントリは末尾（最新）へ回る。上限を単体で超える値は入れない。
// 上限 0 以下 = キャッシュ無効（get は保持分を破棄して必ずミス）。
export function makeAudioLru<T extends { duration: number }>(maxSec: () => number) {
  const m = new Map<string, T>();
  let total = 0;
  const clear = () => {
    m.clear();
    total = 0;
  };
  return {
    get(key: string): T | undefined {
      if (maxSec() <= 0) {
        if (m.size) clear(); // 設定で無効化されたら保持分も手放す
        return undefined;
      }
      const hit = m.get(key);
      if (hit) {
        m.delete(key); // 触ったら末尾へ（Map は挿入順）
        m.set(key, hit);
      }
      return hit;
    },
    put(key: string, v: T): void {
      const max = maxSec();
      if (!m.has(key) && v.duration <= max) {
        m.set(key, v);
        total += v.duration;
      }
      // 上限まで古いものから捨てる（上限が下げられた直後の縮小もここで効く）。
      for (const [k, old] of m) {
        if (total <= max) break;
        m.delete(k);
        total -= old.duration;
      }
    },
    size: () => m.size,
  };
}
