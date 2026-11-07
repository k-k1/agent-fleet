import { afterEach, describe, expect, it, vi } from "vitest";

// The Control Plane hands the brand over as <script type="application/json" id="af-brand">:
// the Console's CSP (script-src 'self') blocks an inline assignment to a global.
afterEach(() => {
  document.getElementById("af-brand")?.remove();
  delete (globalThis as { __AF_BRAND?: unknown }).__AF_BRAND;
});

const load = async (block: string | null) => {
  vi.resetModules();
  if (block !== null) {
    const el = document.createElement("script");
    el.type = "application/json";
    el.id = "af-brand";
    el.textContent = block;
    document.head.append(el);
  }
  return await import("./brand.ts");
};

describe("brand data block", () => {
  it("reads the label, name and colour from #af-brand", async () => {
    const b = await load('{"label":"dev","name":"[dev] Agent Fleet","color":"#7c4dff"}');
    expect(b.brandLabel).toBe("dev");
    expect(b.appTitle).toBe("[dev] Agent Fleet — Console");
    expect(b.brandColor).toBe("#7c4dff");
  });

  it("treats a malformed block as none", async () => {
    const b = await load("{not json");
    expect(b.brandLabel).toBe("");
    expect(b.appTitle).toBe("Agent Fleet — Console");
  });
});
