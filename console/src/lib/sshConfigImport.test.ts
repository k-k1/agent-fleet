import { describe, expect, it } from "vitest";
import { parseSshConfig } from "./sshConfigIni.ts";
import { readSshConfig, resolveSshImport, toSshImportSection, type SshEdit, type SshReason } from "./sshConfigImport.ts";
import { planSsmImport } from "./settingsBundle.ts";

const I1 = "i-0123456789abcdef0";
const I2 = "i-0fedcba9876543210";
const CMD = "aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p";
const PROFILES = [{ id: "p1", label: "prod", kind: "sso" }];

function read(text: string) {
  const r = parseSshConfig(text);
  if (!r.ok) throw new Error("parse failed " + r.error.code);
  return readSshConfig(r.file);
}
function preview(text: string, profiles: any[] = PROFILES, hosts: any[] = [], edits: Record<string, SshEdit> = {}) {
  return resolveSshImport(read(text), profiles, hosts, edits);
}
const row = (p: ReturnType<typeof preview>, name: string) => p.rows.find((r) => r.name === name)!;
const code = (p: ReturnType<typeof preview>, name: string) => row(p, name).reason?.code;
const host = (name: string, pc: string, extra = "") => `Host ${name}\n  HostName ${I1}\n  ProxyCommand ${pc}\n${extra}`;

describe("parseSshConfig", () => {
  it("reads Keyword=value, comments, tabs, CRLF and quoted Host words", () => {
    const r = parseSshConfig('# c\r\nHost="a b" c # trailing\r\n\tHostName=x\r\n\tUser  me\r\n');
    expect(r.ok && r.file.blocks[1].patterns).toEqual(["a b", "c"]);
    expect(r.ok && r.file.blocks[1].lines.map((l) => [l.key, l.args])).toEqual([
      ["hostname", "x"],
      ["user", "me"],
    ]);
  });
  it("puts lines before the first Host in a global block", () => {
    const r = parseSshConfig("User me\nHost a\n  Port 22\n");
    expect(r.ok && r.file.blocks[0]).toMatchObject({ kind: "global", lines: [{ key: "user" }] });
  });
  it("refuses a Host without arguments and an open quote, by line", () => {
    expect(parseSshConfig("Host\n")).toEqual({ ok: false, error: { code: "missing_args", line: 1 } });
    expect(parseSshConfig('\nHost "a\n')).toEqual({ ok: false, error: { code: "bad_quote", line: 2 } });
    expect(parseSshConfig("=x\n")).toEqual({ ok: false, error: { code: "bad_line", line: 1 } });
  });
  it("strips a BOM and reports it", () => {
    const r = parseSshConfig("﻿Host a\n");
    expect(r.ok && r.file.bomStripped).toBe(true);
  });
});

describe("the static form", () => {
  it("creates a host from HostName + %h", () => {
    const p = preview(host("web", CMD, "  User ec2-user\n  IdentityFile ~/.ssh/k\n  Port 2222\n") + "");
    expect(row(p, "web")).toMatchObject({ status: "blocked", instanceId: I1, ignored: ["user", "identityfile", "port"] });
    expect(code(p, "web")).toBe("profile_unset");
  });
  it("maps --profile to the Settings profile by AWS name and --region to the host", () => {
    const p = preview(host("web", CMD + " --profile prod --region ap-northeast-1"));
    expect(row(p, "web")).toMatchObject({ status: "create", via: "named", profileLabel: "prod", entry: { alias: "web", profile: "prod", instanceId: I1, documentName: "", region: "ap-northeast-1" } });
  });
  it("accepts a literal --target, --opt=value, portNumber=22 and the sh -c wrapper", () => {
    for (const pc of [
      `aws ssm start-session --target ${I1} --document-name=AWS-StartSSHSession --profile=prod`,
      `aws ssm start-session --target ${I1} --document-name AWS-StartSSHSession --parameters portNumber=22 --profile prod`,
      `sh -c "aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters 'portNumber=%p' --profile prod"`,
      `sh -c 'aws ssm start-session --target %h --document-name AWS-StartSSHSession --profile "prod"'`,
    ]) {
      expect(row(preview(`Host a\n  HostName ${I2}\n  ProxyCommand ${pc}\n`), "a").status, pc).toBe("create");
    }
  });
  it("uses the Host name itself for %h when there is no HostName", () => {
    const p = preview(`Host ${I2}\n  ProxyCommand ${CMD} --profile prod\n`);
    expect(row(p, I2)).toMatchObject({ status: "create", instanceId: I2 });
  });
  it("converts every created row and says so through `converted`", () => {
    expect(preview(host("a", CMD + " --profile prod")).converted).toBe(1);
  });
});

