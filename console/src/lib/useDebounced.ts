import { useEffect, useState } from "react";

// useDebounced — a value that settles `ms` after it stops changing.
//
// Written for the Markdown editor's preview (docs/log/44 §1.1): re-parsing +
// sanitising + Mermaid on every keystroke would render half-written diagrams
// over and over. `key` identifies what the value belongs to — when it changes
// (a different file), the new value is adopted immediately rather than after
// the delay, so a freshly opened document never renders its predecessor.
export function useDebounced<T>(value: T, ms: number, key?: unknown): T {
  const [settled, setSettled] = useState<{ key: unknown; value: T }>({ key, value });

  // Adjusting state during render is React's documented way to reset on an
  // identity change; it re-renders before committing, so no frame shows the
  // previous file's value.
  if (settled.key !== key) setSettled({ key, value });

  useEffect(() => {
    if (settled.key === key && settled.value === value) return;
    const timer = setTimeout(() => setSettled({ key, value }), ms);
    return () => clearTimeout(timer);
  }, [key, ms, settled, value]);

  return settled.key === key ? settled.value : value;
}
