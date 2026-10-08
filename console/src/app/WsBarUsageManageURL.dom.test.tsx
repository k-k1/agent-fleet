// Every usage chip that has a vendor page links to it from its dropdown; muse is one of them.
import { describe, it, expect } from "vitest";
import { USAGE_SOURCES } from "./WsBar.tsx";

describe("WS bar usage sources: manage links", () => {
  it("points each chip at the vendor's own usage page", () => {
    const urls = Object.fromEntries(USAGE_SOURCES.map((s) => [s.kind, s.manageURL]));
    expect(urls.claude).toBe("https://claude.ai/new#settings/usage");
    expect(urls.codex).toBe("https://chatgpt.com/#settings/Usage");
    expect(urls.muse).toBe("https://dev.meta.ai/usage");
  });
});
