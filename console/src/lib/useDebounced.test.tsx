import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { useDebounced } from "./useDebounced.ts";

let seen: string[] = [];

function Harness({ value, docKey }: { value: string; docKey: string }) {
  seen.push(useDebounced(value, 200, docKey));
  return null;
}

let renderer: ReactTestRenderer | null = null;

function render(value: string, docKey = "a"): void {
  act(() => {
    renderer = create(<Harness value={value} docKey={docKey} />);
  });
}

function update(value: string, docKey = "a"): void {
  act(() => {
    renderer!.update(<Harness value={value} docKey={docKey} />);
  });
}

const latest = () => seen[seen.length - 1];

beforeEach(() => {
  seen = [];
  vi.useFakeTimers();
});

afterEach(() => {
  act(() => {
    renderer?.unmount();
  });
  renderer = null;
  vi.useRealTimers();
});

describe("useDebounced", () => {
  it("returns the first value without waiting", () => {
    render("one");
    expect(latest()).toBe("one");
  });

  it("holds the previous value until the delay elapses", () => {
    render("one");
    update("two");
    expect(latest()).toBe("one");
    act(() => void vi.advanceTimersByTime(199));
    expect(latest()).toBe("one");
    act(() => void vi.advanceTimersByTime(1));
    expect(latest()).toBe("two");
  });

  it("renders once for a burst of changes", () => {
    render("one");
    update("t");
    act(() => void vi.advanceTimersByTime(100));
    update("tw");
    act(() => void vi.advanceTimersByTime(100));
    update("two");
    act(() => void vi.advanceTimersByTime(100));
    // Still nothing: each change restarted the wait.
    expect(latest()).toBe("one");
    act(() => void vi.advanceTimersByTime(100));
    expect(latest()).toBe("two");
    // Only the settled value was ever returned besides the initial one.
    expect(new Set(seen)).toEqual(new Set(["one", "two"]));
  });

  it("adopts a new key's value immediately", () => {
    // Opening another file must never render the previous file's content, not
    // even for the debounce interval.
    render("one");
    update("other", "b");
    expect(latest()).toBe("other");
    act(() => void vi.advanceTimersByTime(200));
    expect(latest()).toBe("other");
  });

  it("drops a pending value when the key changes mid-flight", () => {
    render("one");
    update("two");
    act(() => void vi.advanceTimersByTime(100));
    update("other", "b");
    expect(latest()).toBe("other");
    act(() => void vi.advanceTimersByTime(200));
    expect(latest()).toBe("other");
  });
});
