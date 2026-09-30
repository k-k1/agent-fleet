import { describe, expect, it, vi } from "vitest";

vi.mock("../../core/api/client.ts", () => ({ api: vi.fn(), apiJSON: vi.fn() }));
const { shellQuote } = await import("./gitflow.ts");

// The origin-only note is a command to paste into a shell, and git accepts branch names that a
// shell would split or expand.
describe("shellQuote", () => {
  it("leaves plain branch names alone", () => {
    expect(shellQuote("origin/release/1.2_x-y")).toBe("origin/release/1.2_x-y");
  });

  it("single-quotes anything a shell would split or expand", () => {
    expect(shellQuote("dev;echo")).toBe("'dev;echo'");
    expect(shellQuote("dev$(id)")).toBe("'dev$(id)'");
    expect(shellQuote("it's")).toBe("'it'\\''s'");
  });
});
