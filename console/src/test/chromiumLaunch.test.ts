// The shots harness must not attach to another session's browser or share its profile, and must
// not add --no-sandbox unasked. The helper is checked here with a fake "chromium" script that
// records its argv and writes DevToolsActivePort the way the real one does.
import { describe, it, expect, afterEach } from "vitest";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launchChromium, sandboxArgs, readDevToolsPort } from "../../scripts/lib/chromium-launch.mjs";

const made: string[] = [];
const closers: (() => void)[] = [];
afterEach(() => {
  closers.splice(0).forEach((c) => c());
  made.splice(0).forEach((d) => fs.rmSync(d, { recursive: true, force: true }));
});

function fakeChromium(body: string) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "fake-chromium-"));
  made.push(root);
  const bin = path.join(root, "chromium");
  fs.writeFileSync(
    bin,
    `#!/bin/sh\nfor a in "$@"; do case "$a" in --user-data-dir=*) d="\${a#--user-data-dir=}";; esac; done\n` +
      `printf '%s\\n' "$@" > "$d/argv"\n${body}\nsleep 30\n`,
    { mode: 0o755 },
  );
  return { root, bin };
}

describe("sandboxArgs", () => {
  it("is empty by default and opts in only explicitly", () => {
    expect(sandboxArgs({ env: {}, argv: [] })).toEqual([]);
    expect(sandboxArgs({ env: { SHOTS_NO_SANDBOX: "1" }, argv: [] })).toEqual(["--no-sandbox"]);
    expect(sandboxArgs({ env: {}, argv: ["--no-sandbox"] })).toEqual(["--no-sandbox"]);
  });
});

describe("readDevToolsPort", () => {
  it("reads line 1, and 0 while the file is absent", () => {
    const d = fs.mkdtempSync(path.join(os.tmpdir(), "dtap-"));
    made.push(d);
    expect(readDevToolsPort(d)).toBe(0);
    fs.writeFileSync(path.join(d, "DevToolsActivePort"), "41234\n/devtools/browser/x\n");
    expect(readDevToolsPort(d)).toBe(41234);
  });
});

describe("launchChromium", () => {
  it("uses port 0, a private profile, no --no-sandbox, and the port Chromium wrote", async () => {
    const { bin } = fakeChromium(`printf '43210\\n/devtools/browser/g\\n' > "$d/DevToolsActivePort"`);
    const b = await launchChromium({ bin, args: ["--lang=en-US"], env: {}, argv: [], pauseMs: 20 });
    closers.push(b.close);
    expect(b.port).toBe(43210);
    const argv = fs.readFileSync(path.join(b.dir, "argv"), "utf8").split("\n");
    expect(argv).toContain("--remote-debugging-port=0");
    expect(argv).toContain(`--user-data-dir=${b.dir}`);
    expect(argv).toContain("--lang=en-US");
    expect(argv).not.toContain("--no-sandbox");
    expect(argv.some((a) => a.startsWith("--remote-debugging-port=") && a !== "--remote-debugging-port=0")).toBe(false);
  });

  it("gives two runs different profiles under AF_WORK_DIR and close() removes the profile and the process", async () => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "work-"));
    made.push(work);
    const { bin } = fakeChromium(`printf '1111\\n' > "$d/DevToolsActivePort"`);
    const opts = { bin, env: { AF_WORK_DIR: work }, argv: [], pauseMs: 20 };
    const a = await launchChromium(opts);
    const b = await launchChromium(opts);
    expect(a.dir).not.toBe(b.dir);
    expect(path.dirname(a.dir)).toBe(work);
    a.close();
    b.close();
    b.close(); // idempotent
    expect(fs.existsSync(a.dir)).toBe(false);
    expect(fs.existsSync(b.dir)).toBe(false);
    await new Promise((r) => setTimeout(r, 100));
    expect(a.proc.exitCode !== null || a.proc.signalCode !== null).toBe(true);
  });

  it("cleans up the profile and the process when the port never appears", async () => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "work-"));
    made.push(work);
    const { bin } = fakeChromium("");
    await expect(
      launchChromium({ bin, env: { AF_WORK_DIR: work }, argv: [], tries: 3, pauseMs: 20 }),
    ).rejects.toThrow(/did not open a debugging port/);
    expect(fs.readdirSync(work)).toEqual([]);
  });
});
