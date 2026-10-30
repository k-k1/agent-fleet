// Expected values were taken from Python's configparser.RawConfigParser (the AWS CLI v2's
// reader) on the same inputs.
import { describe, expect, it } from "vitest";
import { parseAwsIni, sectionRole } from "./awsConfigIni.ts";

function ok(text: string) {
  const r = parseAwsIni(text);
  if (!r.ok) throw new Error("parse failed: " + JSON.stringify(r.error));
  return r.file;
}
const opt = (text: string, sec: number, key: string) => ok(text).sections[sec].options.get(key)?.value;

describe("parseAwsIni", () => {
  it("joins an indented line to the previous value instead of reading a key", () => {
    const text = "[profile a]\nsso_role_name = Admin\n  aws_secret_access_key = X\nregion=us-east-1\n";
    expect(opt(text, 0, "sso_role_name")).toBe("Admin\naws_secret_access_key = X");
    expect(ok(text).sections[0].options.has("aws_secret_access_key")).toBe(false);
    expect(opt(text, 0, "region")).toBe("us-east-1");
  });

  it("refuses a section given twice", () => {
    expect(parseAwsIni("[profile a]\nx=1\n[profile a]\ny=2\n")).toEqual({ ok: false, error: { code: "duplicate_section", line: 3 } });
  });

  it("refuses a key given twice after lower-casing", () => {
    expect(parseAwsIni("[profile a]\nregion=1\nREGION=2\n")).toEqual({
      ok: false,
      error: { code: "duplicate_key", line: 3, key: "region" },
    });
  });

  it("lets [DEFAULT] reopen, but not repeat a key", () => {
    const f = ok("[DEFAULT]\na=1\n[profile x]\nb=2\n[DEFAULT]\nc=3\n");
    expect([...f.defaults.keys()]).toEqual(["a", "c"]);
    expect(parseAwsIni("[DEFAULT]\na=1\n[DEFAULT]\na=3\n")).toMatchObject({ ok: false, error: { code: "duplicate_key", line: 4 } });
  });

  it("keeps # in the middle of a value and reads : as a delimiter", () => {
    expect(opt("[profile a]\nregion = us-east-1 # c\n", 0, "region")).toBe("us-east-1 # c");
    expect(opt("[profile a]\nregion: us-east-1\n", 0, "region")).toBe("us-east-1");
  });

  it("refuses a key before any section and a line without a delimiter", () => {
    expect(parseAwsIni("region=1\n[profile a]\n")).toMatchObject({ ok: false, error: { code: "no_section_header", line: 1 } });
    expect(parseAwsIni("[profile a]\nregion\n")).toMatchObject({ ok: false, error: { code: "bad_line", line: 2 } });
  });

  it("keeps blank lines inside a nested value and drops comment lines", () => {
    expect(opt("[profile a]\ns3 =\n  x = 1\n\n  y = 2\nregion=r\n", 0, "s3")).toBe("\nx = 1\n\ny = 2");
    expect(opt("[profile a]\nk = v\n  # c\n  w\n", 0, "k")).toBe("v\nw");
  });

  it("reads an indented header as a continuation, as configparser does", () => {
    const f = ok("[profile a]\nk=v\n  [profile b]\nz=1\n");
    expect(f.sections).toHaveLength(1);
    expect(opt("[profile a]\nk=v\n  [profile b]\nz=1\n", 0, "k")).toBe("v\n[profile b]");
  });

  it("reads CRLF and an empty value", () => {
    expect(opt("[profile a]\r\nk = v\r\n", 0, "k")).toBe("v");
    expect(opt("[profile a]\nregion =\n", 0, "region")).toBe("");
  });

  it("strips a BOM and says so (the CLI cannot read such a file)", () => {
    const f = ok("﻿[profile a]\nx=1\n");
    expect(f.bomStripped).toBe(true);
    expect(f.sections[0].header).toBe("profile a");
  });

  it("refuses a nested block with a line botocore cannot split", () => {
    expect(parseAwsIni("[profile a]\ns3 =\n  malformed-line\n")).toEqual({ ok: false, error: { code: "bad_nested", line: 2 } });
    expect(parseAwsIni("[profile a]\ns3 =\n  max_concurrent_requests = 20\n").ok).toBe(true);
    // botocore checks each section's effective values: a bad [DEFAULT] block a section overrides
    // is never read, one it inherits is.
    expect(parseAwsIni("[DEFAULT]\ns3 =\n  malformed-line\n[profile a]\ns3 = ignored\n").ok).toBe(true);
    expect(parseAwsIni("[DEFAULT]\ns3 =\n  malformed-line\n[profile a]\nregion = x\n")).toEqual({
      ok: false,
      error: { code: "bad_nested", line: 2 },
    });
  });

  it("never puts a value in an error", () => {
    const r = parseAwsIni("[profile a]\nsecret-value-123\n");
    expect(JSON.stringify(r)).not.toContain("secret-value-123");
  });
});

describe("sectionRole", () => {
  it("maps headers the way botocore does", () => {
    expect(sectionRole("default")).toEqual({ kind: "profile", name: "default" });
    expect(sectionRole("profile prod")).toEqual({ kind: "profile", name: "prod" });
    expect(sectionRole('profile "prod app"')).toEqual({ kind: "profile", name: "prod app" });
    expect(sectionRole("profile  prod ")).toEqual({ kind: "profile", name: "prod" });
    expect(sectionRole("profile a b")).toEqual({ kind: "other" });
    expect(sectionRole('profile "open')).toEqual({ kind: "other" });
    expect(sectionRole("sso-session corp")).toEqual({ kind: "sso-session", name: "corp" });
    expect(sectionRole("services dev")).toEqual({ kind: "other" });
    expect(sectionRole("prod")).toEqual({ kind: "other" });
    // Python's shlex: a trailing backslash is an error, and only ASCII whitespace separates.
    expect(sectionRole("profile a\\")).toEqual({ kind: "other" });
    expect(sectionRole("profile prod\u00a0app")).toEqual({ kind: "profile", name: "prod\u00a0app" });
  });
});
