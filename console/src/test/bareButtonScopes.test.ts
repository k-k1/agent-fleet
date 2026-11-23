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

  it("no feature stylesheet restyles a bare ghost / primary / danger button", () => {
    // `button.ghost`, `.scope button.primary`, … — the element-qualified form the shells used.
    const BARE = /button\.(ghost|primary|danger)\b/;
    const found: string[] = [];
    for (const f of files(SRC, /\.css$/)) {
      if (f.endsWith(path.join("ui", "ui.css"))) continue;
      const css = readFileSync(f, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
      css.split("\n").forEach((l, i) => BARE.test(l) && found.push(`${path.relative(SRC, f)}:${i + 1}: ${l.trim()}`));
    }
    expect(found).toEqual([]);
  });

  it("the scan catches a feature rule that restyles one", () => {
    const BARE = /button\.(ghost|primary|danger)\b/;
    expect(BARE.test(":where(.chatview) button.ghost { color: red }")).toBe(true);
    expect(BARE.test(".mt-perm-btn.mt-perm-btn--ghost { border: 0 }")).toBe(false);
  });
});
