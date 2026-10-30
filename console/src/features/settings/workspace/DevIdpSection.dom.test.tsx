// The dev IdP block's behaviour: the credentials table, the framework / variant / option controls
// and the code boxes. Snippet content itself is pinned in devIdpSnippets.test.ts.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { FRAMEWORKS, fileText, renderFiles } from "./devIdpSnippets.ts";

const toastSpy = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toastSpy(m) }));
const openTargetInNew = vi.fn();
vi.mock("../../../layout/store.ts", () => ({ useLayoutStore: { getState: () => ({ openTargetInNew }) } }));

import { DevIdpSection } from "./DevIdpSection.tsx";
import { t } from "../../../lib/i18n/index.ts";

const au = {
  devIdpAvailable: true,
  devIdpIssuer: "https://af.example.invalid/dev-idp",
  devIdpClientId: "afdev_abc",
  devIdpClientSecret: "S3CRET-LEAK", // a server that wrongly returned it must still not reach the page
  previewPorts: [3000, 8080],
  previewUrls: { "8080": "https://slug-8080.example.invalid", "3000": "https://slug-3000.example.invalid" },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
const rotate = vi.fn();

async function mount(extra: Record<string, unknown> = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<DevIdpSection au={{ ...au, ...extra }} rotate={rotate} />);
  });
}
const click = (el: Element) => act(async () => (el as HTMLElement).click());
const tab = (name: string) => [...host!.querySelectorAll('[role="tab"]')].find((b) => b.textContent === name)!;
const code = () => [...host!.querySelectorAll(".dip-pre")].map((p) => p.textContent).join("\n");
const setSelect = (sel: HTMLSelectElement, v: string) =>
  act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
    set.call(sel, v);
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  toastSpy.mockReset();
  openTargetInNew.mockReset();
  rotate.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("DevIdpSection", () => {
  it("renders every framework tab and every variant × values mode, never the secret", async () => {
    await mount();
    for (const fw of FRAMEWORKS) {
      await click(tab(fw.literal ? fw.label : t(fw.label as never)));
      for (const v of fw.variants) {
        if (fw.variants.length > 1) await click([...host!.querySelectorAll(".dip-variant-seg .seg-btn")].find((b) => b.textContent === t(v.label as never))!);
        for (const m of ["env.devidp_values_env", "env.devidp_values_fill"] as const) {
          await click([...host!.querySelectorAll(".seg-btn")].find((b) => b.textContent === t(m))!);
          expect(host!.querySelectorAll(".dip-code").length, `${fw.id}/${v.id}/${m}`).toBeGreaterThan(0);
          expect(code()).toContain("AF_DEV_IDP_CLIENT_SECRET");
        }
      }
    }
    expect(host!.textContent).not.toContain("S3CRET-LEAK");
  });

  it("credentials rows stack on phones: label + button on the first line, value on the second", async () => {
    await mount();
    const rows = [...host!.querySelectorAll(".dip-creds .dip-row")];
    expect(rows).toHaveLength(4); // issuer, discovery, client id, secret
    for (const r of rows) {
      expect([...r.children].map((c) => ["dip-k", "dip-v", "dip-b"].find((k) => c.classList.contains(k)))).toEqual(["dip-k", "dip-v", "dip-b"]);
    }
    // The stacking itself is the stylesheet's job; pin that the rule exists.
    const css = (await import("node:fs")).readFileSync("src/features/settings/workspace/workspace.css", "utf8"); // cwd is console/
    expect(css).toMatch(/@media \(max-width: 760px\)[\s\S]*\.dip-row\s*\{[^}]*"k b"\s*"v v"/);
    expect(css).toMatch(/\.dip-tabs\s*\{[^}]*overflow-x:\s*auto/);
    expect(css).toMatch(/\.dip-pre\s*\{[^}]*overflow-x:\s*auto/);
  });

  it("shows one warning callout, and offers a select next to the segmented variant switch", async () => {
    await mount();
    expect(host!.querySelectorAll(".dip-callout")).toHaveLength(1);
    expect(host!.querySelector(".dip-variant-seg")).not.toBeNull();
    expect(host!.querySelectorAll("select.dip-variant-select option")).toHaveLength(3);
  });

  it("copy buttons copy exactly the shown text", async () => {
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true });
    await mount();
    await click(tab("Auth.js"));
    const copies = [...host!.querySelectorAll(".dip-code .dip-copy")];
    expect(copies.length).toBe(3); // auth.ts, .env.local (AUTH_TRUST_HOST), next.config.ts
    const pres = [...host!.querySelectorAll(".dip-pre")];
    for (let i = 0; i < copies.length; i++) {
      await click(copies[i]);
      expect(write).toHaveBeenLastCalledWith(pres[i].textContent!.replace(/\n$/, ""));
    }
    const first = renderFiles(FRAMEWORKS[1].variants[0], {
      issuer: au.devIdpIssuer,
      discovery: `${au.devIdpIssuer}/.well-known/openid-configuration`,
      clientId: "afdev_abc",
      port: 3000,
      previewUrl: "",
      values: "env",
      preview: true,
    })[0];
    expect(write.mock.calls[0][0]).toBe(fileText(first, true));
    expect(toastSpy).toHaveBeenCalledWith(t("env.devidp_copied"));
  });

  it("the port select changes the preview URL in the snippet", async () => {
    await mount();
    await click(tab("Auth.js"));
    expect(code()).toContain("AF_PREVIEW_URL_3000");
    const sel = host!.querySelector<HTMLSelectElement>(".dip-opts label select")!;
    await setSelect(sel, "8080");
    expect(code()).toContain("if (process.env.AF_PREVIEW_URL_8080) process.env.AUTH_URL = process.env.AF_PREVIEW_URL_8080");
    expect(code()).not.toContain("AF_PREVIEW_URL_3000");
  });

  it("include preview settings adds and removes the marked lines", async () => {
    await mount();
    expect(host!.querySelectorAll(".dip-mark").length).toBeGreaterThan(0);
    expect(code()).toContain("forward-headers-strategy: framework");
    await click(host!.querySelector('.dip-check input')!);
    expect(host!.querySelectorAll(".dip-mark")).toHaveLength(0);
    expect(code()).not.toContain("forward-headers-strategy");
  });

  it("says whether the file may be committed, by variant and values mode", async () => {
    await mount();
    expect(host!.textContent).toContain(t("env.devidp_note_commit_yes"));
    await click([...host!.querySelectorAll(".seg-btn")].find((b) => b.textContent === t("env.devidp_values_fill"))!);
    expect(host!.textContent).toContain(t("env.devidp_note_commit_no"));
  });

  it("keeps the rotate action in the table and links to the guide", async () => {
    await mount();
    await click([...host!.querySelectorAll(".dip-creds button")].find((b) => b.textContent === t("env.devidp_rotate"))!);
    expect(rotate).toHaveBeenCalledTimes(1);
    await click(host!.querySelector(".dip-link")!);
    // The shipped markdown chapter as a file pane, not the getting-started checklist.
    expect(openTargetInNew).toHaveBeenCalledWith(
      { content: { kind: "file", filePath: expect.stringMatching(/^\/usr\/local\/share\/agent-fleet\/docs\/member\/14-preview-browser(\.ja)?\.md$/) } },
      true,
    );
  });
});
