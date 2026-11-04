// The run line (ADR 0126): what a row shows, and what the parser keeps.
import { describe, expect, it } from "vitest";
import {
  cloudRunParts,
  cloudRunSummary,
  parseCloudRuns,
  placeRuns,
  providerIcon,
  toolKey,
  type CloudRun,
} from "./cloudRunsModel.ts";
import type { Group, Part } from "../mirror/transcript/types.ts";
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
    // The provider is the line's brand icon, not a word in the text.
    expect(cloudRunParts(base)).toEqual(["prod-admin", "123456789012 / AdministratorAccess", "ap-northeast-1", "aws s3 ls"]);
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

describe("a Google Cloud run's line", () => {
  const gcp: CloudRun = {
    id: "g",
    provider: "gcp",
    startedAt: "2026-10-30T12:00:00Z",
    profile: "dev",
    project: "my-project",
    principal: "dev@example.com",
    region: "asia-northeast1",
    program: "gcloud",
    exit: 0,
  };
  it("names the project and the account, and the exit status", () => {
    expect(cloudRunParts(gcp)).toEqual(["dev", "my-project / dev@example.com", "asia-northeast1", "gcloud", t("cloudruns.exit", { code: 0 })]);
  });
  it("names an impersonated service account with the account behind it", () => {
    expect(cloudRunParts({ ...gcp, impersonates: "sa@my-project.iam.gserviceaccount.com" })[1]).toBe(
      "my-project / " + t("cloudruns.impersonates", { sa: "sa@my-project.iam.gserviceaccount.com", by: "dev@example.com" }),
    );
  });
  it("uses the WS bar's brand icons", () => {
    expect(providerIcon("aws")).toBe("brand:aws");
    expect(providerIcon("gcp")).toBe("brand:gcp");
    expect(providerIcon("other")).toBe("cloud");
  });
});

describe("cloudRunSummary", () => {
  it("names one identity once and counts failures apart", () => {
    const s = cloudRunSummary([base, { ...base, id: "2" }, { ...base, id: "3", refused: "login_required" }]);
    expect(s.text).toBe("prod-admin · 123456789012 / AdministratorAccess · ap-northeast-1");
    expect(s.failed).toBe(1);
    expect(s.providers).toEqual(["aws"]);
  });
  it("lists the profiles of several identities, and counts a non-zero exit as a failure", () => {
    const s = cloudRunSummary([base, { ...base, id: "2", provider: "gcp", profile: "dev", exit: 2 }]);
    expect(s.text).toBe("prod-admin, dev");
    expect(s.failed).toBe(1);
    expect(s.providers).toEqual(["aws", "gcp"]);
  });
});

describe("placeRuns", () => {
  const T = (s: number) => new Date(Date.UTC(2026, 9, 30, 12, 0, s)).toISOString();
  const bash = (cmd: string, command?: string): Part => ({ kind: "tool", tool: "Bash", info: cmd, ...(command ? { command } : {}) });
  const block = (role: string, idx: number, parts: Part[], times: number[]): Group =>
    ({ role, idx, ts: T(times[0]), parts, partTs: times.map(T), origins: parts.map(() => "") }) as unknown as Group;
  const at = (s: number, extra: Partial<CloudRun> = {}): CloudRun => ({ ...base, id: "r" + s, startedAt: T(s), ...extra });

  const groups = [
    block("user", 0, [{ kind: "text", text: "deploy" }], [0]),
    block(
      "assistant",
      1,
      [
        { kind: "text", text: "ok" },
        bash("af-aws-exec --profile prod-admin --account 1 -- bash -c 'aws s3 ls'"),
        { kind: "tool", tool: "Read", info: "x" },
        bash("./deploy.sh"),
        bash("af-aws-exec --profile other -- aws sts get-caller-identity"),
      ],
      [1, 2, 3, 10, 20],
    ),
    block("user", 2, [{ kind: "text", text: "thanks" }], [40]),
  ];

  it("puts a run on the latest call before it that names the wrapper and its profile", () => {
    const p = placeRuns(groups, [at(5), at(25)]);
    // r5: the named call at 2 s. r25: the call at 20 s names another profile, so the named one
    // at 2 s still wins (a loop inside that call).
    expect(p.onTool.get(toolKey(1, 1))?.map((r) => r.id)).toEqual(["r5", "r25"]);
    expect(p.loose).toEqual([]);
  });
  it("matches the profile in the whole command when the label was cut", () => {
    const g = [
      groups[0],
      block("assistant", 1, [bash("af-aws-exec --account 123456789012 --region ap-northeast-1 …", "af-aws-exec --account 123456789012 --region ap-northeast-1 --profile prod-admin -- aws s3 ls"), bash("ls")], [1, 2]),
    ];
    expect(placeRuns(g, [at(3)]).onTool.get(toolKey(1, 0))).toHaveLength(1);
  });
  it("gives a run nothing names to the latest shell call (a script)", () => {
    const p = placeRuns(groups, [at(15, { profile: "script-profile" })]);
    expect(p.onTool.get(toolKey(1, 3))?.map((r) => r.id)).toEqual(["r15"]);
  });
  it("leaves loose a run after a member prompt, before any call, or in a block without row times", () => {
    const noTimes = groups.map((g) => ({ ...g, partTs: undefined })) as Group[];
    expect(placeRuns(groups, [at(45)]).loose).toHaveLength(1);
    expect(placeRuns(groups, [at(1)]).loose).toHaveLength(1);
    expect(placeRuns(noTimes, [at(5)]).loose).toHaveLength(1);
    expect(placeRuns(groups, [at(5, { startedAt: "garbage" })]).loose).toHaveLength(1);
  });
  it("matches af-gcloud-exec for a Google Cloud run, never af-aws-exec", () => {
    const g = [
      groups[0],
      block("assistant", 1, [bash("af-gcloud-exec --profile dev --project p -- gcloud ls"), { kind: "tool", tool: "Read" }, bash("af-aws-exec --profile dev -- aws s3 ls")], [1, 2, 3]),
    ];
    const p = placeRuns(g, [at(5, { provider: "gcp", profile: "dev" }), at(6, { provider: "aws", profile: "dev" })]);
    expect(p.onTool.get(toolKey(1, 0))?.map((r) => r.id)).toEqual(["r5"]);
    expect(p.onTool.get(toolKey(1, 2))?.map((r) => r.id)).toEqual(["r6"]);
  });
});
