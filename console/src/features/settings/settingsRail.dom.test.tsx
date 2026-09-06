// The settings rail's shape. Two things here are contracts rather than layout:
//
//   - a section key is a DEEP LINK (openSettings("ssm") from the CloudWatch card, the
//     remembered last-opened tab in localStorage). Moving an item between groups must not
//     rename it, or those links land on Display with no error anywhere.
//   - every item in the rail needs a renderer. A key with no `section === "…"` branch
//     renders an empty pane — the rail highlights it, nothing appears, and no test that
//     mounts one tab at a time would notice.
import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import source from "./SettingsDialog.tsx?raw";
import { GROUPS } from "./SettingsDialog.tsx";

/** Every .ts/.tsx under a directory, recursively. */
function* walk(dir: string): Generator<string> {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = dir + "/" + e.name;
    if (e.isDirectory()) yield* walk(p);
    else if (/\.tsx?$/.test(e.name)) yield p;
  }
}

const groupOf = (key: string) => GROUPS.find((g) => g.items.some(([k]) => k === key))?.key;

describe("settings rail", () => {
  it("keeps each section in the group whose audience it belongs to", () => {
    // Machine sits with the workspace's own infrastructure, above the toolchains that run
    // on it.
    expect(groupOf("machine")).toBe("workspace");
    // AWS SSM registers an SSO profile and the hosts to log into — a connection.
    expect(groupOf("ssm")).toBe("connections");
    // Agent memory is what the agent carries into a session, next to the instructions.
    expect(groupOf("memory")).toBe("personal");
  });

  it("has a renderer for every item in the rail", () => {
    const missing = GROUPS.flatMap((g) => g.items.map(([k]) => k)).filter(
      (k) => !source.includes(`section === "${k}"`),
    );
    expect(missing).toEqual([]);
  });

  it("names every section exactly once", () => {
    const keys = GROUPS.flatMap((g) => g.items.map(([k]) => k));
    expect(keys.length).toBe(new Set(keys).size);
  });

  // Every deep link in the app has to name a section that exists. A key that no longer does
  // silently opens Display instead — the modal falls back rather than failing — so the
  // hand-over from a card or a chip lands on the wrong screen with nothing to report it.
  it("only deep-links to sections that exist", () => {
    // Read with fs rather than import.meta.glob(?raw): the glob puts every file in src
    // through the transform pipeline, which cost ~9s of this suite for one grep.
    const keys = new Set(GROUPS.flatMap((g) => g.items.map(([k]) => k)));
    const bad: string[] = [];
    let seen = 0;
    // process.cwd() is the console project root (vitest's root); import.meta.url is an
    // http URL under jsdom, so it cannot be turned into a path here.
    for (const path of walk(process.cwd() + "/src")) {
      if (path.includes(".test.")) continue;
      for (const m of readFileSync(path, "utf8").matchAll(/openSettings\("([a-z]+)"\)/g)) {
        seen++;
        if (!keys.has(m[1])) bad.push(`${path}: ${m[1]}`);
      }
    }
    // Positive control: a glob that matched nothing would make the check above vacuous.
    expect(seen).toBeGreaterThan(3);
    expect(bad).toEqual([]);
  });
});
