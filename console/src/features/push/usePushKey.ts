import { useEffect, useState } from "react";
import { probePushKey } from "./pushApi.ts";

export const KEY_RETRY_MS = 15_000;

/**
 * The CP's Web Push key for a screen that stays open: `undefined` while unknown, `null` once the
 * deployment answered "no Web Push", the key otherwise. A transient failure (network, a gateway's
 * 503) is asked again rather than remembered as "not configured".
 */
export function usePushKey(): string | null | undefined {
  const [key, setKey] = useState<string | null | undefined>(undefined);
  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const ask = () =>
      probePushKey().then(
        (k) => alive && setKey(k),
        () => {
          if (alive) timer = setTimeout(ask, KEY_RETRY_MS);
        },
      );
    void ask();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, []);
  return key;
}
