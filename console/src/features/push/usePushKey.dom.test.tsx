import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let fails = 1;
vi.mock("./pushApi.ts", () => ({ probePushKey: () => (fails-- > 0 ? Promise.reject(new Error("503")) : Promise.resolve("k")) }));
import { usePushKey, KEY_RETRY_MS } from "./usePushKey.ts";

let root: Root | null = null;
let seen: (string | null | undefined)[] = [];
function Host() {
  seen.push(usePushKey());
  return null;
}
beforeEach(() => {
  vi.useFakeTimers();
  fails = 1;
  seen = [];
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  vi.useRealTimers();
});

describe("usePushKey", () => {
  it("asks again after a transient failure and then yields the key", async () => {
    root = createRoot(document.createElement("div"));
    act(() => root!.render(<Host />));
    await act(async () => void (await Promise.resolve()));
    expect(seen.at(-1)).toBeUndefined();
    await act(async () => void (await vi.advanceTimersByTimeAsync(KEY_RETRY_MS)));
    expect(seen.at(-1)).toBe("k");
  });
});
