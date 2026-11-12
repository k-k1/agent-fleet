// guideUrl maps a guide page id onto the docs site: English at the base, Japanese under
// ja/, a README as its directory, and an anchor taken from the id or the argument.
import { beforeEach, describe, expect, it, vi } from "vitest";

let locale = "en";
vi.mock("./i18n/index.ts", () => ({ getLocale: () => locale }));

import { GUIDE_SITE, guideLinkProps, guideUrl, openGuidePage } from "./guideDocs.ts";

describe("guideUrl", () => {
  beforeEach(() => {
    locale = "en";
  });

  it("maps the top README to the site root in each language", () => {
    expect(guideUrl("README")).toBe("https://agent-fleet.org/docs/");
    expect(guideUrl("README", undefined, "ja")).toBe("https://agent-fleet.org/docs/ja/");
  });

  it("maps a shelf README to its directory", () => {
    expect(guideUrl("user/README")).toBe(`${GUIDE_SITE}user/`);
    expect(guideUrl("user/README", undefined, "ja")).toBe(`${GUIDE_SITE}ja/user/`);
  });

  it("maps a page to its directory URL, Japanese under ja/", () => {
    expect(guideUrl("user/preview/dev-signin")).toBe(`${GUIDE_SITE}user/preview/dev-signin/`);
    locale = "ja";
    expect(guideUrl("user/preview/dev-signin")).toBe(`${GUIDE_SITE}ja/user/preview/dev-signin/`);
  });

  it("keeps an anchor given in the page id or as an argument", () => {
    expect(guideUrl("user/preview/dev-signin#sign-in")).toBe(`${GUIDE_SITE}user/preview/dev-signin/#sign-in`);
    expect(guideUrl("user/preview/dev-signin", "sign-in")).toBe(`${GUIDE_SITE}user/preview/dev-signin/#sign-in`);
    expect(guideUrl("user/preview/dev-signin#a", "b")).toBe(`${GUIDE_SITE}user/preview/dev-signin/#b`);
  });

  it("picks the anchor of the UI language and encodes it", () => {
    const anchor = { en: "sign-in", ja: "サインイン" };
    expect(guideUrl("user/preview/dev-signin", anchor)).toBe(`${GUIDE_SITE}user/preview/dev-signin/#sign-in`);
    expect(guideUrl("user/preview/dev-signin", anchor, "ja")).toBe(
      `${GUIDE_SITE}ja/user/preview/dev-signin/#${encodeURIComponent("サインイン")}`,
    );
  });
});

describe("opening a guide page", () => {
  it("links into a new tab without an opener", () => {
    expect(guideLinkProps("README")).toEqual({ href: GUIDE_SITE, target: "_blank", rel: "noopener noreferrer" });
  });

  it("opens a new tab without an opener when it cannot be a link", () => {
    const open = vi.fn();
    vi.stubGlobal("window", { open });
    openGuidePage("user/README");
    expect(open).toHaveBeenCalledWith(`${GUIDE_SITE}user/`, "_blank", "noopener,noreferrer");
    vi.unstubAllGlobals();
  });
});