describe("refusals", () => {
  const refused = (pc: string, c: SshReason["code"]) => expect(code(preview(host("h", pc + " --profile prod")), "h"), pc).toBe(c);
  it("other documents", () => {
    refused(`aws ssm start-session --target %h --document-name AWS-StartPortForwardingSession --parameters portNumber=22,localPortNumber=1`, "port_forward_doc");
    refused(`aws ssm start-session --target %h --document-name AWS-StartPortForwardingSessionToRemoteHost`, "port_forward_doc");
    refused(`aws ssm start-session --target %h --document-name MyDoc`, "doc_other");
    refused(`aws ssm start-session --target %h`, "no_document");
  });
  it("other ports and parameters", () => {
    refused(`aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=3389`, "parameters");
  });
  it("unresolved or foreign tokens", () => {
    refused(`aws ssm start-session --target %r --document-name AWS-StartSSHSession`, "unresolved");
    refused(`aws ssm start-session --target web.example.com --document-name AWS-StartSSHSession`, "target_not_instance");
    refused(`aws ssm start-session --document-name AWS-StartSSHSession`, "no_target");
  });
  it("an HostName that is not an instance id leaves %h unresolved", () => {
    expect(code(preview(`Host h\n  HostName web.example.com\n  ProxyCommand ${CMD} --profile prod\n`), "h")).toBe("target_not_instance");
    expect(code(preview(`Host h\n  HostName %h\n  ProxyCommand ${CMD} --profile prod\n`), "h")).toBe("unresolved");
  });
  it("shell syntax, other programs, unknown options", () => {
    refused(`${CMD}; curl evil`, "not_static");
    refused(`${CMD} | tee x`, "not_static");
    refused(`${CMD} $(id)`, "not_static");
    refused(`${CMD} --token abc`, "unsupported_option");
    refused(`${CMD} --region us-east-1 --region eu-west-1`, "not_static");
    refused(`/usr/bin/aws ssm start-session --target %h`, "other_command");
    refused(`nc %h %p`, "other_command");
    refused(`AWS_PROFILE=x ${CMD}`, "other_command");
  });
  it("a command that names an unusable profile or region", () => {
    expect(code(preview(host("h", CMD + " --profile 'a b'")), "h")).toBe("not_static");
    expect(code(preview(host("h", CMD + " --region UPPER")), "h")).toBe("invalid");
  });
  it("ProxyJump, ProxyCommand none, no ProxyCommand", () => {
    expect(code(preview(host("h", CMD + " --profile prod", "  ProxyJump bastion\n")), "h")).toBe("proxy_jump");
    expect(code(preview(`Host h\n  ProxyCommand none\n`), "h")).toBe("not_ssm");
    expect(code(preview(`Host github.com\n  User git\n`), "github.com")).toBe("not_ssm");
  });
  it("wildcards, negations, Match and Include are rows of their own, never importable", () => {
    const p = preview(`Host *.corp !b\n  User x\nHost a !a\n  User y\nMatch host z\n  User q\nInclude conf.d/*\n`);
    expect(p.rows.map((r) => r.reason?.code)).toEqual(["wildcard", "negation", "barrier", "negation", "match", "include"]);
    expect(p.rows.every((r) => r.status === "blocked")).toBe(true);
  });
});

describe("first-match-wins", () => {
  it("refuses a ProxyCommand inherited from Host * above", () => {
    const p = preview(`Host *\n  ProxyCommand ${CMD} --profile prod\nHost a\n  HostName ${I1}\n`);
    expect(code(p, "a")).toBe("inherited");
  });
  it("refuses one inherited from a pattern block above, but not from one below", () => {
    expect(code(preview(`Host a*\n  ProxyCommand ${CMD} --profile prod\nHost a\n  HostName ${I1}\n`), "a")).toBe("inherited");
    expect(row(preview(`Host a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\nHost *\n  ProxyCommand nc %h %p\n`), "a").status).toBe("create");
  });
  it("the first ProxyCommand wins when a name has two blocks", () => {
    const t = `Host a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\nHost a\n  ProxyCommand nc %h %p\n`;
    expect(preview(t).rows.filter((r) => r.name === "a")).toHaveLength(1);
    expect(row(preview(t), "a").status).toBe("create");
    const u = `Host a\n  ProxyCommand nc %h %p\nHost a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n`;
    expect(code(preview(u), "a")).toBe("other_command");
  });
  it("a HostName from a wildcard block above overrides the block's own", () => {
    const p = preview(`Host *\n  HostName ${I2}\nHost a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n`);
    expect(code(p, "a")).toBe("inherited");
  });
  it("a negated pattern keeps a block off the name", () => {
    const p = preview(`Host * !a\n  ProxyCommand nc %h %p\nHost a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n`);
    expect(row(p, "a").status).toBe("create");
  });
  it("a Match above that sets a deciding keyword, or any Include above, is a barrier", () => {
    const own = `Host a\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n`;
    expect(code(preview(`Match host a\n  ProxyCommand nc %h %p\n` + own), "a")).toBe("barrier");
    expect(code(preview(`Include conf.d/*\n` + own), "a")).toBe("barrier");
    expect(code(preview(`Host b\n  Include more\n` + own), "a")).toBeUndefined();
    // A Match that sets nothing that decides, and anything below, does not matter.
    expect(row(preview(`Match host a\n  User x\n` + own), "a").status).toBe("create");
    expect(row(preview(own + `Match all\n  ProxyCommand nc %h %p\nInclude x\n`), "a").status).toBe("create");
  });
  it("a name with no ProxyCommand but a Match or Include somewhere above says why it cannot tell", () => {
    expect(code(preview(`Include x\nHost a\n  User u\n`), "a")).toBe("barrier");
  });
});

