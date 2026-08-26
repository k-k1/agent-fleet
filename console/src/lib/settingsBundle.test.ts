import { describe, it, expect } from "vitest";
import {
  BUNDLE_KIND,
  BUNDLE_VERSION,
  buildBundle,
  bundleFileName,
  exportablePrefs,
  mergeImportedPrefs,
  parseBundle,
  planSsmImport,
  profileIdByLabel,
  sanitizeImportedPrefs,
  summarizeBundle,
  toInstructionsSection,
  toSsmSection,
} from "./settingsBundle.ts";

const DEFAULTS = {
  theme: "dark",
  termSize: 13,
  wrap: true,
  keybindings: {} as Record<string, string>,
  quickReplies: [] as unknown[],
  workingSets: {} as Record<string, unknown>,
};
const accumulated = (k: string) => k === "keybindings" || k === "quickReplies" || k === "workingSets";

describe("exportablePrefs", () => {
  it("keeps known keys and drops ones this build doesn't know", () => {
    const out = exportablePrefs({ theme: "light", legacyThing: 1 }, DEFAULTS);
    expect(out).toEqual({ theme: "light" });
  });
});

describe("toSsmSection", () => {
  it("replaces the profile id reference with the profile label", () => {
    const s = toSsmSection(
      [{ id: "p1", label: "prod", startUrl: "https://x.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "1", roleName: "r", region: "" }],
      [{ id: "h1", alias: "mng@web-01", profileId: "p1", instanceId: "i-1", documentName: "", region: "us-east-1" }],
    );
    expect(s.profiles[0].label).toBe("prod");
    expect(s.hosts[0].profile).toBe("prod");
    expect(s.hosts[0]).not.toHaveProperty("profileId");
  });

  it("leaves the reference empty when the profile is gone (import skips it with a reason)", () => {
    const s = toSsmSection([], [{ alias: "a", profileId: "missing", instanceId: "i-1" }]);
    expect(s.hosts[0].profile).toBe("");
  });
});

describe("toInstructionsSection", () => {
  it("folds the target rows into kind → on, ignoring unsupported kinds", () => {
    const s = toInstructionsSection({
      text: "hello",
      enabled: true,
      targets: [
        { kind: "claude", supported: true, on: true },
        { kind: "codex", supported: true, on: false },
        { kind: "cursor", supported: false },
      ],
    });
    expect(s).toEqual({ text: "hello", enabled: true, targets: { claude: true, codex: false } });
  });
});

describe("parseBundle", () => {
  const ok = JSON.stringify(
    buildBundle({ prefs: { theme: "light" } }, "2026-08-26T00:00:00.000Z"),
  );

  it("reads a bundle this build wrote", () => {
    const r = parseBundle(ok);
    expect("bundle" in r && r.bundle.sections.prefs).toEqual({ theme: "light" });
  });

  it("rejects non-JSON, a foreign file, another version and an empty bundle", () => {
    expect(parseBundle("not json")).toEqual({ error: "bad_json" });
    expect(parseBundle(JSON.stringify({ kind: "something-else", version: 1 }))).toEqual({ error: "bad_kind" });
    expect(parseBundle(JSON.stringify({ kind: BUNDLE_KIND, version: BUNDLE_VERSION + 1, sections: {} }))).toEqual({
      error: "bad_version",
    });
    expect(parseBundle(JSON.stringify({ kind: BUNDLE_KIND, version: BUNDLE_VERSION, sections: {} }))).toEqual({
      error: "empty",
    });
  });

  it("drops a section whose shape is wrong instead of trusting it", () => {
    const r = parseBundle(
      JSON.stringify({
        kind: BUNDLE_KIND,
        version: BUNDLE_VERSION,
        sections: { prefs: [1, 2], ssm: { profiles: "nope", hosts: [{ alias: "a" }] } },
      }),
    );
    expect("bundle" in r).toBe(true);
    if (!("bundle" in r)) return;
    expect(r.bundle.sections.prefs).toBeUndefined();
    expect(r.bundle.sections.ssm).toEqual({ profiles: [], hosts: [{ alias: "a" }] });
  });
});

describe("sanitizeImportedPrefs", () => {
  it("keeps known keys whose value has the expected shape", () => {
    const { patch, skipped } = sanitizeImportedPrefs(
      { theme: "light", termSize: 15, wrap: false, keybindings: { a: "b" }, quickReplies: [1] },
      DEFAULTS,
    );
    expect(patch).toEqual({ theme: "light", termSize: 15, wrap: false, keybindings: { a: "b" }, quickReplies: [1] });
    expect(skipped).toEqual([]);
  });

  it("skips unknown keys and values of the wrong shape", () => {
    const { patch, skipped } = sanitizeImportedPrefs(
      { theme: 42, termSize: "big", keybindings: [], quickReplies: {}, nope: true },
      DEFAULTS,
    );
    expect(patch).toEqual({});
    expect(skipped.sort()).toEqual(["keybindings", "nope", "quickReplies", "termSize", "theme"]);
  });
});

describe("mergeImportedPrefs", () => {
  it("replaces ordinary settings outright", () => {
    expect(mergeImportedPrefs({ theme: "dark" }, { theme: "light" }, accumulated)).toEqual({ theme: "light" });
  });

  it("never empties accumulated data with an empty imported value", () => {
    const out = mergeImportedPrefs(
      { keybindings: { a: "1" }, quickReplies: [{ t: "x" }] },
      { keybindings: {}, quickReplies: [] },
      accumulated,
    );
    expect(out).toEqual({});
  });

  it("adds to accumulated objects instead of replacing them", () => {
    const out = mergeImportedPrefs(
      { keybindings: { a: "1", b: "2" } },
      { keybindings: { b: "9", c: "3" } },
      accumulated,
    );
    expect(out.keybindings).toEqual({ a: "1", b: "9", c: "3" });
  });
});

describe("planSsmImport", () => {
  const section = {
    profiles: [
      { label: "prod", startUrl: "https://c.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
      { label: "broken", startUrl: "http://insecure", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
    ],
    hosts: [
      { alias: "mng@web-01", profile: "prod", instanceId: "i-1", documentName: "", region: "" },
      { alias: "mng@web-02", profile: "broken", instanceId: "i-2", documentName: "", region: "" },
      { alias: "", profile: "prod", instanceId: "i-3", documentName: "", region: "" },
    ],
  };

  it("creates what is missing and reports why the rest was left out", () => {
    const plan = planSsmImport(section, [], []);
    expect(plan.profiles.map((p) => p.label)).toEqual(["prod"]);
    expect(plan.skippedProfiles).toEqual([{ label: "broken", reason: "invalid" }]);
    expect(plan.hosts.map((h) => h.alias)).toEqual(["mng@web-01"]);
    // 参照先のプロファイルが作られなかったホストは no_profile、alias 欠けは invalid。
    expect(plan.skippedHosts).toEqual([
      { alias: "mng@web-02", reason: "no_profile" },
      { alias: "", reason: "invalid" },
    ]);
  });

  it("leaves existing entries alone (import only adds)", () => {
    const plan = planSsmImport(
      section,
      [{ id: "p1", label: "PROD" }],
      [{ alias: "MNG@WEB-01", instanceId: "I-1" }],
    );
    expect(plan.profiles).toEqual([]);
    expect(plan.skippedProfiles[0]).toEqual({ label: "prod", reason: "exists" });
    expect(plan.hosts).toEqual([]);
    expect(plan.skippedHosts[0]).toEqual({ alias: "mng@web-01", reason: "exists" });
  });

  it("attaches a host to a profile that the same import is about to create", () => {
    const plan = planSsmImport(section, [], []);
    expect(plan.hosts[0].profile).toBe("prod");
    const ids = profileIdByLabel([{ id: "new1", label: "prod" }]);
    expect(ids.get("prod")).toBe("new1");
  });
});

describe("summarizeBundle / bundleFileName", () => {
  it("counts what the file carries", () => {
    const b = buildBundle(
      {
        prefs: { theme: "light", wrap: true },
        ssm: { profiles: [{ label: "p" } as any], hosts: [] },
        instructions: { text: "あ", enabled: true, targets: {} },
      },
      "2026-08-26T00:00:00.000Z",
    );
    expect(summarizeBundle(b)).toEqual({ prefs: 2, profiles: 1, hosts: 0, instructionBytes: 3, instructions: true });
  });

  it("names the file after the local date and time", () => {
    expect(bundleFileName(new Date(2026, 7, 26, 9, 5))).toBe("af-settings-20260826-0905.json");
  });
});
