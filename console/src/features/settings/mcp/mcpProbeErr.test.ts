// The connection test's own failures arrive as a code + params and are worded by the Console;
// `error` is the Agent's English fallback for a code this Console does not know.
import { afterEach, describe, expect, it } from "vitest";
import { en } from "../../../lib/i18n/locales/en.ts";
import { ja } from "../../../lib/i18n/locales/ja.ts";
import { setLocale } from "../../../lib/i18n/index.ts";
import { probeErrText } from "./mcpWire.ts";
import type { ProbeResult } from "./mcpWire.ts";

const failed = (over: Partial<ProbeResult>): ProbeResult => ({ ok: false, toolCount: 0, elapsedMs: 0, ...over });

afterEach(() => setLocale("ja"));

describe("probeErrText", () => {
  it("words a known code from the catalogue with its params, per locale", () => {
    const p = failed({ code: "rpc_rejected", params: { method: "tools/list", message: "boom" }, error: "english fallback" });
    setLocale("en");
    expect(probeErrText(p)).toBe("tools/list was rejected: boom");
    setLocale("ja");
    expect(probeErrText(p)).toBe("tools/list が拒否されました: boom");
  });

  it("keeps the timeout budget", () => {
    setLocale("en");
    expect(probeErrText(failed({ code: "timeout", params: { timeoutMs: "30000" } }))).toContain("30000");
  });

  it("falls back to the Agent's text for a code from a newer Agent", () => {
    expect(probeErrText(failed({ code: "brand_new", error: "Something new happened" }))).toBe("Something new happened");
  });

  it("shows an uncoded error (HTTP status, OS text) as is", () => {
    expect(probeErrText(failed({ error: "HTTP 503" }))).toBe("HTTP 503");
  });

  it("has the same probe_err keys in en and ja", () => {
    const keys = (c: Record<string, string>) => Object.keys(c).filter((k) => k.startsWith("mcp.probe_err.")).sort();
    expect(keys(en)).toEqual(keys(ja));
    expect(keys(en).length).toBeGreaterThan(0);
  });
});