describe("resolveSshImport", () => {
  const ok = host("web", CMD + " --profile prod");
  it("is idempotent: the same alias and instance is 'exists'", () => {
    const p = preview(ok, PROFILES, [{ alias: "WEB", instanceId: I1 }]);
    expect(row(p, "web").status).toBe("exists");
    expect(toSshImportSection(p).hosts).toEqual([]);
  });
  it("refuses an alias already used for another instance", () => {
    expect(code(preview(ok, PROFILES, [{ alias: "web", instanceId: I2 }]), "web")).toBe("alias_taken");
  });
  it("refuses a second creation under the same alias", () => {
    const p = preview(ok + host("web2", CMD + " --profile prod"), PROFILES, [], { "host:web2": { alias: "WEB" } });
    expect(p.rows.map((r) => r.status)).toEqual(["create", "blocked"]);
    expect(p.rows[1].reason).toMatchObject({ code: "alias_dup" });
  });
  it("renames through the alias edit, and an invalid alias is refused", () => {
    const id = "host:web";
    expect(row(preview(ok, PROFILES, [], { [id]: { alias: "web-1" } }), "web").entry?.alias).toBe("web-1");
    expect(code(preview(ok, PROFILES, [], { [id]: { alias: "a b" } }), "web")).toBe("alias_invalid");
    expect(code(preview(ok, PROFILES, [], { [id]: { alias: " " } }), "web")).toBe("alias_empty");
  });
  it("needs a picked profile when the command names none, or none matches", () => {
    const t = host("web", CMD);
    expect(code(preview(t), "web")).toBe("profile_unset");
    const picked = preview(t, PROFILES, [], { "host:web": { profileId: "p1" } });
    expect(row(picked, "web")).toMatchObject({ status: "create", via: "picked", profileLabel: "prod" });
    expect(code(preview(host("web", CMD + " --profile other")), "web")).toBe("profile_missing");
    expect(code(preview(ok, [{ id: "a", label: "prod" }, { id: "b", label: "prod" }]), "web")).toBe("profile_ambiguous");
  });
  it("matches by the AWS name the label becomes, not by the label", () => {
    const p = preview(host("web", CMD + " --profile prod-app"), [{ id: "x", label: "prod app" }]);
    expect(row(p, "web")).toMatchObject({ status: "create", profileLabel: "prod app" });
  });
  it("refuses a profile whose label is shared, since the executor binds by label", () => {
    expect(code(preview(ok, [{ id: "a", label: "prod" }, { id: "b", label: "PROD" }]), "web")).toBe("profile_label_dup");
  });
  it("unticking leaves it out; the section feeds the planner", () => {
    expect(row(preview(ok, PROFILES, [], { "host:web": { selected: false } }), "web").status).toBe("unselected");
    const plan = planSsmImport(toSshImportSection(preview(ok)), PROFILES, []);
    expect(plan.hosts).toEqual([{ alias: "web", profile: "prod", instanceId: I1, documentName: "", region: "" }]);
  });
  it("a name that is not safe to show is shown by its line and needs an alias", () => {
    const p = preview(`Host "we:b"\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n`);
    expect(p.rows[0]).toMatchObject({ display: "", reason: { code: "alias_empty" } });
  });
});

describe("secrets", () => {
  it("keeps no part of any command, key or path in the entries", () => {
    const text =
      `Host a\n  HostName ${I1}\n  IdentityFile /home/me/KEYPATHSENTINEL\n  ProxyCommand ${CMD} --profile prod --token CMDSENTINEL\n` +
      `Host b\n  HostName ${I1}\n  ProxyCommand ${CMD} --profile prod\n  SetEnv AWS_SECRET_ACCESS_KEY=ENVSENTINEL\n`;
    const p = preview(text);
    const dump = JSON.stringify(p);
    for (const s of ["CMDSENTINEL", "KEYPATHSENTINEL", "ENVSENTINEL", "aws ssm", "AWS-StartSSHSession"]) expect(dump).not.toContain(s);
    expect(read(text).secrets).toBe(true);
  });
});
