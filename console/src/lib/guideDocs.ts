// Links the Console to a page of the user guide on the public docs site (ADR 0132 decision 1).
//
// People read the guide on the site, in a new tab: the site has search, a sidebar and
// alerts that the Console's file viewer does not, and it refuses to be framed
// (`frame-ancestors 'none'`), so it cannot open inside the Console.
//
// A page id is the path under guide/ without extension, e.g. "README" or
// "user/preview/dev-signin"; scripts/docs-check.py checks that every literal page id (and
// anchor) passed here exists in guide/ in both languages. The site serves English at
// `<base><page>/` and Japanese at `<base>ja/<page>/`, and a README is its directory.
//
// The base is a constant, not deployment config: nothing deployment-wide reaches the
// Console at boot (the workspace payload is per workspace, `api/version` is fetched lazily
// for the account menu), and an operator who mirrors the site is not a case anyone has.
import { getLocale } from "./i18n/index.ts";

export const GUIDE_SITE = "https://agent-fleet.org/docs/";

// A heading id on the site, built with the Console's slug rule. Headings are translated,
// so a section usually has a different id per language; pass both when they differ.
export type GuideAnchor = string | { en: string; ja: string };

// guideUrl returns the site URL of a guide page. `page` may carry the anchor itself
// ("user/preview/dev-signin#section"); an explicit `anchor` wins over it.
export function guideUrl(page: string, anchor?: GuideAnchor, locale: string = getLocale()): string {
  const [path, inline = ""] = page.split("#", 2);
  const ja = locale === "ja";
  const dir = path.replace(/^\/+|\/+$/g, "").replace(/(^|\/)README$/, "");
  const frag = anchor === undefined ? inline : typeof anchor === "string" ? anchor : ja ? anchor.ja : anchor.en;
  return `${GUIDE_SITE}${ja ? "ja/" : ""}${dir ? `${dir}/` : ""}${frag ? `#${encodeURIComponent(frag)}` : ""}`;
}

// The props of a link that opens a guide page in a new tab. Prefer this over
// openGuidePage wherever the element can be an <a>: middle-click and copy-link work.
export function guideLinkProps(page: string, anchor?: GuideAnchor) {
  return { href: guideUrl(page, anchor), target: "_blank", rel: "noopener noreferrer" } as const;
}

// openGuidePage opens a guide page in a new tab, for callers that cannot render a link.
export function openGuidePage(page: string, anchor?: GuideAnchor): void {
  window.open(guideUrl(page, anchor), "_blank", "noopener,noreferrer");
}
