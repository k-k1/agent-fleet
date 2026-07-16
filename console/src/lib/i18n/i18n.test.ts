import { describe, it, expect, beforeEach } from "vitest";
import { t, tMaybe, setLocale, getLocale, SUPPORTED_LOCALES, DEFAULT_LOCALE } from "./index.ts";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

describe("i18n runtime", () => {
  beforeEach(() => setLocale("ja"));

  it("defaults to ja and returns the ja string", () => {
    expect(getLocale()).toBe("ja");
    expect(t("theme.dark")).toBe("ダーク");
  });

  it("switches to en via setLocale", () => {
    setLocale("en");
    expect(getLocale()).toBe("en");
    expect(t("theme.dark")).toBe("Dark");
  });

  it("interpolates {vars} in both locales", () => {
    expect(t("notif.answer_ready.speech", { name: "sol" })).toBe("sol の回答が返りました。");
    setLocale("en");
    expect(t("notif.answer_ready.speech", { name: "sol" })).toBe("sol has replied.");
  });

  it("falls back to the default locale for an unsupported one", () => {
    setLocale("fr");
    expect(getLocale()).toBe(DEFAULT_LOCALE);
    expect(t("theme.light")).toBe("ライト");
  });

  it("tMaybe resolves dynamic keys and returns undefined for unknown ones", () => {
    // errText builds the key at runtime as "err." + backend code.
    expect(tMaybe("err.quota_sessions")).toBe(ja["err.quota_sessions"]);
    expect(tMaybe("err.__does_not_exist__")).toBeUndefined();
    setLocale("en");
    expect(tMaybe("err.quota_sessions")).toBe(en["err.quota_sessions"]);
  });

  it("exposes ja + en as supported locales", () => {
    expect(SUPPORTED_LOCALES).toContain("ja");
    expect(SUPPORTED_LOCALES).toContain("en");
  });

  it("en covers every ja key and adds none (completeness guard at runtime too)", () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(ja).sort());
  });
});
