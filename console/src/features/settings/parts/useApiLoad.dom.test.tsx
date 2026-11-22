// useApiLoad / useApiOnce: the shared read hooks of the settings tabs. A transient failure is
// retried and never surfaced, a superseded answer is dropped, a reply reaches the caller whole,
// and each answer reaches the callback of the render that asked for it.
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

type Props = { id: string; fetch: ((id: string) => Promise<AnyReply>) | null; onReply: (id: string, r: AnyReply) => void; onFail?: () => void };
function Load({ id, fetch, onReply }: Props) {
  useApiLoad(() => fetch!(id) as never, (r) => onReply(id, r as AnyReply), [id]);
  return null;
}
function Once({ id, fetch, onReply, onFail = () => {} }: Props) {
  useApiOnce(fetch ? () => fetch(id) as never : null, (r) => onReply(id, r as AnyReply), onFail, [id]);
  return null;
}

// Both hooks: a read for deps A answers late, deps have moved to B — only B's answer is reported.
describe.each([
  ["useApiLoad", Load],
  ["useApiOnce", Once],
])("%s", (_name, Probe) => {
  it("hands over an ok reply and an error reply whole", async () => {
    const err = { error: { code: "forbidden", message: "no" } };
    const seen = vi.fn();
    await mount(<Probe id="a" fetch={() => Promise.resolve({ a: 1 })} onReply={seen} />);
    await mount(<Probe id="b" fetch={() => Promise.resolve(err)} onReply={seen} />);
    await flush();
    expect(seen).toHaveBeenCalledWith("a", { a: 1 });
    expect(seen).toHaveBeenCalledWith("b", err);
  });

  it("drops the answer of a read that deps outdated, and of one the component left behind", async () => {
    const pending: Record<string, (v: AnyReply) => void> = {};
    const fetch = (id: string) => new Promise<AnyReply>((r) => (pending[id] = r));
    const seen = vi.fn();
    const root = await mount(<Probe id="A" fetch={fetch} onReply={seen} />);
    await act(async () => root.render(<Probe id="B" fetch={fetch} onReply={seen} />));
    pending.A({ from: "A" });
    await flush();
    expect(seen).not.toHaveBeenCalled();
    pending.B({ from: "B" });
    await flush();
    expect(seen).toHaveBeenCalledTimes(1);
    expect(seen).toHaveBeenCalledWith("B", { from: "B" });
    // Unmounted before the answer: nothing is reported.
    const seen2 = vi.fn();
    const r2 = await mount(<Probe id="C" fetch={fetch} onReply={seen2} />);
    await act(async () => r2.unmount());
    pending.C({ late: 1 });
    await flush();
    expect(seen2).not.toHaveBeenCalled();
  });
});

describe("useApiLoad", () => {
  it("retries a transient failure instead of reporting it", async () => {
    vi.useFakeTimers();
    const seen = vi.fn();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce({ error: { code: "http_502", message: "bad gateway" } })
      .mockResolvedValue({ ok: 1 });
    await mount(<Load id="a" fetch={fetch} onReply={seen} />);
    expect(seen).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(seen).toHaveBeenCalledTimes(1);
    expect(seen).toHaveBeenCalledWith("a", { ok: 1 });
  });
});

describe("useApiOnce", () => {
  it("reads nothing for a null fetch", async () => {
    const onReply = vi.fn();
    await mount(<Once id="a" fetch={null} onReply={onReply} />);
    await flush();
    expect(onReply).not.toHaveBeenCalled();
  });

  it("reports a 5xx reply as it is, once, without retrying", async () => {
    const fetch = vi.fn().mockResolvedValue({ error: { code: "http_502", message: "x" } });
    const onReply = vi.fn();
    await mount(<Once id="a" fetch={fetch} onReply={onReply} />);
    await flush();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(onReply).toHaveBeenCalledTimes(1);
  });

  it("calls onFail when the request rejects, or when onReply throws", async () => {
    const onFail = vi.fn();
    const onReply = vi.fn();
    await mount(<Once id="a" fetch={() => Promise.reject(new Error("down"))} onReply={onReply} onFail={onFail} />);
    await flush();
    expect(onFail).toHaveBeenCalledTimes(1);
    expect(onReply).not.toHaveBeenCalled();
    const onFail2 = vi.fn();
    await mount(
      <Once id="b" fetch={() => Promise.resolve({})} onReply={() => { throw new Error("boom"); }} onFail={onFail2} />,
    );
    await flush();
    expect(onFail2).toHaveBeenCalledTimes(1);
  });
});
