import { describe, expect, it } from "vitest";
import { parseAwsIni } from "./awsConfigIni.ts";
import { readAwsConfig, resolveImport, toImportSection, type RowEdit } from "./awsConfigImport.ts";
import { planSsmImport } from "./settingsBundle.ts";

const URL = "https://corp.awsapps.com/start";
const SESSION = `[sso-session corp]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_registration_scopes = sso:account:access\n`;
const ARN = "arn:aws:iam::222222222222:role/Deploy";

function read(text: string) {
  const r = parseAwsIni(text);
  if (!r.ok) throw new Error("parse failed");
  return readAwsConfig(r.file);
}
function preview(text: string, existing: any[] = [], edits: Record<string, RowEdit> = {}) {
  return resolveImport(read(text), existing, edits);
}
const row = (p: ReturnType<typeof preview>, name: string) => p.rows.find((r) => r.name === name)!;

const ssoProfile = (name: string, account = "111111111111", role = "Admin") =>
  `[profile ${name}]\nsso_session = corp\nsso_account_id = ${account}\nsso_role_name = ${role}\n`;

describe("readAwsConfig / resolveImport — SSO", () => {
  it("creates an sso-session profile and a legacy one", () => {
    const p = preview(
      SESSION +
        ssoProfile("dev") +
        `[profile old]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_account_id = 333333333333\nsso_role_name = Read\nregion = eu-west-1\n`,
    );
    expect(row(p, "dev")).toMatchObject({
      status: "create",
      kind: "sso",
      entry: { label: "dev", startUrl: URL, ssoRegion: "us-east-1", accountId: "111111111111", roleName: "Admin", region: "" },
      effectiveRegion: "us-east-1",
    });
    expect(row(p, "old")).toMatchObject({ status: "create", effectiveRegion: "eu-west-1" });
    expect(p.logins).toBe(2);
    expect(p.laptopSignIns).toBe(2);
  });

  it("counts one laptop sign-in for profiles sharing an sso-session", () => {
    const p = preview(SESSION + ssoProfile("a") + ssoProfile("b", "222222222222"));
    expect(p.logins).toBe(2);
    expect(p.laptopSignIns).toBe(1);
  });

  it("refuses an SSO profile without account and role, and one whose session is missing", () => {
    const p = preview(SESSION + "[profile portal]\nsso_session = corp\n[profile lost]\nsso_session = nope\nsso_account_id = 1\nsso_role_name = R\n");
    expect(row(p, "portal")).toMatchObject({ status: "blocked", reason: { code: "incomplete_sso", missing: ["sso_account_id", "sso_role_name"] } });
    expect(row(p, "lost")).toMatchObject({ status: "blocked", reason: { code: "no_session", session: "nope" } });
  });

  it("refuses a profile whose sso_start_url disagrees with its session's", () => {
    const p = preview(SESSION + ssoProfile("x") + "sso_start_url = https://other.awsapps.com/start\n");
    expect(row(p, "x").reason).toEqual({ code: "sso_conflict", key: "sso_start_url" });
  });

  it("applies [DEFAULT]: its region reaches every profile, an explicit empty value overrides it", () => {
    const p = preview("[DEFAULT]\nregion = ap-northeast-1\n" + SESSION + ssoProfile("a") + ssoProfile("b") + "region =\n");
    expect(row(p, "a").entry?.region).toBe("ap-northeast-1");
    expect(row(p, "b").entry?.region).toBe("");
    expect(row(p, "b").effectiveRegion).toBe("us-east-1");
  });

  it("treats a credential_process inherited from [DEFAULT] as a second way to sign in", () => {
    const p = preview("[DEFAULT]\ncredential_process = /bin/creds\n" + SESSION + ssoProfile("a"));
    expect(row(p, "a")).toMatchObject({ status: "blocked", reason: { code: "mixed_auth" } });
  });

  it("flags a scope other than the one Settings uses", () => {
    const p = preview(SESSION.replace("sso:account:access", "sso:account:access,codewhisperer:completions") + ssoProfile("a"));
    expect(row(p, "a")).toMatchObject({ status: "create", scopesChange: true });
  });

  it("refuses a value outside the Agent's allowlist and never keeps it", () => {
    const p = preview(SESSION + ssoProfile("a") + "region = us-east-1 # c\n");
    expect(row(p, "a").reason).toEqual({ code: "invalid", key: "region" });
    expect(JSON.stringify(p)).not.toContain("# c");
  });
});

