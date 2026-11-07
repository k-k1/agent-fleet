// wsURL marks the Console's own automatic reconnects (?reconnect=1) so the Control Plane
// does not count them as a human arriving (#3461). A first attach must stay unmarked.
import { beforeAll, describe, expect, it, vi } from "vitest";

vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("document", { baseURI: "http://localhost/console/" });
vi.stubGlobal("location", { protocol: "http:" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });

let client: typeof import("./client.ts");
beforeAll(async () => {
  client = await import("./client.ts");
});

describe("wsURL", () => {
  it("leaves a first attach unmarked", () => {
    const u = client.wsURL("s 1");
    expect(u.searchParams.get("session")).toBe("s 1");
    expect(u.searchParams.has("reconnect")).toBe(false);
  });
  it("marks an automatic reconnect", () => {
    const u = client.wsURL("s 1", true);
    expect(u.searchParams.get("session")).toBe("s 1");
    expect(u.searchParams.get("reconnect")).toBe("1");
  });
});
