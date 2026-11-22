// useApiLoad / useApiOnce: the shared read hooks of the settings tabs. A transient failure is
// retried and never surfaced, a superseded answer is dropped, a reply reaches the caller whole.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useApiLoad, useApiOnce } from "./useApiLoad.ts";

type AnyReply = Record<string, unknown>;
const roots: Root[] = [];
async function mount(el: React.ReactElement) {
  const root = createRoot(document.createElement("div"));
  roots.push(root);
  await act(async () => root.render(el));
  return root;
}
const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
afterEach(() => {
  roots.splice(0).forEach((r) => act(() => r.unmount()));
  vi.useRealTimers();
});

function Load({ fetch, seen }: { fetch: () => Promise<AnyReply>; seen: (r: AnyReply) => void }) {
  useApiLoad(fetch as never, seen as never, []);
  return null;
}
function Once({
  fetch,
  onReply,
  onFail,
}: {
  fetch: (() => Promise<AnyReply>) | null;
  onReply: (r: AnyReply) => void;
  onFail: () => void;
}) {
  useApiOnce(fetch as never, onReply as never, onFail, []);
  return null;
}

describe("useApiLoad", () => {
  it("hands over an ok reply and an error reply whole", async () => {
    const err = { error: { code: "forbidden", message: "no" } };
    const a = vi.fn();
    const b = vi.fn();
    await mount(<Load fetch={() => Promise.resolve({ a: 1 })} seen={a} />);
    await mount(<Load fetch={() => Promise.resolve(err)} seen={b} />);
    await flush();
    expect(a).toHaveBeenCalledWith({ a: 1 });
    expect(b).toHaveBeenCalledWith(err);
  });

  it("retries a transient failure instead of reporting it", async () => {
    vi.useFakeTimers();
    const seen = vi.fn();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce({ error: { code: "http_502", message: "bad gateway" } })
      .mockResolvedValue({ ok: 1 });
    await mount(<Load fetch={fetch} seen={seen} />);
    expect(seen).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(seen).toHaveBeenCalledTimes(1);
    expect(seen).toHaveBeenCalledWith({ ok: 1 });
  });
});

describe("useApiOnce", () => {
  it("reads nothing for a null fetch", async () => {
    const onReply = vi.fn();
    await mount(<Once fetch={null} onReply={onReply} onFail={() => {}} />);
    await flush();
    expect(onReply).not.toHaveBeenCalled();
  });

  it("calls onFail, not onReply, when the request rejects", async () => {
    const onReply = vi.fn();
    const onFail = vi.fn();
    await mount(<Once fetch={() => Promise.reject(new Error("down"))} onReply={onReply} onFail={onFail} />);
    await flush();
    expect(onFail).toHaveBeenCalledTimes(1);
    expect(onReply).not.toHaveBeenCalled();
  });

  it("drops the answer of a read the component left behind", async () => {
    let resolve!: (v: AnyReply) => void;
    const onReply = vi.fn();
    const root = await mount(<Once fetch={() => new Promise((r) => (resolve = r))} onReply={onReply} onFail={() => {}} />);
    await act(async () => root.unmount());
    resolve({ late: 1 });
    await flush();
    expect(onReply).not.toHaveBeenCalled();
  });
});