describe("readAwsConfig — other ways to sign in", () => {
  it("refuses keys, unsupported methods and mixes, naming only the key", () => {
    const p = preview(
      SESSION +
        "[profile keys]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = SECRETVALUE\n" +
        "[profile proc]\ncredential_process = /usr/bin/get-creds --token SECRETARG\n" +
        `[profile mfa]\nrole_arn = ${ARN}\nsource_profile = dev\nmfa_serial = arn:aws:iam::1:mfa/me\n` +
        "[profile ls]\nlogin_session = x\n" +
        ssoProfile("mixed") +
        "aws_session_token = TOKENVALUE\n" +
        "[profile none]\noutput = json\n",
    );
    expect(row(p, "keys").reason).toEqual({ code: "keys" });
    expect(row(p, "proc").reason).toEqual({ code: "auth", key: "credential_process" });
    expect(row(p, "mfa").reason).toEqual({ code: "auth", key: "mfa_serial" });
    expect(row(p, "ls").reason).toEqual({ code: "auth", key: "login_session" });
    expect(row(p, "mixed").reason).toEqual({ code: "mixed_auth" });
    expect(row(p, "none").reason).toEqual({ code: "no_auth" });
    const s = JSON.stringify(p);
    for (const secret of ["AKIAEXAMPLE", "SECRETVALUE", "SECRETARG", "TOKENVALUE"]) expect(s).not.toContain(secret);
    expect(read(SESSION + "[profile keys]\naws_access_key_id = AKIAEXAMPLE\n").secrets).toBe(true);
    expect(read(SESSION + ssoProfile("dev")).secrets).toBe(false);
  });

  it("does not turn a continuation line holding a key into a role name", () => {
    const text = SESSION + ssoProfile("a", "111111111111", "Admin\n  aws_secret_access_key = SECRETVALUE");
    const cfg = read(text);
    expect(cfg.secrets).toBe(true);
    const p = resolveImport(cfg, [], {});
    expect(row(p, "a").reason).toEqual({ code: "invalid", key: "sso_role_name" });
    expect(JSON.stringify(p)).not.toContain("SECRETVALUE");
  });
});

describe("resolveImport — role chains", () => {
  const chain = SESSION + ssoProfile("dev") + `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = dev\nrole_session_name = me\n`;

  it("chains to a source in the file regardless of order, and the planner accepts it", () => {
    const text = `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = dev\n` + SESSION + ssoProfile("dev");
    const p = preview(text);
    expect(row(p, "deploy")).toMatchObject({
      status: "create",
      source: { via: "file", label: "dev" },
      entry: { kind: "assume_role", source: "dev", roleArn: ARN, accountId: "", startUrl: "" },
      effectiveRegion: "us-east-1",
    });
    expect(p.logins).toBe(1);
    const plan = planSsmImport(toImportSection(p), [], []);
    expect(plan.skippedProfiles).toEqual([]);
    expect(plan.profiles.map((x) => x.label)).toEqual(["dev", "deploy"]);
  });

  it("refuses a multi-hop chain and a chain to a non-importable source", () => {
    const p = preview(
      chain +
        `[profile hop2]\nrole_arn = ${ARN}\nsource_profile = deploy\n` +
        "[profile keys]\naws_access_key_id = A\n" +
        `[profile viakeys]\nrole_arn = ${ARN}\nsource_profile = keys\n`,
    );
    expect(row(p, "hop2").reason).toEqual({ code: "multi_hop", source: "deploy" });
    expect(row(p, "viakeys").reason).toEqual({ code: "source_unsupported", source: "keys" });
  });

  it("keeps a renamed source attached and blocks dependents of an unticked one", () => {
    const text = SESSION + ssoProfile("default") + `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = default\n`;
    expect(row(preview(text), "default").reason).toEqual({ code: "name_default" });
    expect(row(preview(text), "deploy").reason).toEqual({ code: "source_unsupported", source: "default" });
    const renamed = preview(text, [], { default: { label: "main" } });
    expect(row(renamed, "deploy")).toMatchObject({ status: "create", source: { label: "main" } });
    const unticked = preview(text, [], { default: { label: "main", selected: false } });
    expect(row(unticked, "deploy").reason).toEqual({ code: "source_unselected", source: "default" });
  });

  it("after a partial success, joins the source that now exists under its renamed label", () => {
    const text = SESSION + ssoProfile("default") + `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = default\n`;
    const existing = [{ id: "m1", kind: "sso", label: "main", startUrl: URL, ssoRegion: "us-east-1", accountId: "111111111111", roleName: "Admin" }];
    const p = preview(text, existing, { default: { label: "main" } });
    expect(row(p, "default")).toMatchObject({ status: "exists", existingId: "m1" });
    expect(row(p, "deploy")).toMatchObject({ status: "create", source: { via: "existing", label: "main" } });
    const plan = planSsmImport(toImportSection(p), existing, []);
    expect(plan.profiles.map((x) => x.label)).toEqual(["deploy"]);
  });

  it("joins a converted source name the same way (prod app -> prod-app)", () => {
    const text = SESSION + ssoProfile('"prod app"') + `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = prod app\n`;
    const existing = [{ id: "p1", kind: "sso", label: "prod app", startUrl: URL, ssoRegion: "us-east-1", accountId: "111111111111", roleName: "Admin" }];
    const p = preview(text, existing);
    expect(row(p, "prod app")).toMatchObject({ status: "exists", awsName: "prod-app" });
    expect(row(p, "deploy")).toMatchObject({ status: "create", source: { via: "existing", label: "prod app" } });
  });

  it("does not join an existing row of the same name with another sign-in", () => {
    const existing = [{ id: "d1", kind: "sso", label: "dev", startUrl: URL, ssoRegion: "us-east-1", accountId: "999999999999", roleName: "Admin" }];
    const p = preview(chain, existing);
    expect(row(p, "dev").reason).toEqual({ code: "label_taken", other: "dev" });
    expect(row(p, "deploy").reason).toEqual({ code: "source_unsupported", source: "dev" });
  });

  it("needs the member's pick for a source the file does not define, and marks it as picked", () => {
    const text = `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = elsewhere\n`;
    const existing = [{ id: "e1", kind: "sso", label: "Elsewhere", startUrl: URL, ssoRegion: "us-east-1", accountId: "1", roleName: "R", region: "eu-west-1" }];
    expect(row(preview(text, existing), "deploy").reason).toEqual({ code: "source_missing", source: "elsewhere" });
    const p = preview(text, existing, { deploy: { sourceId: "e1" } });
    expect(row(p, "deploy")).toMatchObject({ status: "create", source: { via: "picked", label: "Elsewhere" }, effectiveRegion: "eu-west-1" });
    expect(p.sourceChoices.map((c) => c.id)).toEqual(["e1"]);
    expect(planSsmImport(toImportSection(p), existing, []).profiles.map((x) => x.label)).toEqual(["deploy"]);
  });

  it("checks duration_seconds and lets the preview fix it", () => {
    const bad = (d: string) => chain + `duration_seconds = ${d}\n`;
    expect(row(preview(bad("7200")), "deploy").reason).toEqual({ code: "bad_duration" });
    expect(row(preview(bad("899")), "deploy").reason).toEqual({ code: "bad_duration" });
    expect(row(preview(bad("12abc")), "deploy").reason).toEqual({ code: "bad_duration" });
    expect(row(preview(bad("900")), "deploy").durationSeconds).toBe(900);
    expect(row(preview(bad("3600")), "deploy").durationSeconds).toBe(3600);
    expect(row(preview(bad("7200"), [], { deploy: { duration: "3600" } }), "deploy")).toMatchObject({ status: "create", durationSeconds: 3600 });
    expect(row(preview(bad("12abc"), [], { deploy: { duration: "" } }), "deploy")).toMatchObject({ status: "create", durationSeconds: undefined });
  });
});

