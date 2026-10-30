import { useEffect, useState } from "react";

/** The current time, re-read every `ms` so a relative-time label ("as of 3 min ago") keeps
 *  ageing while the data it describes does not change (a poll whose body is identical
 *  never re-renders). */
export function useNow(ms: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(id);
  }, [ms]);
  return now;
}
