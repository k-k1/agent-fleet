import { describe, expect, it } from "vitest";
import { parseWatches, safeWatchUrl, sameWatches, sourceIcon, timeLeftMs, typeLabel } from "./WatchesPanel.tsx";
import { t } from "../../../lib/i18n/index.ts";

describe("safeWatchUrl", () => {
  it("passes only https on github.com and bitbucket.org", () => {
    expect(safeWatchUrl("https://github.com/o/r/pull/1")).toBe("https://github.com/o/r/pull/1");
    expect(safeWatchUrl("https://bitbucket.org/o/r/pull-requests/2")).toBe("https://bitbucket.org/o/r/pull-requests/2");
    for (const bad of [
      "http://github.com/o/r",
      "javascript:alert(1)",
      "https://evil.example/github.com",
      "https://github.com.evil.example/x",
      "https://github.com@evil.example/x",
      "https://user:pw@github.com/x",
      "https://github.com:8443/x",
      "//github.com/x",
      "not a url",
      "",
      undefined,
    ]) {
      expect(safeWatchUrl(bad), String(bad)).toBeUndefined();
    }
  });
});

describe("parseWatches", () => {
  it("keeps well-formed rows and drops the rest", () => {
    const got = parseWatches([
      { id: "w1", source: "github", ref: "o/r#1", types: ["pr.merged", 3], url: "https://github.com/o/r/pull/1", extra: 1 },
      { id: "", source: "time" },
      { source: "time" },
      null,
      "x",
      { id: "w2", source: "ext.jira", ref: 5, error: "boom", due: "" },
    ]);
    expect(got).toEqual([
      { id: "w1", source: "github", ref: "o/r#1", types: ["pr.merged"], url: "https://github.com/o/r/pull/1" },
      { id: "w2", source: "ext.jira", ref: "", types: [], error: "boom" },
    ]);
    expect(parseWatches(undefined)).toEqual([]);
  });

  it("sameWatches keeps the old array for an unchanged poll", () => {
    const a = parseWatches([{ id: "w1", source: "time", types: [] }]);
    expect(sameWatches(a, parseWatches([{ id: "w1", source: "time", types: [] }]))).toBe(a);
    expect(sameWatches(a, [])).toEqual([]);
  });
});

describe("watch words", () => {
  it("words the built-in types and leaves an ext type as text", () => {
    expect(typeLabel("pr.checks.completed")).toBe(t("mirror.watch_type_checks_completed"));
    expect(typeLabel("pr.merged")).toBe(t("mirror.watch_type_merged"));
    expect(typeLabel("ext.jira.done")).toBe("ext.jira.done");
  });
  it("picks an icon per source", () => {
    expect(sourceIcon("github")).toBe("git-pull-request");
    expect(sourceIcon("time")).toBe("clock");
    expect(sourceIcon("github_actions")).toBe("play-circle");
    expect(sourceIcon("ext.x")).toBe("plug");
  });
  it("counts down to a due time and never goes negative", () => {
    const now = Date.parse("2026-11-12T10:00:00Z");
    expect(timeLeftMs({ id: "a", source: "time", ref: "", types: [], due: "2026-11-12T10:05:00Z" }, now)).toBe(300_000);
    expect(timeLeftMs({ id: "a", source: "time", ref: "", types: [], due: "2026-11-12T09:00:00Z" }, now)).toBe(0);
    expect(timeLeftMs({ id: "a", source: "github", ref: "", types: [] }, now)).toBeNull();
  });
});