describe("resolveImport — names", () => {
  it("reports an existing identical profile as exists, not as a collision", () => {
    const existing = [{ id: "d1", kind: "sso", label: "Dev", startUrl: URL + "/", ssoRegion: "us-east-1", accountId: "111111111111", roleName: "Admin" }];
    const p = preview(SESSION + ssoProfile("dev"), existing);
    expect(row(p, "dev")).toMatchObject({ status: "exists", existingId: "d1" });
    expect(p.logins).toBe(0);
  });

  it("refuses labels that only differ in case and names that convert to the same AWS name", () => {
    const p = preview(SESSION + ssoProfile("Prod") + ssoProfile("prod", "2") + ssoProfile('"a b"', "3") + ssoProfile("a-b", "4"));
    expect(row(p, "Prod").reason).toEqual({ code: "label_dup", other: "prod" });
    expect(row(p, "prod").reason).toEqual({ code: "label_dup", other: "Prod" });
    expect(row(p, "a b").reason).toEqual({ code: "name_collision", other: "a-b" });
    expect(row(p, "a-b").reason).toEqual({ code: "name_collision", other: "a b" });
  });

  it("refuses a name that collides with an existing row after conversion, and its dependents", () => {
    const existing = [{ id: "x", kind: "sso", label: "a b", startUrl: URL, ssoRegion: "us-east-1", accountId: "9", roleName: "R" }];
    const p = preview(SESSION + ssoProfile("a-b") + `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = a-b\n`, existing);
    expect(row(p, "a-b").reason).toEqual({ code: "name_collision", other: "a b" });
    expect(row(p, "deploy").reason).toEqual({ code: "source_unsupported", source: "a-b" });
  });

  it("refuses a label whose AWS name is too long, and lets a rename fix it", () => {
    const long = "x".repeat(65);
    expect(row(preview(SESSION + ssoProfile(long)), long).reason).toEqual({ code: "name_too_long" });
    expect(row(preview(SESSION + ssoProfile(long), [], { [long]: { label: "short" } }), long).status).toBe("create");
  });

  it("leaves an unticked row out of the creations", () => {
    const p = preview(SESSION + ssoProfile("a"), [], { a: { selected: false } });
    expect(row(p, "a").status).toBe("unselected");
    expect(toImportSection(p).profiles).toEqual([]);
  });

  it("refuses a file with too many profiles", () => {
    let text = SESSION;
    for (let i = 0; i < 501; i++) text += ssoProfile("p" + i);
    expect(read(text).tooMany).toBe(true);
  });
});
