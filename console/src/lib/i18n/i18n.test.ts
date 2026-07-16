import { describe, it, expect, beforeEach } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { t, tCount, tMaybe, setLocale, getLocale, SUPPORTED_LOCALES, DEFAULT_LOCALE } from "./index.ts";
import { Trans } from "./Trans.tsx";
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

describe("tCount (plurals)", () => {
  beforeEach(() => setLocale("ja"));

  it("ja is single-form: 1 and 2 both use _other", () => {
    expect(tCount("common.count_ken", 1)).toBe("1件");
    expect(tCount("common.count_ken", 2)).toBe("2件");
  });

  it("en selects one/other by Intl.PluralRules", () => {
    setLocale("en");
    expect(tCount("common.count_ken", 1)).toBe("1 item");
    expect(tCount("common.count_ken", 2)).toBe("2 items");
    expect(tCount("common.days_left", 1)).toBe("1 day left");
    expect(tCount("common.days_left", 3)).toBe("3 days left");
  });

  it("count is auto-injected into vars", () => {
    setLocale("ja");
    expect(tCount("common.days_left", 5)).toBe("あと5日");
  });
});

describe("<Trans> (markup interpolation)", () => {
  beforeEach(() => setLocale("ja"));

  it("fills numbered slots (self-closing + paired) and {vars}", () => {
    const html = renderToStaticMarkup(
      createElement(Trans, {
        k: "session.recreate_body",
        vars: { name: "sol" },
        components: [createElement("br"), createElement("strong")],
      }),
    );
    expect(html).toContain("「sol」を新しいセッションで開始します。");
    expect(html).toContain("<br/>");
    expect(html).toContain("<strong>アーカイブに退避</strong>");
  });

  it("renders the en variant when locale switches", () => {
    setLocale("en");
    const html = renderToStaticMarkup(
      createElement(Trans, {
        k: "session.recreate_body",
        vars: { name: "sol" },
        components: [createElement("br"), createElement("strong")],
      }),
    );
    expect(html).toContain("Starting “sol” as a new session.");
    expect(html).toContain("<strong>moved to the archive</strong>");
  });
});
