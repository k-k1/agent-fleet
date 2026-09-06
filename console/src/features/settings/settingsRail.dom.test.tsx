// The settings rail's shape. Two things here are contracts rather than layout:
//
//   - a section key is a DEEP LINK (openSettings("ssm") from the CloudWatch card, the
//     remembered last-opened tab in localStorage). Moving an item between groups must not
//     rename it, or those links land on Display with no error anywhere.
//   - every item in the rail needs a renderer. A key with no `section === "…"` branch
//     renders an empty pane — the rail highlights it, nothing appears, and no test that
//     mounts one tab at a time would notice.
import { describe, it, expect } from "vitest";
import source from "./SettingsDialog.tsx?raw";
import { GROUPS } from "./SettingsDialog.tsx";

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
});
