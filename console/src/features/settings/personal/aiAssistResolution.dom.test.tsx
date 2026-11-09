// The real hook's enabled transitions: a disabled hook answers nothing, and re-enabling never
// shows the answer of an earlier fetch while the new one is in flight.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({ apiJSON: async () => ({}), api: (...a: unknown[]) => apiMock(...a) }));

const { useAiAssistResolution } = await import("./aiAssistResolution.ts");

let root: Root;
let host: HTMLDivElement;
let seen: boolean[] = [];
const unavailable = (r: ReturnType<typeof useAiAssistResolution>) => r?.["title.session"]?.source === "unavailable";

function Probe({ enabled }: { enabled: boolean }) {
  const v = unavailable(useAiAssistResolution(enabled));
  seen.push(v);
  return null;
}

beforeEach(() => {
  apiMock.mockReset();
  seen = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const render = (enabled: boolean) => act(async () => root.render(<Probe enabled={enabled} />));

describe("useAiAssistResolution", () => {
  it("fetches nothing while disabled", async () => {
    await render(false);
    expect(apiMock).not.toHaveBeenCalled();
    expect(seen.at(-1)).toBe(false);
  });

  it("drops the answer on disable and does not reuse it when re-enabled", async () => {
    apiMock.mockResolvedValueOnce({ features: [{ feature: "title.session", enabled: true, source: "unavailable" }] });
    await render(true);
    expect(seen.at(-1)).toBe(true);
    await render(false);
    expect(seen.at(-1)).toBe(false);
    apiMock.mockReturnValueOnce(new Promise(() => {})); // the next fetch never answers
    seen = [];
    await render(true);
    expect(seen.every((v) => v === false)).toBe(true);
  });
});
