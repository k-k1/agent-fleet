import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EDITOR_IO_TIMEOUT_MS, getEditableFile, probeFileMeta, putFile } from "./api.ts";
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

describe("probeFileMeta classification (docs/log/44 §7.5)", () => {
  const jsonResponse = (status: number, body: unknown) =>
    vi.fn(() =>
      Promise.resolve({
        ok: status >= 200 && status < 300,
        status,
        json: () => Promise.resolve(body),
      } as Response));

  it("returns the revision of an editable file", async () => {
    const revision = revisionOf("ext\n");
    vi.stubGlobal("fetch", jsonResponse(200, {
      path: "a.txt", size: 4, binary: false, truncated: false,
      editable: true, editabilityReason: null, revision,
    }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "revision", revision });
    const [url] = vi.mocked(fetch).mock.calls[0] as [string];
    expect(url).toContain("meta=1");
  });

  it("reports an uneditable file with its reason", async () => {
    vi.stubGlobal("fetch", jsonResponse(200, {
      path: "a.txt", size: 4, binary: false, truncated: false,
      editable: false, editabilityReason: "unsupported_newline",
    }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({
      kind: "uneditable",
      reason: "unsupported_newline",
    });
  });

  it("maps 404 to missing and safety-boundary statuses to boundary", async () => {
    vi.stubGlobal("fetch", jsonResponse(404, { error: { code: "not_file" } }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "missing" });
    vi.stubGlobal("fetch", jsonResponse(400, { error: { code: "symlink_not_allowed" } }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "boundary" });
    vi.stubGlobal("fetch", jsonResponse(403, { error: { code: "denied" } }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "boundary" });
  });

  it("stays silent on gateway failures, malformed bodies, and timeouts", async () => {
    vi.stubGlobal("fetch", jsonResponse(502, null));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "unavailable" });
    vi.stubGlobal("fetch", jsonResponse(200, { editable: true, revision: "not-a-revision" }));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "unavailable" });
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("network down"))));
    await expect(probeFileMeta("a.txt")).resolves.toEqual({ kind: "unavailable" });
    vi.stubGlobal("fetch", stalledFetch());
    const stalled = probeFileMeta("a.txt");
    await vi.advanceTimersByTimeAsync(EDITOR_IO_TIMEOUT_MS);
    await expect(stalled).resolves.toEqual({ kind: "unavailable" });
  });
});
