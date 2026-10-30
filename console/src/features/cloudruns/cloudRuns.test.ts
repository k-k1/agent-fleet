// The run line (ADR 0126): what a row shows, and what the parser keeps.
import { describe, expect, it } from "vitest";
import { cloudRunParts, fmtDuration, parseCloudRuns, type CloudRun } from "./cloudRunsModel.ts";
import { t } from "../../lib/i18n/index.ts";

const base: CloudRun = {
  id: "1",
  provider: "aws",
  startedAt: "2026-10-30T12:00:00Z",
  profile: "prod-admin",
  account: "123456789012",
  role: "AdministratorAccess",
  region: "ap-northeast-1",
  program: "aws",
  service: "s3",
  operation: "ls",
  exitCode: 0,
  durationMs: 3200,
};

describe("cloudRunParts", () => {
  it("names the identity, the command and how it ended", () => {
    expect(cloudRunParts(base)).toEqual([
      "AWS",
      "prod-admin",
      "123456789012 / AdministratorAccess",
      "ap-northeast-1",
      "aws s3 ls",
      t("cloudruns.exit", { code: 0 }) + " · 3.2 s",
    ]);
  });
  it("names a chain's source", () => {
    expect(cloudRunParts({ ...base, source: "base" })).toContain(t("cloudruns.via", { source: "base" }));
  });
  it("says why a refused run did not start", () => {
    const p = cloudRunParts({ ...base, account: undefined, role: undefined, exitCode: undefined, refused: "login_required" });
    expect(p.at(-1)).toBe(t("cloudruns.refused_login_required"));
    expect(cloudRunParts({ ...base, exitCode: undefined, refused: "something new" }).at(-1)).toBe(t("cloudruns.refused_refused"));
  });
  it("shows a run that has not ended as running", () => {
    expect(cloudRunParts({ ...base, exitCode: undefined, durationMs: undefined }).at(-1)).toBe(t("cloudruns.running"));
  });
});

describe("fmtDuration", () => {
  it("scales", () => {
    expect(fmtDuration(250)).toBe("250 ms");
    expect(fmtDuration(3200)).toBe("3.2 s");
    expect(fmtDuration(42_400)).toBe("42 s");
    expect(fmtDuration(125_000)).toBe("2 m 5 s");
  });
});

describe("parseCloudRuns", () => {
  it("keeps well-formed rows only", () => {
    expect(parseCloudRuns(undefined)).toEqual([]);
    expect(parseCloudRuns([base, null, { id: 1 }, { id: "x" }, "str"])).toEqual([base]);
  });
});
