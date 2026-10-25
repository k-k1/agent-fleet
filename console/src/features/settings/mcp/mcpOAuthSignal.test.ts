// outcomeText is what the member reads on the return tab and in the toast: every outcome must
// resolve to a finished sentence, never a raw template or a doubled prefix.
import { beforeEach, describe, expect, it } from "vitest";
import { RETURN_REASONS } from "./mcpOAuth.ts";
import { outcomeText } from "./mcpOAuthSignal.ts";
import { setLocale, t } from "../../../lib/i18n/index.ts";

const PREFIX = { ja: "サインインが完了しませんでした: ", en: "Sign-in did not finish: " };

describe.each(["ja", "en"] as const)("outcomeText (%s)", (loc) => {
  beforeEach(() => setLocale(loc));

  it("ok reads as the success sentence", () => {
    expect(outcomeText(t, "ok")).toBe(t("mcp.oauth_return_ok_generic"));
  });

  it("failed shows the wrapper once, with the generic reason", () => {
    const s = outcomeText(t, "failed");
    expect(s).toBe(PREFIX[loc] + t("mcp.oauth_return_failed_generic"));
    expect(s).not.toContain("{reason}");
  });

  it("an unknown reason falls back to the generic reason", () => {
    // @ts-expect-error not a ReturnReason on purpose
    expect(outcomeText(t, "no_such_reason")).toBe(PREFIX[loc] + t("mcp.oauth_return_failed_generic"));
  });

  it("a known reason shows its own text", () => {
    const s = outcomeText(t, "iss_mismatch");
    expect(s).toBe(PREFIX[loc] + t("mcp.oauth_return_reason_iss_mismatch"));
  });

  it.each(RETURN_REASONS)("%s never leaks a template or doubles the prefix", (r) => {
    const s = outcomeText(t, r);
    expect(s).not.toContain("{reason}");
    expect(s.startsWith(PREFIX[loc])).toBe(true);
    expect(s.indexOf(PREFIX[loc], 1)).toBe(-1);
  });
});
