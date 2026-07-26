import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EDITOR_IO_TIMEOUT_MS, getEditableFile, putFile } from "./api.ts";
import { revisionOf } from "./buffer.ts";

vi.mock("../../core/api/client.ts", () => ({
  rel: (path: string) => `/${path}`,
}));

/** Mirrors real fetch: settles only when the request's AbortSignal fires. */
function stalledFetch() {
  return vi.fn((_input: unknown, init?: RequestInit) =>
    new Promise<Response>((_, reject) => {
      init?.signal?.addEventListener("abort", () =>
        reject(new DOMException("The operation was aborted.", "AbortError")),
      );
    }));
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("editor api timeout", () => {
  it("aborts a stalled PUT as io_timeout", async () => {
    vi.stubGlobal("fetch", stalledFetch());
    const result = putFile("a.txt", "x\n", revisionOf("base\n"));
    const failure = expect(result).rejects.toThrow("io_timeout");
    await vi.advanceTimersByTimeAsync(EDITOR_IO_TIMEOUT_MS);
    await failure;
  });

  it("aborts a stalled recovery GET as io_timeout", async () => {
    vi.stubGlobal("fetch", stalledFetch());
    const result = getEditableFile("a.txt");
    const failure = expect(result).rejects.toThrow("io_timeout");
    await vi.advanceTimersByTimeAsync(EDITOR_IO_TIMEOUT_MS);
    await failure;
  });

  it("reports io_timeout, not an ordinary failure, when a 200 body read is cut off", async () => {
    vi.stubGlobal("fetch", vi.fn((_input: unknown, init?: RequestInit) =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          new Promise((_, reject) => {
            init?.signal?.addEventListener("abort", () =>
              reject(new DOMException("The operation was aborted.", "AbortError")),
            );
          }),
      } as Response)));
    const result = putFile("a.txt", "x\n", revisionOf("base\n"));
    const failure = expect(result).rejects.toThrow("io_timeout");
    await vi.advanceTimersByTimeAsync(EDITOR_IO_TIMEOUT_MS);
    await failure;
  });

  it("treats an unreadable 200 body before the deadline as a lost response", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.reject(new SyntaxError("Unexpected end of JSON input")),
      } as Response)));
    await expect(putFile("a.txt", "x\n", revisionOf("base\n")))
      .rejects.toThrow("invalid save response");
  });

  it("keeps an unreadable non-2xx body as an ordinary status failure", async () => {
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 400,
        json: () => Promise.reject(new SyntaxError("Unexpected end of JSON input")),
      } as Response)));
    await expect(putFile("a.txt", "x\n", revisionOf("base\n"))).resolves.toEqual({
      ok: false,
      status: 400,
      error: { code: "http_400", message: "HTTP 400" },
    });
  });

  it("does not abort a PUT that answers within the deadline", async () => {
    const content = "x\n";
    vi.stubGlobal("fetch", vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            path: "a.txt",
            size: 2,
            revision: revisionOf(content),
          }),
      } as Response)));
    const result = await putFile("a.txt", content, revisionOf("base\n"));
    expect(result).toEqual({
      ok: true,
      path: "a.txt",
      size: 2,
      revision: revisionOf(content),
    });
    // The cleared timer must not fire an abort afterwards.
    await vi.advanceTimersByTimeAsync(EDITOR_IO_TIMEOUT_MS * 2);
  });
});
