import { describe, expect, it } from "vitest";
import { parseAwsIni } from "./awsConfigIni.ts";
import { readAwsConfig, resolveImport } from "./awsConfigImport.ts";
import { assertSafe, EXPORT_FILENAME, renderAwsConfig } from "./awsConfigExport.ts";

const URL = "https://corp.awsapps.com/start";
const sso = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id,
  label,
  kind: "sso",
  startUrl: URL,
  ssoRegion: "us-east-1",
  accountId: "111111111111",
  roleName: "Admin",
  region: "",
  ...extra,
});
const role = (id: string, label: string, src: string, extra: Record<string, unknown> = {}) => ({
  id,
  label,
  kind: "assume_role",
  sourceProfileId: src,
  roleArn: "arn:aws:iam::222222222222:role/Deploy",
  region: "",
  ...extra,
});
const reasons = (r: ReturnType<typeof renderAwsConfig>) => Object.fromEntries(r.excluded.map((e) => [e.label, e.reason.code]));

describe("renderAwsConfig", () => {
  it("renders an SSO row with its sso-session and a role row from it, as the Agent does", () => {
    const r = renderAwsConfig([sso("a", "prod"), role("b", "deploy", "a", { externalId: "ext-1", sessionName: "sess", durationSeconds: 1800 })]);
    expect(r.text).toContain(
      `[sso-session af-prod]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_registration_scopes = sso:account:access\n\n` +
        `[profile prod]\nsso_session = af-prod\nsso_account_id = 111111111111\nsso_role_name = Admin\nregion = us-east-1\n`,
    );
    expect(r.text).toContain(
      "[profile deploy]\nrole_arn = arn:aws:iam::222222222222:role/Deploy\nsource_profile = prod\nexternal_id = ext-1\nrole_session_name = sess\nduration_seconds = 1800\nregion = us-east-1\n",
    );
    expect(r.included.map((x) => x.name)).toEqual(["prod", "deploy"]);
    expect(r.excluded).toEqual([]);
  });
  it("a role's region is its own, else its source's region, else the source's SSO region (the Agent's chainRegion)", () => {
    const at = (srcRegion: string, roleRegion: string) => {
      const t = renderAwsConfig([sso("a", "prod", { region: srcRegion }), role("b", "deploy", "a", { region: roleRegion })]).text;
      return /\[profile deploy\][^[]*region = (\S+)/.exec(t)![1];
    };
    expect(at("eu-west-1", "ap-northeast-1")).toBe("ap-northeast-1");
    expect(at("eu-west-1", "")).toBe("eu-west-1");
    expect(at("", "")).toBe("us-east-1");
  });
  it("round-trips through the Phase 1 reader: nothing is lost that Settings holds", () => {
    const rows = [sso("a", "prod", { region: "eu-west-1" }), role("b", "deploy", "a", { region: "ap-northeast-1" })];
    const parsed = parseAwsIni(renderAwsConfig(rows).text);
    if (!parsed.ok) throw new Error("not parseable");
    const preview = resolveImport(readAwsConfig(parsed.file), [], {});
    expect(preview.rows.map((x) => [x.name, x.status])).toEqual([
      ["prod", "create"],
      ["deploy", "create"],
    ]);
    expect(preview.rows[0].region).toBe("eu-west-1");
  });
  it("names the file as a fragment, not as a config", () => {
    expect(EXPORT_FILENAME).toMatch(/fragment/);
    expect(EXPORT_FILENAME).not.toMatch(/^config|\/config$/);
    expect(renderAwsConfig([]).text).toMatch(/not a replacement/);
  });
});

describe("exclusions, each with a reason", () => {
  it("default, collisions (both sides), invalid names", () => {
    const r = renderAwsConfig([sso("a", "default"), sso("b", "prod app"), sso("c", "prod-app"), sso("d", "x".repeat(65))]);
    expect(reasons(r)).toEqual({ default: "name_default", "prod app": "name_collision", "prod-app": "name_collision", ["x".repeat(65)]: "name_invalid" });
    expect(r.included).toEqual([]);
  });
  it("incomplete SSO rows, naming the missing fields", () => {
    const r = renderAwsConfig([sso("a", "noacct", { accountId: "" }), sso("b", "none", { accountId: "", roleName: "" })]);
    expect(r.excluded.map((e) => e.reason)).toEqual([
      { code: "incomplete", missing: ["accountId"] },
      { code: "incomplete", missing: ["accountId", "roleName"] },
    ]);
  });
  it("role rows whose source is excluded, missing, or not SSO", () => {
    const r = renderAwsConfig([
      sso("a", "default"),
      role("b", "r1", "a"),
      role("c", "r2", "zzz"),
      sso("d", "ok"),
      role("e", "r3", "d"),
      role("f", "r4", "e"),
    ]);
    expect(reasons(r)).toEqual({ default: "name_default", r1: "source_excluded", r2: "source_missing", r4: "source_not_sso" });
    expect(r.included.map((x) => x.name)).toEqual(["ok", "r3"]);
  });
  it("a role row with a bad ARN, external id, session name or duration", () => {
    const rows = [sso("a", "s")];
    for (const [k, v, f] of [
      ["roleArn", "arn:aws:iam::1:role/x", "roleArn"],
      ["externalId", "a b", "externalId"],
      ["sessionName", "x", "sessionName"],
      ["durationSeconds", 100, "durationSeconds"],
      ["region", "US_EAST", "region"],
    ] as const) {
      const r = renderAwsConfig([...rows, role("b", "r", "a", { [k]: v })]);
      expect(r.excluded.map((e) => e.reason), k).toEqual([{ code: "invalid", field: f }]);
    }
  });
});

describe("injection", () => {
  // CP validation of an SSO row is looser than the Agent's: each value below is something the CP
  // accepts (start URL: https://\S+) or may hold, and each would add a key or section if written as is.
  const hostile: [string, Record<string, unknown>][] = [
    ["startUrl newline", { startUrl: URL + "\naws_secret_access_key = SECRETSENTINEL" }],
    ["startUrl bracket", { startUrl: URL + "]\n[profile evil" }],
    ["startUrl space", { startUrl: URL + " x" }],
    ["startUrl hash", { startUrl: "https://a b#c" }],
    ["ssoRegion", { ssoRegion: "us-east-1\nregion = x" }],
    ["accountId", { accountId: "1\n[profile evil]" }],
    ["roleName", { roleName: "Admin\ncredential_process = /bin/x" }],
    ["roleName eq", { roleName: "Admin = x" }],
    ["region", { region: "a\rb" }],
  ];
  for (const [name, extra] of hostile) {
    it(`leaves out a row whose ${name} could add a key or section`, () => {
      const r = renderAwsConfig([sso("a", "bad", extra), sso("b", "good")]);
      expect(r.included.map((x) => x.name)).toEqual(["good"]);
      expect(r.excluded).toHaveLength(1);
      expect(r.text).not.toContain("SECRETSENTINEL");
      expect(r.text).not.toContain("evil");
      expect(JSON.stringify(r.excluded)).not.toContain("SECRETSENTINEL");
      expect((r.text.match(/^\[/gm) || []).length).toBe(2); // one sso-session, one profile
    });
  }
  it("also for a role row's fields; a label with a newline only becomes a safe name", () => {
    const r = renderAwsConfig([
      sso("a", "s"),
      role("b", "r1", "a", { roleArn: "arn:aws:iam::222222222222:role/D\n[profile evil]" }),
      role("c", "r2", "a", { externalId: "ab\ncredential_process=x" }),
      sso("d", "multi\nline"),
    ]);
    expect(r.text).not.toContain("evil");
    expect(r.text).not.toContain("credential_process");
    expect(r.included.map((x) => x.name)).toEqual(["s", "multi-line"]);
    expect(r.excluded.map((e) => e.label)).toEqual(["r1", "r2"]);
  });
  it("assertSafe refuses what the renderer would never write", () => {
    for (const t of ["[profile x]\nfoo = bar\n", "[profile x]\nregion = a\n[evil]\n", "[profile x]\nregion = [x\n", "region = a\r\nrole_arn = b\n", "[profile a b]\n"]) {
      expect(() => assertSafe(t), t).toThrow();
    }
    expect(() => assertSafe("[profile x]\nexternal_id = =abc\n")).not.toThrow();
  });
});
