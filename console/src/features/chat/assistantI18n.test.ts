import { describe, it, expect, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { assistantSaveError } from "./assistantI18n.ts";

describe("assistantSaveError", () => {
  afterEach(() => setLocale("ja"));

  it("shows the localized reason instead of the developer message", () => {
    const err = { code: "assistant_name_required", message: "name is required" };
    setLocale("en");
    expect(assistantSaveError(err, "fallback")).toBe("Enter a name.");
    setLocale("ja");
    expect(assistantSaveError(err, "fallback")).toBe("名前を入力してください");
  });

  it("appends the rejected integration id", () => {
    setLocale("en");
    const err = { code: "assistant_integration_unsupported", message: "unsupported integration: x", integration: "x" };
    expect(assistantSaveError(err, "fallback")).toBe("Unsupported integration: x");
  });

  it("keeps the generic wording for anything that is not an assistant_* refusal", () => {
    expect(assistantSaveError(null, "fallback")).toBe("fallback");
    expect(assistantSaveError({ code: "save", message: "open …: no space left on device" }, "fallback")).toBe(
      "fallback",
    );
    expect(assistantSaveError({ code: "http_502" }, "fallback")).toBe("fallback");
    // A malformed body is refused by the shared decoder with bad_request, which has a catalogue
    // entry of its own ("The request is malformed.") — the prefix gate is what keeps it out.
    expect(assistantSaveError({ code: "bad_request", message: "invalid JSON body" }, "fallback")).toBe("fallback");
    expect(assistantSaveError({ code: "assistant_from_a_newer_agent" }, "fallback")).toBe("fallback");
  });
});
