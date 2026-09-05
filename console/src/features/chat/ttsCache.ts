// features/chat/ttsCache - the LRU behind the synthesis cache (a pure-function module).
// tts.ts stores AudioBuffers as the values. It depends on no DOM, so vitest can test it directly.

// makeAudioLru - an LRU that evicts oldest-first once the total duration (seconds) exceeds the cap.
// The cap is read through a getter on every call so it follows changes to the ttsCacheSec setting.
// An entry touched by get moves to the tail (most recent). A value larger than the cap on its own is
// never stored. A cap of 0 or less disables the cache: get drops what is held and always misses.
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
        if (m.size) clear(); // let go of what is held once the setting disables it
        return undefined;
      }
      const hit = m.get(key);
      if (hit) {
        m.delete(key); // touched entries move to the tail (Map keeps insertion order)
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
      // Drop oldest-first down to the cap; this is also what shrinks the cache right after the cap
      // is lowered.
      for (const [k, old] of m) {
        if (total <= max) break;
        m.delete(k);
        total -= old.duration;
      }
    },
    size: () => m.size,
  };
}
