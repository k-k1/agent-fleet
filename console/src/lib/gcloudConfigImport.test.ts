// The gcloud configuration reader and preview (issue #3017, part 2): what maps to a profile,
// what is refused with which reason, and that the value checks match the CP's.
import { describe, it, expect } from "vitest";
import {
  GCP_EMAIL_RE,
  GCP_PROJECT_RE,
  GCP_REGION_RE,
  GCP_SERVICE_ACCOUNT_RE,
  GCP_ZONE_RE,
  configNameOfFile,
  planGcpCreates,
  readGcloudSource,
  resolveGcpImport,
  type GcpReason,
  type NativeGcpConfig,
} from "./gcloudConfigImport.ts";
import { parseAwsIni, parseIni } from "./awsConfigIni.ts";

const read = (name: string | null, text: string, index = 0) => readGcloudSource(index, name, text);
const reasonOf = (c: NativeGcpConfig): GcpReason | null => c.reason;
const BASE = "[core]\nproject = my-project-123\n";

describe("readGcloudSource: mapping", () => {
  it("maps the six fields from their sections", () => {
    const c = read(
      "prod",
      `[core]
account = me@example.com
project = my-project-123
[billing]
quota_project = quota-project-1
[compute]
region = asia-northeast1
zone = asia-northeast1-a
[auth]
impersonate_service_account = deployer@my-project-123.iam.gserviceaccount.com
[run]
region = us-central1
`,
    );
    expect(c.reason).toBeNull();
    expect(c.fields).toEqual({
      project: "my-project-123",
      quotaProject: "quota-project-1",
      account: "me@example.com",
      region: "asia-northeast1",
      zone: "asia-northeast1-a",
      impersonateServiceAccount: "deployer@my-project-123.iam.gserviceaccount.com",
    });
    expect(c.ignored).toBe(1); // run/region
  });

  it("applies [DEFAULT] to every section, as ConfigParser does, and honours its inherited refusals", () => {
    expect(read("a", "[DEFAULT]\nproject = my-project-123\n[core]\naccount = me@example.com\n").fields?.project).toBe("my-project-123");
    // an override inherited from [DEFAULT] reaches [auth]
    const c = read("a", `[DEFAULT]\naccess_token_file = /x/y\n${BASE}[auth]\nimpersonate_service_account =\n`);
    expect(reasonOf(c)).toEqual({ code: "auth_override", key: "auth/access_token_file" });
  });

  it("is case-insensitive in keys only, like ConfigParser", () => {
    expect(read("a", "[core]\nPROJECT = my-project-123\n").fields?.project).toBe("my-project-123");
    expect(read("a", "[Core]\nproject = my-project-123\n").reason).toEqual({ code: "no_project" });
  });

  it("does not apply botocore's nested-value rule", () => {
    const text = "[core]\nproject = my-project-123\n[other]\nk =\n  not a pair\n";
    expect(parseAwsIni(text).ok).toBe(false);
    expect(read("a", text).reason).toBeNull();
    expect(parseIni(text, false).ok).toBe(true);
  });

  it("reports a parse error by line and key only", () => {
    const c = read("a", `[core]\nproject = my-project-123\nproject = SECRETSENTINEL\n`);
    expect(c.reason).toEqual({ code: "parse", error: { code: "duplicate_key", line: 3, key: "project" } });
    expect(JSON.stringify(c)).not.toContain("SECRETSENTINEL");
  });
});

