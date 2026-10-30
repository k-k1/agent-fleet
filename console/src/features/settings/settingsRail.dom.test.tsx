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
import { t } from "../../lib/i18n/index.ts";

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
    // Memory snapshots is a second entry in the same group, right after Agent memory (#2324):
    // two objects, two entries.
    expect(groupOf("memorysnapshots")).toBe("personal");
    const personal = GROUPS.find((g) => g.key === "personal")!.items.map(([k]) => k);
    expect(personal.indexOf("memorysnapshots")).toBe(personal.indexOf("memory") + 1);
    // Databases belong to the workspace, not to the toolchains they used to hang off:
    // they hold a member's data and are created and destroyed (ADR 0086).
    expect(groupOf("database")).toBe("workspace");
  });

  // MCP tokens are inbound (outside → Workspace); every Connections tab is outbound (#2680).
  // The section key stays "tokens" so openSettings("tokens") and the remembered tab survive.
  it("keeps the inbound MCP tokens tab out of Connections, in its own group", () => {
    expect(groupOf("tokens")).toBe("outside");
    expect(groupOf("mcp")).toBe("connections");
    const outside = GROUPS.find((g) => g.key === "outside")!;
    expect(outside.items).toEqual([["tokens", "set.tab_tokens"]]);
    expect(GROUPS.map((g) => g.key)).toEqual(["personal", "connections", "outside", "workspace"]);
    expect(t("set.group_outside")).toBe("Outside access");
    expect(t("set.tab_mcp")).toContain("used by sessions");
  });

  it("has a renderer for every item in the rail", () => {
    const missing = GROUPS.flatMap((g) => g.items.map(([k]) => k)).filter(
      (k) => !source.includes(`section === "${k}"`),
    );
    expect(missing).toEqual([]);
  });

  it("labels the two memory entries with their own names", () => {
    const items = GROUPS.flatMap((g) => g.items);
    expect(items).toContainEqual(["memory", "set.tab_memory"]);
    expect(items).toContainEqual(["memorysnapshots", "set.tab_memory_snapshots"]);
    expect(t("set.tab_memory")).not.toBe(t("set.tab_memory_snapshots"));
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
      for (const m of readFileSync(path, "utf8").matchAll(/openSettings\("([a-z]+)(?::[a-z]+)?"\)/g)) {
        seen++;
        if (!keys.has(m[1])) bad.push(`${path}: ${m[1]}`);
      }
    }
    // Positive control: a glob that matched nothing would make the check above vacuous.
    expect(seen).toBeGreaterThan(3);
    expect(bad).toEqual([]);
  });
});
