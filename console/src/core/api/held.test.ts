// fetchHeld / apiHeld (#1151): routes the Agent holds open while a model answers. The answer
// arrives as keepalive comments and one final frame; callers must see exactly what apiJSON
// would have resolved with for the same status and body.
import { beforeAll, describe, expect, it, vi } from "vitest";

// client.ts binds window.fetch and reads document.baseURI at import time, so stub first.
vi.stubGlobal("localStorage", {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
});
vi.stubGlobal("sessionStorage", {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
});
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", {
  fetch: vi.fn(),
  addEventListener: () => {},
  removeEventListener: () => {},
});
const answers: Response[] = [];
const fetchSpy = vi.fn(async () => answers.shift() ?? new Response("{}"));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});

// held builds an event-stream response out of the given chunks, so a frame can be split
// across reads the way a real relay splits it.
function held(...chunks: string[]): Response {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const chunk of chunks) c.enqueue(enc.encode(chunk));
      c.close();
    },
  });
  return new Response(body, {
    headers: { "Content-Type": "text/event-stream" },
  });
}

describe("apiHeld", () => {
  it("asks for an event stream and resolves with the final frame's body", async () => {
    fetchSpy.mockClear();
    answers.push(
      held(
        ": keepalive\n\n",
        ': keepalive\n\ndata: {"status":200,"bo',
        'dy":{"id":"c1","plan":"x"}}\n\n',
      ),
    );
    const res = await client.chatRefreshPlan("c1");
    expect(res).toEqual({ id: "c1", plan: "x" });
    const [url, init] = fetchSpy.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("http://localhost/api/chat/conversations/c1/plan/refresh");
    expect(new Headers(init.headers).get("Accept")).toBe("text/event-stream");
  });

  it("stamps the framed status onto an error body, as api() does for a real one", async () => {
    answers.push(
      held(
        ': keepalive\n\ndata: {"status":502,"body":{"error":{"code":"provider","message":"boom"}}}\n\n',
      ),
    );
    const res = await client.chatCompact("c1");
    expect(res.error).toEqual({
      code: "provider",
      message: "boom",
      status: 502,
    });
    expect(client.isTransientErr(res)).toBe(true);
  });

  it("reads a plain JSON answer as it is", async () => {
    answers.push(
      new Response(JSON.stringify({ assistant: "a", reply: "r" }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    expect(await client.askAssistant("hi")).toEqual({
      assistant: "a",
      reply: "r",
    });
    answers.push(new Response("workspace agent unreachable", { status: 502 }));
    expect((await client.askAssistant("hi")).error).toEqual({
      code: "http_502",
      message: "workspace agent unreachable",
    });
  });

  it("reads a stream cut before its final frame as a gateway failure", async () => {
    answers.push(held(": keepalive\n\n"));
    const res = await client.chatRefreshPlan("c1");
    expect(res.error?.code).toBe("http_502");
  });
});
