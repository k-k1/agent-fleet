// downloadFolderZip (ADR 0111): a refusal must be a message in the Console and never a download,
// the exclusions must be said before the file is on disk, and a repeated press must not start a
// second download.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let tenant = "";
const check = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => tenant,
  downloadZipURL: (p: string) => `http://x/api/fs/download-zip?path=${encodeURIComponent(p)}${tenant ? "&tenant=" + tenant : ""}`,
  fsZipCheck: (p: string) => check(p),
  errDetail: (e: { code?: string; message?: string }) => `[${e.code}] ${e.message}`,
}));

const { downloadFolderZip, zipStartedText } = await import("./folderZip.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");

const ok = { name: "app.zip", files: 12, dirs: 3, bytes: 2048, excluded: [] as string[], skipped: 0 };
let clicked: { href: string; download: string }[] = [];
let toasts: { msg: string; kind?: string }[] = [];
const notify = (msg: string, opts?: { kind?: string }) => toasts.push({ msg, kind: opts?.kind });

beforeEach(() => {
  setLocale("en");
  tenant = "";
  clicked = [];
  toasts = [];
  check.mockReset();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.href, download: this.download });
  });
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("downloadFolderZip", () => {
  it("starts the download of the checked folder and says what it holds", async () => {
    check.mockResolvedValue({ ...ok, excluded: [".git", "node_modules"], skipped: 2 });
    await downloadFolderZip("repos/app", notify);
    expect(check).toHaveBeenCalledWith("repos/app");
    expect(clicked).toEqual([{ href: "http://x/api/fs/download-zip?path=repos%2Fapp", download: "app.zip" }]);
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("success");
    expect(toasts[0].msg).toContain("app.zip");
    expect(toasts[0].msg).toContain("12 files");
    expect(toasts[0].msg).toContain(".git, node_modules");
    expect(toasts[0].msg).toContain("2 links");
    expect(document.querySelector("a")).toBeNull(); // the helper anchor does not linger
  });

  it("says nothing about exclusions when nothing was left out", () => {
    expect(zipStartedText(ok)).not.toContain("Left out");
    expect(zipStartedText(ok)).not.toContain("links");
  });

  it.each([
    [413, "zip_too_large", "more than 20000 files"],
    [503, "zip_busy", "another folder is being zipped"],
    [404, "zip_not_dir", "not a folder"],
    [401, "unauthenticated", "no gateway identity"],
    [502, "http_502", "workspace agent unreachable"],
  ])("a %i refusal is shown as an error and downloads nothing", async (status, code, message) => {
    check.mockResolvedValue({ error: { code, message, status } });
    await downloadFolderZip("repos/big", notify);
    expect(clicked).toEqual([]);
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBeUndefined(); // the default kind is the error one
    expect(toasts[0].msg).toContain(`[${code}] ${message}`);
  });

  it("a dropped connection is a message too, not an unhandled rejection", async () => {
    check.mockRejectedValue(new TypeError("Failed to fetch"));
    await downloadFolderZip("repos/a", notify);
    expect(clicked).toEqual([]);
    expect(toasts).toHaveLength(1);
    expect(toasts[0].msg).toContain("didn't answer");
  });

  it("ignores a second press on the same folder while its check is out, then allows another", async () => {
    let release!: (v: unknown) => void;
    check.mockReturnValueOnce(new Promise((r) => (release = r)));
    const first = downloadFolderZip("repos/a", notify);
    await downloadFolderZip("repos/a", notify);
    expect(check).toHaveBeenCalledTimes(1);
    release(ok);
    await first;
    expect(clicked).toHaveLength(1);
    check.mockResolvedValue(ok);
    await downloadFolderZip("repos/a", notify);
    expect(clicked).toHaveLength(2);
  });

  it("a different folder is not blocked by one in flight", async () => {
    let release!: (v: unknown) => void;
    check.mockReturnValueOnce(new Promise((r) => (release = r)));
    const first = downloadFolderZip("repos/a", notify);
    check.mockResolvedValueOnce(ok);
    await downloadFolderZip("repos/b", notify);
    expect(clicked).toHaveLength(1);
    release(ok);
    await first;
    expect(clicked).toHaveLength(2);
  });

  it("drops the result when the tenant changed while the check was out", async () => {
    let release!: (v: unknown) => void;
    check.mockReturnValueOnce(new Promise((r) => (release = r)));
    const p = downloadFolderZip("repos/a", notify);
    tenant = "other";
    release(ok);
    await p;
    expect(clicked).toEqual([]);
    expect(toasts).toEqual([]);
  });

  it("builds the download URL for the tenant the press happened under", async () => {
    tenant = "acme";
    check.mockResolvedValue(ok);
    await downloadFolderZip("repos/a", notify);
    expect(clicked[0].href).toContain("tenant=acme");
  });
});
