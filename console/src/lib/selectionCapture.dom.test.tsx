import { describe, it, expect, vi, afterEach } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { holdSelection, useSelectionCapture } from "./selectionCapture.ts";

// The bug this pins is touch-only and invisible to jsdom in its own right: on a phone, tapping a
// selection control collapses the selection, `selectionchange` fires at once, and the capture
// scheduled by it clears the control before the click lands. What CAN be pinned is the ordering
// rule that fixes it — a capture armed before a press must not run while the finger is down —
// so that is what is asserted here. Whether the control is actually reachable behind the native
// selection menu is a real-device question (ADR 0050, addendum 2026-09-05).
//
// The hold is module state that outlives a test, so the hold case goes LAST: an assertion that
// nothing was captured would otherwise pass on a leftover hold rather than on its own subject.

function mount(capture: () => void): () => void {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  function Probe() {
    useSelectionCapture(capture);
    return null;
  }
  act(() => root.render(<Probe />));
  return () => {
    act(() => root.unmount());
    host.remove();
  };
}

const selectionChanged = () => act(() => void document.dispatchEvent(new Event("selectionchange")));
const pointerUp = () => act(() => void window.dispatchEvent(new Event("pointerup")));

afterEach(() => vi.useRealTimers());

describe("useSelectionCapture", () => {
  it("captures once the selection settles, not on every change", () => {
    vi.useFakeTimers();
    const capture = vi.fn();
    const unmount = mount(capture);

    selectionChanged();
    act(() => vi.advanceTimersByTime(100));
    selectionChanged(); // still dragging the handle — the wait restarts
    act(() => vi.advanceTimersByTime(200));
    expect(capture).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(100));
    expect(capture).toHaveBeenCalledTimes(1);

    unmount();
  });

  it("stops capturing once unmounted", () => {
    vi.useFakeTimers();
    const capture = vi.fn();
    const unmount = mount(capture);

    // Positive control: the same events do reach the capture while mounted, so the silence
    // asserted below is the unsubscription and not a dead harness.
    selectionChanged();
    act(() => vi.advanceTimersByTime(1000));
    expect(capture).toHaveBeenCalledTimes(1);

    unmount();
    selectionChanged();
    act(() => vi.advanceTimersByTime(1000));
    expect(capture).toHaveBeenCalledTimes(1);
  });

  it("does not clear the control the finger is on, and re-reads after the release", () => {
    vi.useFakeTimers();
    const capture = vi.fn();
    const unmount = mount(capture);

    // The tap: the press collapses the selection, which fires selectionchange and arms the
    // capture that used to delete the control out from under the finger.
    holdSelection();
    selectionChanged();
    act(() => vi.advanceTimersByTime(1000));
    expect(capture).not.toHaveBeenCalled();

    // Released: the click has landed by now, so the selection is read once more and the control
    // follows whatever the action left behind.
    pointerUp();
    act(() => vi.advanceTimersByTime(1000));
    expect(capture).toHaveBeenCalledTimes(1);

    unmount();
  });
});
