// The adapter through the real API client: a 200 whose body is cut off mid-stream must read as an
// unknown outcome (a thrown save) or an unavailable remote (a thrown read), never as success or as
// a blank memory.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { memoryEntryIO, resetMemoryRevisions, encodeEntry } from "./memoryEntryIO.ts";
import { revisionOf } from "../editor/buffer.ts";
import { useTenantStore } from "../../core/store/tenant.ts";

function cutOff(): Response {
  const enc = new TextEncoder();
  // The fragment is delivered by the first pull and the connection drops on the next, so the
  // client is already reading the body when it fails.
  let pulls = 0;
  const body = new ReadableStream<Uint8Array>({
    pull(c) {
      if (pulls++ === 0) c.enqueue(enc.encode('{"name":"house-rules","rev'));
      else c.error(new TypeError("network error"));
    },
  });
  return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("memoryEntryIO through the real API client", () => {
  beforeEach(() => {
    resetMemoryRevisions();
    useTenantStore.setState({ tenant: "t1" });
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => cutOff());
  });
  afterEach(() => vi.restoreAllMocks());

  it("throws, rather than succeeds, when the save's 200 body is interrupted (new memory)", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "" }, () => {});
    const content = encodeEntry({ name: "house-rules", description: "", type: "feedback", body: "text" });
    await expect(io.save("k", content, revisionOf("blank"))).rejects.toThrow();
  });

  it("throws when the recovery read's 200 body is interrupted", async () => {
    const io = memoryEntryIO({ key: "k", scope: "user", project: "", name: "" }, () => {});
    const content = encodeEntry({ name: "house-rules", description: "", type: "feedback", body: "text" });
    await expect(io.save("k", content, "x")).rejects.toThrow();
    await expect(io.load("k")).rejects.toThrow();
  });
});
