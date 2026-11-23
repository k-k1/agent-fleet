// The raw <button className="ghost|primary|danger"> variants are skinned once, in ui/ui.css, for
// the views whose root carries `ui-bare-btns` (docs/build/02-console.md). These checks keep that
// true: each view root still carries the marker its surface needs, ui.css skins nothing for a
// marker no root carries, and no other stylesheet styles the variants on a bare button.
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");
const read = (rel: string) => readFileSync(path.join(SRC, rel), "utf8");

function files(dir: string, re: RegExp, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) files(p, re, out);
    else if (re.test(e.name)) out.push(p);
  }
  return out;
}

// The view roots, the class that makes each one a scope, and the markers it must carry.
const ROOTS: { file: string; scope: string; markers: string[] }[] = [
  { file: "features/chat/ChatView.tsx", scope: "chatview", markers: ["ui-bare-btns"] },
  { file: "features/mirror/MirrorView.tsx", scope: "mirrorview", markers: ["ui-bare-btns", "ui-bare-btns--mirror"] },
  { file: "features/sharing/SharedSessionView.tsx", scope: "shared-view", markers: ["ui-bare-btns", "ui-bare-btns--shared"] },
  { file: "features/terminal/OnboardingCard.tsx", scope: "onboard-scope", markers: ["ui-bare-btns"] },
  { file: "features/settings/SettingsDialog.tsx", scope: "settings-modal", markers: ["ui-bare-btns", "ui-bare-btns--settings"] },
  { file: "features/settings/TenantDialog.tsx", scope: "settings-modal", markers: ["ui-bare-btns", "ui-bare-btns--settings"] },
  { file: "features/settings/AdminDialog.tsx", scope: "settings-modal", markers: ["ui-bare-btns", "ui-bare-btns--settings"] },
];

// The element-level class attribute that names the scope, wherever it sits in the file.
const rootClassLines = (src: string, scope: string) =>
  src.split("\n").filter((l) => new RegExp(`className=.*["' ]${scope}[\\s"'\`]`).test(l));

