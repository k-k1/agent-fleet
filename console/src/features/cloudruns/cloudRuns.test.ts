// The run line (ADR 0126): what a row shows, and what the parser keeps.
import { describe, expect, it } from "vitest";
import { cloudRunParts, parseCloudRuns, type CloudRun } from "./cloudRunsModel.ts";
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
};

describe("cloudRunParts", () => {
  it("names the identity and the command", () => {
    expect(cloudRunParts(base)).toEqual(["AWS", "prod-admin", "123456789012 / AdministratorAccess", "ap-northeast-1", "aws s3 ls"]);
  });
  it("names a chain's source", () => {
    expect(cloudRunParts({ ...base, source: "base" })).toContain(t("cloudruns.via", { source: "base" }));
  });
  it("says why a refused run did not start", () => {
    const p = cloudRunParts({ ...base, account: undefined, role: undefined, refused: "login_required" });
    expect(p.at(-1)).toBe(t("cloudruns.refused_login_required"));
    expect(cloudRunParts({ ...base, refused: "something new" }).at(-1)).toBe(t("cloudruns.refused_refused"));
  });
});

describe("parseCloudRuns", () => {
  it("keeps well-formed rows only", () => {
    expect(parseCloudRuns(undefined)).toEqual([]);
    expect(parseCloudRuns([base, null, { id: 1 }, { id: "x" }, "str"])).toEqual([base]);
  });
});

describe("an SSM pane's row (ADR 0126 decision 3)", () => {
  const ssm: CloudRun = { ...base, service: "ssm", operation: "start-session", target: "i-0abc", signIn: "corp", identityFromSettings: true };
  it("names the target and sign-in, and says the identity is from Settings, not confirmed", () => {
    const p = cloudRunParts(ssm);
    expect(p).toContain("aws ssm start-session");
    expect(p).toContain(t("cloudruns.target", { target: "i-0abc" }));
    expect(p).toContain(t("cloudruns.sign_in", { name: "corp" }));
    expect(p).toContain(t("cloudruns.from_settings"));
  });
  it("leaves the marker off an af-aws-exec run", () => {
    expect(cloudRunParts(base)).not.toContain(t("cloudruns.from_settings"));
  });
});

describe("confirm before use in a run's line (ADR 0126)", () => {
  it("says why a run was held, and flags an approval from outside the Console", () => {
    expect(cloudRunParts({ ...base, refused: "confirm_required" }).at(-1)).toBe(t("cloudruns.refused_confirm"));
    expect(cloudRunParts({ ...base, approvedOutsideConsole: true })).toContain(t("cloudruns.outside_console"));
  });
});
