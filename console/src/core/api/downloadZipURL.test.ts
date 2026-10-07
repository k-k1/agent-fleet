// downloadZipURL / fsZipCheck (ADR 0111): the folder zip is a top-level navigation, so the
// tenant has to ride in the query, and the path has to survive being a URL component.
import { beforeAll, describe, expect, it, vi } from "vitest";

vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
vi.stubGlobal("document", { baseURI: "http://localhost/console/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });
const fetchSpy = vi.fn(async (_u: string) => new Response(JSON.stringify({ name: "x.zip", files: 1 })));
vi.stubGlobal("fetch", fetchSpy);

let client: typeof import("./client.ts");
beforeAll(async () => {
  client = await import("./client.ts");
});

describe("downloadZipURL", () => {
  it("targets the zip route and round-trips awkward paths exactly", () => {
    for (const path of ["repos/app", "repos/日本語 dir", "a&b=c#d", "100%/x?y", "q+plus/ü", "with\"quote'"]) {
      const u = new URL(client.downloadZipURL(path));
      expect(u.pathname).toBe("/console/api/fs/download-zip");
      expect(u.searchParams.get("path")).toBe(path);
      expect(u.searchParams.has("check")).toBe(false);
      expect(u.hash).toBe("");
    }
  });

  it("carries the selected tenant, and only when one is selected", () => {
    expect(new URL(client.downloadZipURL("a")).searchParams.has("tenant")).toBe(false);
    client.setTenant("acme");
    try {
      expect(new URL(client.downloadZipURL("a")).searchParams.get("tenant")).toBe("acme");
    } finally {
      client.setTenant(null);
    }
  });
});

describe("fsZipCheck", () => {
  it("asks the same route with check=1 and the path encoded", async () => {
    await client.fsZipCheck("repos/a&b c");
    const url = new URL(String(fetchSpy.mock.calls.at(-1)![0]));
    expect(url.pathname).toBe("/console/api/fs/download-zip");
    expect(url.searchParams.get("path")).toBe("repos/a&b c");
    expect(url.searchParams.get("check")).toBe("1");
  });

  it("hands a refusal back as {error} with its status instead of throwing", async () => {
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: "zip_too_large", message: "more than 20000 files" } }), { status: 413 }),
    );
    const r = await client.fsZipCheck("big");
    expect(r.error).toMatchObject({ code: "zip_too_large", status: 413 });
  });
});