describe("readGcloudSource: refusals", () => {
  const cases: [string, string, GcpReason][] = [
    ["no project", "[core]\naccount = me@example.com\n", { code: "no_project" }],
    ["credential file", `${BASE}[auth]\ncredential_file_override = /home/u/key.json\n`, { code: "auth_override", key: "auth/credential_file_override" }],
    ["access token file", `${BASE}[auth]\naccess_token_file = /home/u/tok\n`, { code: "auth_override", key: "auth/access_token_file" }],
    ["custom oauth client", `${BASE}[auth]\nclient_id = abc\n`, { code: "auth_override", key: "auth/client_id" }],
    ["token host", `${BASE}[auth]\ntoken_host = https://x.example\n`, { code: "auth_override", key: "auth/token_host" }],
    ["credentials disabled", `${BASE}[auth]\ndisable_credentials = true\n`, { code: "auth_override", key: "auth/disable_credentials" }],
    ["unknown auth key", `${BASE}[auth]\nsomething_new = 1\n`, { code: "auth_override", key: "?" }],
    ["delegation chain", `${BASE}[auth]\nimpersonate_service_account = a@p.iam.gserviceaccount.com,b@p.iam.gserviceaccount.com\n`, { code: "delegation" }],
    ["endpoint override", `${BASE}[api_endpoint_overrides]\ncompute = https://c.example/\n`, { code: "endpoint_override" }],
    ["proxy", `${BASE}[proxy]\naddress = proxy.example\nport = 3128\n`, { code: "proxy" }],
    ["universe domain", `[core]\nproject = my-project-123\nuniverse_domain = example.test\n`, { code: "universe", key: "core/universe_domain" }],
    ["custom CA", `[core]\nproject = my-project-123\ncustom_ca_certs_file = /etc/ca.pem\n`, { code: "universe", key: "core/custom_ca_certs_file" }],
    ["service account as account", "[core]\nproject = my-project-123\naccount = sa@my-project-123.iam.gserviceaccount.com\n", { code: "sa_account" }],
    ["default service account as account", "[core]\nproject = my-project-123\naccount = 123-compute@developer.gserviceaccount.com\n", { code: "sa_account" }],
    ["bad project", "[core]\nproject = 123456789012\n", { code: "invalid", key: "core/project" }],
    ["bad quota project", `${BASE}[billing]\nquota_project = X\n`, { code: "invalid", key: "billing/quota_project" }],
    ["bad account", "[core]\nproject = my-project-123\naccount = not-an-email\n", { code: "invalid", key: "core/account" }],
    ["impersonate a non-service account", `${BASE}[auth]\nimpersonate_service_account = me@example.com\n`, { code: "invalid", key: "auth/impersonate_service_account" }],
    ["bad region", `${BASE}[compute]\nregion = Tokyo\n`, { code: "invalid", key: "compute/region" }],
    ["bad zone", `${BASE}[compute]\nzone = asia-northeast1\n`, { code: "invalid", key: "compute/zone" }],
    ["key material", `[core]\nproject = my-project-123\naccount = "private_key": "x"\n`, { code: "key_material" }],
  ];
  for (const [name, text, reason] of cases) {
    it(name, () => {
      const c = read("a", text);
      expect(c.fields).toBeNull();
      expect(reasonOf(c)).toEqual(reason);
    });
  }

  it("lets harmless values through: empty overrides, disable_credentials = false, the default universe", () => {
    const c = read("a", `${BASE}universe_domain = googleapis.com\n[auth]\nimpersonate_service_account =\naccess_token_file =\ndisable_credentials = false\n[proxy]\ntype =\n`);
    expect(c.reason).toBeNull();
  });

  it("never puts a value in a reason", () => {
    for (const [, text] of cases) {
      const c = read("a", text.replace(/\/home\/u\/\S+|\/etc\/ca\.pem|abc/g, "VALUESENTINEL"));
      expect(JSON.stringify(c.reason)).not.toContain("VALUESENTINEL");
    }
  });

  it("refuses a file that is not config_<name>, and a name gcloud would not accept", () => {
    expect(configNameOfFile("config_prod")).toBe("prod");
    expect(configNameOfFile("active_config")).toBeNull();
    expect(read(configNameOfFile("active_config"), BASE).reason).toEqual({ code: "not_config" });
    expect(read("Prod", BASE).reason).toEqual({ code: "bad_name" });
    expect(read("a-", BASE).reason).toEqual({ code: "bad_name" });
  });
});

