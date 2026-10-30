import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// A stand-in for the CSS Custom Highlight API, which neither node nor jsdom has. It counts adds,
// so the test can tell an incremental update from a rebuild.
let adds = 0;
class FakeHighlight extends Set<Range> {
  priority = 0;
  override add(r: Range) {
    adds++;
    return super.add(r);
  }
}
const range = (id: string) => ({ id }) as unknown as Range;

async function load() {
  vi.resetModules();
  return import("./pdfFindHighlight.ts");
}

describe("pdfFindHighlight", () => {
  let registry: Map<string, FakeHighlight>;
  beforeEach(() => {
    adds = 0;
    registry = new Map();
    vi.stubGlobal("Highlight", FakeHighlight);
    vi.stubGlobal("CSS", { highlights: registry });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("adds a page's ranges without re-adding the ones already painted", async () => {
    const h = await load();
    const pane = {};
    h.addFindHits(pane, [range("a"), range("b")]);
    adds = 0;
    h.addFindHits(pane, [range("c")]);
    expect(adds).toBe(1);
    expect(registry.get(h.FIND_HIGHLIGHT)?.size).toBe(3);
  });

  it("clears one pane's matches and current match and leaves another pane's alone", async () => {
    const h = await load();
    const a = {};
    const b = {};
    const kept = range("b1");
    h.addFindHits(a, [range("a1"), range("a2")]);
    h.addFindHits(b, [kept]);
    h.setFindCurrent(a, range("a1"));
    h.setFindCurrent(b, kept);
    h.clearFindHits(a);
    expect([...registry.get(h.FIND_HIGHLIGHT)!]).toEqual([kept]);
    expect([...registry.get(h.FIND_CURRENT)!]).toEqual([kept]);
    expect(registry.get(h.FIND_CURRENT)!.priority).toBe(1);
  });

  it("moves a pane's current match instead of accumulating them", async () => {
    const h = await load();
    const pane = {};
    const one = range("1");
    const two = range("2");
    h.setFindCurrent(pane, one);
    h.setFindCurrent(pane, two);
    expect([...registry.get(h.FIND_CURRENT)!]).toEqual([two]);
    h.setFindCurrent(pane, null);
    expect(registry.get(h.FIND_CURRENT)!.size).toBe(0);
  });

  it("does nothing where the API is missing", async () => {
    vi.stubGlobal("CSS", {});
    const h = await load();
    expect(() => h.addFindHits({}, [range("x")])).not.toThrow();
    expect(() => h.clearFindHits({})).not.toThrow();
  });
});
