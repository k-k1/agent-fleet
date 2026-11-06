import { describe, it, expect } from "vitest";
import { formatTcpEntry, isTcpEntry, parseTcpEntry, parseTcpParts, tcpEntryParts } from "./egressTcp.ts";

// The same cases as control-plane/internal/egressallow/tcp_test.go: the form and the CP
// must agree on what is valid.
describe("parseTcpEntry", () => {
  it("accepts what the CP accepts, in the CP's canonical spelling", () => {
    const good: Record<string, string> = {
      "tcp:git.example.com:22": "tcp:git.example.com:22",
      " TCP:Git.Example.com.:22 ": "tcp:git.example.com:22",
      "tcp:db.example.com:11000-11999": "tcp:db.example.com:11000-11999",
      "tcp:db.example.com:1433-1433": "tcp:db.example.com:1433",
      "tcp:203.0.113.7:22": "tcp:203.0.113.7:22",
      // just outside each refused range
      "tcp:1.0.0.1:22": "tcp:1.0.0.1:22",
      "tcp:100.63.255.255:22": "tcp:100.63.255.255:22",
      "tcp:100.128.0.1:22": "tcp:100.128.0.1:22",
      "tcp:172.15.255.255:22": "tcp:172.15.255.255:22",
      "tcp:172.32.0.1:22": "tcp:172.32.0.1:22",
      "tcp:192.0.1.1:22": "tcp:192.0.1.1:22",
      "tcp:192.169.0.1:22": "tcp:192.169.0.1:22",
      "tcp:198.17.255.255:22": "tcp:198.17.255.255:22",
      "tcp:198.20.0.1:22": "tcp:198.20.0.1:22",
      "tcp:223.255.255.254:22": "tcp:223.255.255.254:22",
      "tcp:db.example.com:20000-22047": "tcp:db.example.com:20000-22047", // exactly 2048 ports
      "tcp:db.example.com:65535": "tcp:db.example.com:65535",
    };
    for (const [raw, want] of Object.entries(good)) {
      const r = parseTcpEntry(raw);
      expect(r.ok && r.entry, raw).toBe(want);
    }
  });

  it("refuses what the CP refuses, naming the reason", () => {
    const bad: Record<string, string> = {
      "git.example.com:22": "format",
      "tcp:git.example.com": "format",
      "tcp:git.example.com:": "format",
      "tcp::22": "format",
      "tcp:.example.com:22": "host",
      "tcp:*.example.com:22": "host",
      "tcp:example:22": "host",
      "tcp:a..example.com:22": "host",
      "tcp:git.example.com:0": "port",
      "tcp:git.example.com:65536": "port",
      "tcp:git.example.com:22-21": "port",
      "tcp:git.example.com:022": "port",
      "tcp:git.example.com:22-": "port",
      "tcp:git.example.com:x": "port",
      "tcp:git.example.com:1-2-3": "port",
      "tcp:db.example.com:20000-22048": "width",
      "tcp:db.example.com:80": "refused",
      "tcp:db.example.com:443": "refused",
      "tcp:db.example.com:3128": "refused",
      "tcp:db.example.com:53": "refused",
      "tcp:db.example.com:853": "refused",
      "tcp:db.example.com:400-500": "refused", // contains 443
      "tcp:127.0.0.1:22": "ipv4",
      "tcp:169.254.169.254:22": "ipv4",
      "tcp:0.0.0.0:22": "ipv4",
      "tcp:10.0.0.5:2222": "ipv4",
      "tcp:172.16.0.1:22": "ipv4",
      "tcp:172.31.255.255:22": "ipv4",
      "tcp:192.168.1.1:22": "ipv4",
      "tcp:255.255.255.255:22": "ipv4",
      "tcp:0.1.2.3:22": "ipv4",
      "tcp:0.255.255.255:22": "ipv4",
      "tcp:100.64.0.1:22": "ipv4",
      "tcp:100.127.255.254:22": "ipv4",
      "tcp:192.0.0.7:22": "ipv4",
      "tcp:198.18.0.7:22": "ipv4",
      "tcp:198.19.255.255:22": "ipv4",
      "tcp:240.0.0.1:22": "ipv4",
      "tcp:10.255.255.255:22": "ipv4",
      "tcp:::ffff:8.8.8.8:22": "ipv4",
      "tcp:::ffff:10.0.0.5:22": "ipv4",
      "tcp:224.0.0.1:22": "ipv4",
      "tcp:2001:db8::5:22": "ipv4",
    };
    for (const [raw, problem] of Object.entries(bad)) {
      const r = parseTcpEntry(raw);
      expect(r.ok ? "ok" : r.problem, raw).toBe(problem);
    }
  });

  it("reports which refused port a range contains", () => {
    const r = parseTcpParts("db.example.com", "400-500");
    expect(!r.ok && r.problem === "refused" && r.port).toBe(443);
  });
});

describe("helpers", () => {
  it("recognises and formats entries", () => {
    expect(isTcpEntry(" TCP:x")).toBe(true);
    expect(isTcpEntry("tcp.example.com")).toBe(false);
    expect(formatTcpEntry("a.example.com", 22, 22)).toBe("tcp:a.example.com:22");
    expect(formatTcpEntry("a.example.com", 1, 9)).toBe("tcp:a.example.com:1-9");
  });
  it("splits a stored entry for display, and leaves a host entry alone", () => {
    expect(tcpEntryParts("tcp:db.example.com:11000-11999")).toEqual({ host: "db.example.com", ports: "11000-11999" });
    expect(tcpEntryParts("github.com")).toBeNull();
    expect(tcpEntryParts("tcp:garbage")).toBeNull();
  });
});