describe("bare button scopes", () => {
  it("every view root that hosts bare variant buttons carries its markers", () => {
    for (const r of ROOTS) {
      const lines = rootClassLines(read(r.file), r.scope);
      expect(lines.length, `${r.file}: no className names ${r.scope}`).toBeGreaterThan(0);
      for (const l of lines) for (const m of r.markers) expect(l, `${r.file}: ${m}`).toMatch(new RegExp(`(^|[\\s"'\`])${m}([\\s"'\`]|$)`));
    }
  });

  it("ui.css skins only markers some root carries", () => {
    const css = read("ui/ui.css");
    const used = new Set(ROOTS.flatMap((r) => r.markers));
    const named = new Set([...css.matchAll(/\.(ui-bare-btns(?:--[a-z]+)?)\b/g)].map((m) => m[1]));
    expect([...named].filter((n) => !used.has(n))).toEqual([]);
    for (const u of used) expect(named.has(u), `${u} is carried by a root but skinned nowhere`).toBe(true);
  });

  // Every `selector { body }` of a stylesheet, comments removed, selector whitespace collapsed.
  // Nested at-rules are not needed here: the rules read below are all top level.
  const rulesOf = (css: string): { selector: string; body: string }[] =>
    [...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
      selector: m[1].replace(/\s+/g, " ").trim(),
      body: m[2],
    }));

  it("ui.css holds each scope's variant rules, with the declarations they exist for", () => {
    const rules = rulesOf(read("ui/ui.css"));
    // The whole selector, not a substring: a rule that gained an ancestor would stop matching
    // the root that carries only the marker.
    const body = (selector: string): string => {
      const hit = rules.filter((r) => r.selector === selector);
      expect(hit.length, `ui.css has ${hit.length} rules for ${selector}`).toBe(1);
      return hit[0].body;
    };
    const BARE = ":where(.ui-bare-btns) button.ghost";
    expect(body(BARE)).toMatch(/background:\s*transparent/);
    expect(body(BARE)).toMatch(/padding:\s*2px 6px/);
    expect(body(BARE + ":hover:not(:disabled)")).toMatch(/background:\s*var\(--btn\)/);
    const MIRROR_SHARED = ":where(.ui-bare-btns--mirror, .ui-bare-btns--shared) button.primary";
    expect(body(MIRROR_SHARED)).toMatch(/background:\s*var\(--accent\)/);
    expect(body(MIRROR_SHARED + ":hover:not(:disabled)")).toMatch(/background:/);
    // The mirror's own primary fill, and its flat disabled skin: without it a send-answer button
    // that is waiting to unlock washes out to ~2:1 against its label.
    expect(body(".ui-bare-btns--mirror button.primary")).toMatch(/background:\s*var\(--chat-accent\)/);
    expect(body(".ui-bare-btns--mirror button.primary:hover:not(:disabled)")).toMatch(/--chat-accent/);
    const DISABLED = body(".ui-bare-btns--mirror button.primary:disabled");
    expect(DISABLED).toMatch(/opacity:\s*1\b/);
    expect(DISABLED).toMatch(/background:\s*var\(--btn\)/);
    expect(DISABLED).toMatch(/color:\s*var\(--muted\)/);
    const SETTINGS = ":where(.ui-bare-btns--settings) button.";
    expect(body(SETTINGS + "primary")).toMatch(/--on-topbar-accent/);
    expect(body(SETTINGS + "primary:hover:not(:disabled)")).toMatch(/background:/);
    expect(body(SETTINGS + "danger:hover:not(:disabled)")).toMatch(/#4a2222/);
  });

  // The selectors of a stylesheet that name one of the variants: as a class (`button.ghost`,
  // `.scope .ghost`, `button.btn.ghost`, `.x:where(.ghost)`) or through a class attribute
  // selector (`[class~="ghost"]`, `[ class ~= ghost ]`, `[class*="ghost"]`). `.btn-ghost`,
  // `.ui-btn-ghost` and `[class~="ui-btn-ghost"]` are other names; `~=` and `=` compare a whole
  // class token, the substring operators (`*=` `^=` `$=` `|=`) see the word inside a longer one.
  const NAMES = "ghost|primary|danger";
  const AS_CLASS = new RegExp(`\\.(${NAMES})(?![\\w-])`);
  const AS_ATTR = new RegExp(`\\[\\s*class\\s*([~|^$*]?)=\\s*["']?([^\\]"']*?)["']?\\s*(?:[is]\\s*)?\\]`, "g");
  const variantSelectors = (css: string): string[] =>
    rulesOf(css)
      .map((r) => r.selector)
      .filter((sel) => {
        if (AS_CLASS.test(sel)) return true;
        return [...sel.matchAll(AS_ATTR)].some(([, op, value]) =>
          op === "" || op === "~" ? new RegExp(`^(${NAMES})$`).test(value.trim()) : new RegExp(`(^|[^\\w])(${NAMES})([^\\w]|$)`).test(value),
        );
      });

  it("no feature stylesheet restyles a bare ghost / primary / danger button", () => {
    const sheets = files(SRC, /\.css$/);
    // The walk must actually reach the feature sheets the shells used to live in.
    for (const must of ["features/chat/parts/shell.css", "features/mirror/parts/shell.css", "features/settings/parts/shell.css", "features/terminal/terminal.css"]) {
      expect(sheets.map((f) => path.relative(SRC, f)), must).toContain(must);
    }
    const found: string[] = [];
    for (const f of sheets) {
      // ui.css is where the variants live; its .ui-menu-item.danger is a menu-item modifier.
      if (f.endsWith(path.join("ui", "ui.css"))) continue;
      for (const sel of variantSelectors(readFileSync(f, "utf8"))) found.push(`${path.relative(SRC, f)}: ${sel}`);
    }
    expect(found).toEqual([]);
  });

  it("the scan catches the equivalent ways to restyle one, and only those", () => {
    const hits = (selector: string) => variantSelectors(`${selector} { color: red }`).length;
    for (const sel of [
      ":where(.chatview) button.ghost",
      ".scope .ghost",
      "button.btn.ghost",
      ".x:where(.primary)",
      '.chatview button[class~="ghost"]',
      '.chatview button[class ~= "ghost"]',
      "button[ class~=ghost ]",
      '[class="danger"]',
      '[class*="primary"]',
      '[class^="ghost "]',
      "button.danger:hover",
    ]) expect(hits(sel), sel).toBe(1);
    for (const sel of [
      ".mt-perm-btn.mt-perm-btn--ghost",
      ".btn-ghost",
      ".ui-btn-ghost",
      ".danger-zone",
      ".fgraph-arrow--danger",
      ".primary-x",
      'button[class~="ui-btn-ghost"]',
      '[class ~= "mt-perm-btn--ghost"]',
    ]) expect(hits(sel), sel).toBe(0);
  });

  // jsdom has no container queries, so the shared ViewHead's collapse (labels hidden below 520px,
  // the same unnamed container scm.css uses) is pinned in the stylesheets themselves: losing one
  // brings the labels back at 461-520px in that view only.
  it("each view that labels the shared header's buttons hides them below 520px", () => {
    for (const [file, label] of [
      ["features/scm/scm.css", "scm-lbl"],
      ["features/overview/overview.css", "ovw-lbl"],
      ["features/fleetgraph/fleetgraph.css", "fgraph-lbl"],
    ]) {
      const css = read(file).replace(/\/\*[\s\S]*?\*\//g, "");
      const block = css.match(/@container \(max-width: 520px\) \{([\s\S]*?\n\})/);
      expect(block, `${file}: no @container (max-width: 520px) block`).not.toBeNull();
      const rule = rulesOf(block![1]).find((r) => r.selector === `.view-head .ui-btn .${label}`);
      expect(rule?.body, `${file}: .view-head .ui-btn .${label}`).toMatch(/display:\s*none/);
    }
  });
});
