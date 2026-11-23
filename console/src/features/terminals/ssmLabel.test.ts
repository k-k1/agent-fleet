import { describe, expect, it } from "vitest";
import type { SsmHost } from "../../types/session.ts";
import { hostFor, machineLabel, ssmRowDetail } from "./ssmLabel.ts";
import type { SsmProfileRef } from "./ssmLabel.ts";

const host = (id: string, alias: string, instanceId: string, profileId: string, region = ""): SsmHost => ({ id, alias, profileId, region, instanceId, documentName: "" });
const profiles: SsmProfileRef[] = [
  { id: "pa", accountId: "111122223333", region: "ap-northeast-1" },
  { id: "pb", accountId: "999900001111", region: "us-east-1" },
  { id: "pnone", accountId: "111122223333" },
];

describe("machineLabel", () => {
  it("is the alias of the registered host, the region deciding between two with one id", () => {
    const hosts = [host("h1", "web-1", "i-aaa", "pa", "ap-northeast-1"), host("h2", "web-1-us", "i-aaa", "pa", "us-east-1")];
    expect(machineLabel("i-aaa", "us-east-1", "111122223333", hosts, profiles)).toBe("web-1-us");
    expect(machineLabel("i-aaa", "ap-northeast-1", "111122223333", hosts, profiles)).toBe("web-1");
  });

  it("gives a host with no region of its own its profile's region, not every region", () => {
    const hosts = [host("h1", "web-1", "i-aaa", "pa")]; // profile pa: ap-northeast-1
    expect(machineLabel("i-aaa", "ap-northeast-1", "111122223333", hosts, profiles)).toBe("web-1");
    // The same id in another region is another machine: no alias for it.
    expect(machineLabel("i-aaa", "eu-west-1", "111122223333", hosts, profiles)).toBe("i-aaa");
  });

  it("keeps two accounts that reuse an instance id apart", () => {
    const hosts = [host("h1", "prod-web", "i-aaa", "pa", "ap-northeast-1"), host("h2", "stg-web", "i-aaa", "pb", "ap-northeast-1")];
    expect(machineLabel("i-aaa", "ap-northeast-1", "111122223333", hosts, profiles)).toBe("prod-web");
    expect(machineLabel("i-aaa", "ap-northeast-1", "999900001111", hosts, profiles)).toBe("stg-web");
    expect(machineLabel("i-aaa", "ap-northeast-1", "555566667777", hosts, profiles)).toBe("i-aaa");
  });

  it("does not rule a host out for a side that is unknown", () => {
    // A profile with no region and a session row with no account or region.
    const hosts = [host("h1", "db", "i-bbb", "pnone")];
    expect(machineLabel("i-bbb", "eu-west-1", "111122223333", hosts, profiles)).toBe("db");
    expect(hostFor("i-bbb", "", "", hosts, profiles)?.alias).toBe("db");
  });

  it("falls back to the instance id when no host points at it", () => {
    expect(machineLabel("i-zzz", "ap-northeast-1", "", [host("h1", "web-1", "i-aaa", "pa")], profiles)).toBe("i-zzz");
  });
});

describe("ssmRowDetail", () => {
  it("names the profile and the run-as document, whichever it has", () => {
    expect(ssmRowDetail({ ssm: { profile: "ops", document: "AF-Shell" } })).toBe("ops · AF-Shell");
    expect(ssmRowDetail({ ssm: { profile: "ops" } })).toBe("ops");
    expect(ssmRowDetail({})).toBe("");
  });
});