// The same boundaries control-plane/gcp_profiles.go enforces (gcpProjectRe and friends).
describe("the CP's value checks", () => {
  it("project", () => {
    for (const ok of ["abcde1", "my-project-123", "a" + "b".repeat(28) + "c", "example.com:my-project"]) expect(GCP_PROJECT_RE.test(ok), ok).toBe(true);
    for (const bad of ["abcde", "a" + "b".repeat(29) + "c", "1abcde", "abcde-", "My-Project", "my_project", "example.com:ab", ""]) expect(GCP_PROJECT_RE.test(bad), bad).toBe(false);
  });
  it("email, service account, region, zone", () => {
    expect(GCP_EMAIL_RE.test("a.b@example.co")).toBe(true);
    for (const bad of ["a@b", "a b@example.com", "a@example.com\n", "a,b@example.com", "@example.com"]) expect(GCP_EMAIL_RE.test(bad), bad).toBe(false);
    for (const ok of ["d@my-project-123.iam.gserviceaccount.com", "1-compute@developer.gserviceaccount.com", "p@appspot.gserviceaccount.com"]) expect(GCP_SERVICE_ACCOUNT_RE.test(ok), ok).toBe(true);
    for (const bad of ["me@example.com", "D@p.iam.gserviceaccount.com", "d@gserviceaccount.com.evil.com", "-d@p.gserviceaccount.com"]) expect(GCP_SERVICE_ACCOUNT_RE.test(bad), bad).toBe(false);
    expect(GCP_REGION_RE.test("asia-northeast1")).toBe(true);
    expect(GCP_REGION_RE.test("asia-northeast")).toBe(false);
    expect(GCP_ZONE_RE.test("asia-northeast1-a")).toBe(true);
    expect(GCP_ZONE_RE.test("asia-northeast1")).toBe(false);
  });
});

const ok = (name: string, extra = "") => read(name, `[core]\nproject = my-project-123\n${extra}`);
const existingRow = (o: Record<string, string>) => ({ loginMethod: "google", project: "my-project-123", quotaProject: "", account: "", region: "", zone: "", impersonateServiceAccount: "", ...o });

describe("resolveGcpImport", () => {
  it("lists every configuration as create / exists / blocked, in order", () => {
    const cfgs = [ok("new", ""), ok("same"), ok("other"), read("bad", "[core]\naccount = me@example.com\n")].map((c, i) => ({ ...c, index: i }));
    const rows = resolveGcpImport(
      cfgs,
      [existingRow({ label: "same", name: "same" }), existingRow({ label: "other", name: "other", project: "different-proj-1" })],
      {},
    ).rows;
    expect(rows.map((r) => r.status)).toEqual(["create", "exists", "blocked", "blocked"]);
    expect(rows[2].reason).toEqual({ code: "label_taken", other: "other" });
    expect(rows[3].reason).toEqual({ code: "no_project" });
  });

  it("matches an existing row by its derived name too, and ignores case in its label", () => {
    const rows = resolveGcpImport([ok("prod")], [existingRow({ label: "Prod", name: "prod" })], {}).rows;
    expect(rows[0].status).toBe("exists");
  });

  it("honours unticking and a renamed label, and refuses a duplicate or bad label", () => {
    const cfgs = [ok("a"), ok("b", ""), ok("c")].map((c, i) => ({ ...c, index: i }));
    const r = resolveGcpImport(cfgs, [], { 0: { selected: false }, 1: { label: "a" }, 2: { label: "Bad Label" } }).rows;
    expect(r.map((x) => x.status)).toEqual(["unselected", "create", "blocked"]);
    expect(r[2].reason).toEqual({ code: "label_bad" });
    const dup = resolveGcpImport([ok("a"), { ...ok("b"), index: 1 }], [], { 1: { label: "a" } }).rows;
    expect(dup[1].reason).toEqual({ code: "label_dup", other: "a" });
    expect(resolveGcpImport([ok("a")], [], { 0: { label: " " } }).rows[0].reason).toEqual({ code: "label_empty" });
  });

  it("plans only the rows it creates, with the profile fields and nothing else", () => {
    const rows = resolveGcpImport([ok("a", "[compute]\nzone = asia-northeast1-a\n"), read("b", "[auth]\naccess_token_file = /p\n")].map((c, i) => ({ ...c, index: i })), [], {}).rows;
    const plan = planGcpCreates(rows);
    expect(plan).toHaveLength(1);
    expect(Object.keys(plan[0].fields).sort()).toEqual(["account", "impersonateServiceAccount", "project", "quotaProject", "region", "zone"]);
  });
});
