import { describe, expect, it } from "vitest";
import type { SsmHost } from "../../types/session.ts";
import { hostFor, machineLabel, ssmRowDetail } from "./ssmLabel.ts";

const host = (id: string, alias: string, instanceId: string, region = ""): SsmHost => ({ id, alias, profileId: "p", region, instanceId, documentName: "" });

describe("machineLabel", () => {
  const hosts = [host("h1", "web-1", "i-aaa", "ap-northeast-1"), host("h2", "web-1-us", "i-aaa", "us-east-1"), host("h3", "db", "i-bbb")];

  it("is the alias of the registered host, the region deciding between two with one id", () => {
    expect(machineLabel("i-aaa", "us-east-1", hosts)).toBe("web-1-us");
    expect(machineLabel("i-aaa", "ap-northeast-1", hosts)).toBe("web-1");
  });

  it("matches a host with no region of its own in any region, and a session with none against any host", () => {
    expect(machineLabel("i-bbb", "eu-west-1", hosts)).toBe("db");
    expect(hostFor("i-aaa", "", hosts)?.alias).toBe("web-1");
  });

  it("falls back to the instance id when no host points at it, or the region differs", () => {
    expect(machineLabel("i-zzz", "ap-northeast-1", hosts)).toBe("i-zzz");
    expect(machineLabel("i-aaa", "eu-west-1", hosts)).toBe("i-aaa");
  });
});

describe("ssmRowDetail", () => {
  it("names the profile and the run-as document, whichever it has", () => {
    expect(ssmRowDetail({ ssm: { profile: "ops", document: "AF-Shell" } })).toBe("ops · AF-Shell");
    expect(ssmRowDetail({ ssm: { profile: "ops" } })).toBe("ops");
    expect(ssmRowDetail({})).toBe("");
  });
});
