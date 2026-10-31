import { describe, expect, it } from "vitest";
import { parseAwsIni } from "./awsConfigIni.ts";
import { hostLabel, readAwsConfig, resolveImport, toImportSection, type ExistingSignIn, type RowEdit } from "./awsConfigImport.ts";
import { planSsmImport } from "./settingsBundle.ts";

const URL = "https://corp.awsapps.com/start";
const SESSION = `[sso-session corp]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_registration_scopes = sso:account:access\n`;
const ARN = "arn:aws:iam::222222222222:role/Deploy";

function read(text: string) {
  const r = parseAwsIni(text);
  if (!r.ok) throw new Error("parse failed");
  return readAwsConfig(r.file);
}
function preview(text: string, existing: any[] = [], edits: Record<string, RowEdit> = {}, signIns: ExistingSignIn[] | null = []) {
  return resolveImport(read(text), existing, edits, signIns);
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
    // Both name the same portal ("corp" is the session's name and the host label), so one sign-in.
    expect(p.logins).toBe(1);
    expect(p.laptopSignIns).toBe(2);
  });

  it("counts one laptop sign-in for profiles sharing an sso-session", () => {
    const p = preview(SESSION + ssoProfile("a") + ssoProfile("b", "222222222222"));
    expect(p.logins).toBe(1);
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
    const legacy = (name: string) =>
      `[profile ${name}]\nsso_start_url = ${URL}\nsso_region = us-east-1\nsso_account_id = 1\nsso_role_name = R\n`;
    const p = preview("[DEFAULT]\nregion = ap-northeast-1\n" + SESSION + ssoProfile("a") + legacy("b") + "region =\n");
    expect(row(p, "a").entry?.region).toBe("ap-northeast-1");
    expect(row(p, "b").entry?.region).toBe("");
    expect(row(p, "b").effectiveRegion).toBe("us-east-1");
  });

  it("refuses any key the profile and its session give differently, [DEFAULT] included", () => {
    // The session inherits [DEFAULT]'s region too, so a profile overriding it disagrees with it.
    const p = preview("[DEFAULT]\nregion = ap-northeast-1\n" + SESSION + ssoProfile("b") + "region = eu-west-1\n");
    expect(row(p, "b").reason).toEqual({ code: "sso_conflict", key: "region" });
    const acct = preview(SESSION + "sso_account_id = 222222222222\n" + ssoProfile("c"));
    expect(row(acct, "c").reason).toEqual({ code: "sso_conflict", key: "sso_account_id" });
    const odd = preview(SESSION + "secretsentinel = 1\n" + ssoProfile("d") + "secretsentinel = 2\n");
    expect(row(odd, "d").reason).toEqual({ code: "sso_conflict", key: "" });
  });

  it("compares nested blocks by their pairs, not their spelling", () => {
    const p = preview(SESSION + "s3 =\n  x=1\n" + ssoProfile("a") + "s3 =\n  x = 1\n");
    expect(row(p, "a").status).toBe("create");
    const q = preview(SESSION + "s3 =\n  x=1\n" + ssoProfile("a") + "s3 =\n  x = 2\n");
    expect(row(q, "a").reason).toEqual({ code: "sso_conflict", key: "s3" });
  });

  it("compares nested blocks as botocore's dicts: type, last value wins, values with delimiters", () => {
    const status = (sess: string, prof: string) => {
      const r = row(preview(SESSION + sess + ssoProfile("a") + prof), "a");
      return r.reason?.code ?? r.status;
    };
    // A string never equals a dict, even when their text matches.
    const p = preview(SESSION.replace("sso_region", "sso_role_name = Admin=\nsso_region") + "[profile a]\nsso_session = corp\nsso_account_id = 1\nsso_role_name =\n  Admin=\n");
    expect(row(p, "a").reason).toEqual({ code: "sso_conflict", key: "sso_role_name" });
    // A repeated key keeps its last value.
    expect(status("s3 =\n  x=1\n  x=2\n", "s3 =\n  x=2\n")).toBe("create");
    expect(status("s3 =\n  x=1\n  x=2\n", "s3 =\n  x=2\n  x=1\n")).toBe("sso_conflict");
    // Delimiters inside a value do not make two different dicts look alike.
    expect(status("s3 =\n  a=1,b=2\n", "s3 =\n  a=1\n  b=2\n")).toBe("sso_conflict");
  });

  it("does not take a region only the sso-session sets", () => {
    const p = preview(SESSION + "region = eu-west-1\n" + ssoProfile("a"));
    expect(row(p, "a")).toMatchObject({ status: "create", entry: { region: "" }, effectiveRegion: "us-east-1" });
  });

  it("takes the role from the session when the profile names the account", () => {
    const p = preview(SESSION + "sso_role_name = Admin\n[profile a]\nsso_session = corp\nsso_account_id = 111111111111\n");
    expect(row(p, "a")).toMatchObject({ status: "create", entry: { roleName: "Admin" } });
    const none = preview(SESSION + "sso_role_name = Admin\nsso_account_id = 1\n[profile a]\nsso_session = corp\n");
    expect(row(none, "a").reason).toMatchObject({ code: "incomplete_sso" });
  });

  it("does not read a nested block's first line as a value", () => {
    const p = preview(SESSION + "[profile a]\nsso_session = corp\nsso_account_id = 111111111111\nsso_role_name =\n  aws_secret_access_key=SECRETSENTINEL\n");
    expect(row(p, "a").reason).toEqual({ code: "invalid", key: "sso_role_name" });
    expect(JSON.stringify(p)).not.toContain("SECRETSENTINEL");
  });

  it("does not show or propose a profile name that holds a key assignment", () => {
    const p = preview(SESSION + ssoProfile('"aws_secret_access_key=SECRETSENTINEL"'));
    const r = p.rows[0];
    expect(r).toMatchObject({ display: "", label: "", reason: { code: "label_empty" } });
    expect(JSON.stringify({ ...r, name: "" })).not.toContain("SECRETSENTINEL");
    // A key assignment that names no known key is held back too.
    const q = preview(SESSION + ssoProfile('"token=SECRETSENTINEL"'));
    expect(q.rows[0]).toMatchObject({ display: "", label: "" });
    for (const n of ['"credential_process:cmd-PROCSENTINEL"', '"token:SENTINEL"']) {
      const c = preview(SESSION + ssoProfile(n));
      expect(c.rows[0]).toMatchObject({ display: "", label: "" });
      expect(toImportSection(c).profiles).toEqual([]);
    }
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

  it("keeps the picked row's id through the planner when two rows share its label", () => {
    const text = `[profile deploy]\nrole_arn = ${ARN}\nsource_profile = elsewhere\n`;
    const existing = [
      { id: "e1", kind: "sso", label: "Elsewhere", startUrl: URL, ssoRegion: "us-east-1", accountId: "1", roleName: "R" },
      { id: "e2", kind: "sso", label: "Elsewhere", startUrl: URL, ssoRegion: "us-east-1", accountId: "2", roleName: "R" },
    ];
    const p = preview(text, existing, { deploy: { sourceId: "e1" } });
    const plan = planSsmImport(toImportSection(p), existing, []);
    expect(plan.skippedProfiles).toEqual([]);
    expect(plan.profiles[0]).toMatchObject({ label: "deploy", sourceId: "e1" });
  });

  it("shows the region of the existing row a source resolves to", () => {
    const existing = [{ id: "d1", kind: "sso", label: "dev", startUrl: URL, ssoRegion: "us-east-1", accountId: "111111111111", roleName: "Admin", region: "eu-west-1" }];
    const p = preview(chain, existing);
    expect(row(p, "dev")).toMatchObject({ status: "exists", effectiveRegion: "eu-west-1" });
    expect(row(p, "deploy").effectiveRegion).toBe("eu-west-1");
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

  it("does not keep a clash with a row that dropped out for its source", () => {
    // "a b" clashes with the existing a-b, so the role on it cannot be created; the SSO
    // "deploy app" it would have clashed with is then free.
    const existing = [{ id: "x", kind: "sso", label: "a-b", startUrl: URL, ssoRegion: "us-east-1", accountId: "9", roleName: "R" }];
    const text = SESSION + ssoProfile('"a b"') + ssoProfile('"deploy app"', "2") + `[profile deploy-app]\nrole_arn = ${ARN}\nsource_profile = a b\n`;
    const p = preview(text, existing);
    expect(row(p, "a b").reason).toEqual({ code: "name_collision", other: "a-b" });
    expect(row(p, "deploy-app").reason).toEqual({ code: "source_unsupported", source: "a b" });
    expect(row(p, "deploy app").status).toBe("create");
  });

  it("blocks both sides when a role clashes with its own source", () => {
    const text = SESSION + ssoProfile('"x y"') + `[profile x-y]\nrole_arn = ${ARN}\nsource_profile = x y\n`;
    const p = preview(text);
    expect(row(p, "x y").status).toBe("blocked");
    expect(row(p, "x-y").status).toBe("blocked");
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

// ADR 0125 decision 6 / B: the import proposes the laptop's sign-ins.
describe("resolveImport — shared sign-ins", () => {
  const legacy = (name: string, url: string, region = "us-east-1", account = "1") =>
    `[profile ${name}]\nsso_start_url = ${url}\nsso_region = ${region}\nsso_account_id = ${account}\nsso_role_name = R\n`;
  const signInOf = (p: ReturnType<typeof preview>, n: string) => (row(p, n).entry as any).signIn;

  it("defaults an sso-session profile to signIn = the session's name, and counts one login per sign-in", () => {
    const p = preview(SESSION + ssoProfile("a") + ssoProfile("b", "222222222222"));
    expect(signInOf(p, "a")).toBe("corp");
    expect(signInOf(p, "b")).toBe("corp");
    expect(toImportSection(p).profiles.map((e) => e.signIn)).toEqual(["corp", "corp"]);
  });

  it("names a legacy profile's sign-in after its portal's host label, one per start URL and region", () => {
    const a = "https://d-1234567890.awsapps.com/start";
    const p = preview(legacy("x", a) + legacy("y", a, "us-east-1", "2") + legacy("z", "https://other.awsapps.com/start"));
    expect(signInOf(p, "x")).toBe("d-1234567890");
    expect(signInOf(p, "y")).toBe("d-1234567890");
    expect(signInOf(p, "z")).toBe("other");
    expect(p.logins).toBe(2);
  });

  it("proposes no name for a host label outside the allowlist", () => {
    expect(hostLabel("https://a~b.example.com/start")).toBe("");
    expect(hostLabel("https://" + "a".repeat(62) + ".example.com/start")).toBe("");
    expect(hostLabel("https://d-1.awsapps.com/start")).toBe("d-1");
  });

  it("blocks a session name outside the allowlist until a name is typed or it is cleared, never echoing it", () => {
    const long = "s".repeat(62);
    const text = `[sso-session ${long}]\nsso_start_url = ${URL}\nsso_region = us-east-1\n[profile a]\nsso_session = ${long}\nsso_account_id = 1\nsso_role_name = R\n`;
    const p = preview(text);
    expect(row(p, "a")).toMatchObject({ status: "blocked", reason: { code: "sign_in_invalid" }, signInName: "" });
    expect(JSON.stringify(p.rows.map((r) => [r.signInName, r.reason]))).not.toContain(long);
    expect(row(preview(text, [], { a: { signIn: "team" } }), "a")).toMatchObject({ status: "create", signInName: "team" });
    const own = row(preview(text, [], { a: { signIn: "" } }), "a");
    expect(own.status).toBe("create");
    expect((own.entry as any).signIn).toBeUndefined();
    expect(row(preview(text, [], { a: { signIn: "bad name" } }), "a").reason).toEqual({ code: "sign_in_invalid" });
  });

  it("blocks a name an existing sign-in holds for another portal (sign_in_mismatch), and joins one on the same portal", () => {
    const other = { name: "corp", startUrl: "https://other.awsapps.com/start", ssoRegion: "us-east-1" };
    const p = preview(SESSION + ssoProfile("a"), [], {}, [other]);
    expect(row(p, "a")).toMatchObject({ status: "blocked", reason: { code: "sign_in_mismatch" } });
    expect(row(preview(SESSION + ssoProfile("a"), [], { a: { signIn: "corp2" } }, [other]), "a").status).toBe("create");
    const same = preview(SESSION + ssoProfile("a"), [], {}, [{ name: "corp", startUrl: URL, ssoRegion: "us-east-1" }]);
    expect(row(same, "a")).toMatchObject({ status: "create", signInJoins: true });
    expect(same.logins).toBe(0);
  });

  it("blocks a name that an existing profile with its own sign-in already logs in under (sign_in_name_taken)", () => {
    const mine = { id: "p1", kind: "sso", label: "corp", startUrl: "https://z.awsapps.com/start", ssoRegion: "us-east-1", accountId: "9", roleName: "R" };
    const p = preview(SESSION + ssoProfile("a"), [mine]);
    expect(row(p, "a")).toMatchObject({ status: "blocked", reason: { code: "sign_in_name_taken", other: "corp" } });
    // The same profile already in a sign-in keeps no name of its own.
    expect(row(preview(SESSION + ssoProfile("a"), [{ ...mine, signIn: "other" }]), "a").status).toBe("create");
  });

  it("blocks, among the creations, one name on two portals and a name a cleared row logs in under; unticking frees it", () => {
    const text =
      legacy("x", "https://d-1.awsapps.com/start") +
      legacy("y", "https://d-1.awsapps.com/start", "eu-west-1", "2");
    const p = preview(text);
    expect(row(p, "x").reason).toEqual({ code: "sign_in_mismatch" });
    expect(row(p, "y").reason).toEqual({ code: "sign_in_mismatch" });
    const un = preview(text, [], { y: { selected: false } });
    expect(row(un, "x").status).toBe("create");
    const taken = preview(SESSION + ssoProfile("a") + ssoProfile("corp", "2"), [], { corp: { signIn: "" } });
    expect(row(taken, "a").reason).toEqual({ code: "sign_in_name_taken", other: "corp" });
    expect(row(taken, "corp").status).toBe("create");
  });

  it("proposes and sends no sign-in when the member's sign-ins could not be read", () => {
    const p = preview(SESSION + ssoProfile("a"), [], { a: { signIn: "typed" } }, null);
    expect(p.signInsUnknown).toBe(true);
    expect(row(p, "a")).toMatchObject({ status: "create", signInName: "" });
    expect((row(p, "a").entry as any).signIn).toBeUndefined();
  });
});
