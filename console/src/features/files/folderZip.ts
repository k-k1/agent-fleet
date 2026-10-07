// "Download as zip" for a folder, shared by the Files tree and the image gallery (ADR 0111).
//
// A plain `<a href download>` cannot show a refusal: a 413, 503, 401 or a stopped workspace
// becomes a failed (or JSON-named) download with no word in the Console, and an expired session
// may redirect the whole tab to the login page. So the press first asks the Agent what the zip
// would hold (`check=1`, the walk without the build) over fetch — which has the auth-expiry
// handling — and only then starts the real download as a hidden same-origin anchor. A refusal
// at that second step is a race the check cannot rule out (the folder grew in between); the
// browser then reports a failed download, which is still a failure, never a short archive.
//
// The whole zip is never read into a Blob here: the browser downloads it straight to disk.
import { downloadZipURL, errDetail, fsZipCheck, getTenant } from "../../core/api/client.ts";
import type { FsZipCheck } from "../../core/api/client.ts";
import { humanSize } from "../../lib/filemeta.ts";
import { t } from "../../lib/i18n/index.ts";

type Notify = (message: string, opts?: { kind?: "error" | "warn" | "info" | "success" }) => void;

// A second press on the same folder while its check is still out would start two downloads.
const pending = new Set<string>();

/** The sentence under a started download: what is in it and what was left out, so nobody
 *  discovers the exclusions by missing a folder after unzipping. */
export function zipStartedText(c: FsZipCheck): string {
  let msg = t("zip.started", { name: c.name, files: c.files, size: humanSize(c.bytes) });
  if (c.excluded.length) msg += " " + t("zip.excluded", { names: c.excluded.join(", ") });
  if (c.skipped) msg += " " + t("zip.skipped", { n: c.skipped });
  return msg;
}

function startDownload(url: string, name: string) {
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.rel = "noopener";
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export async function downloadFolderZip(path: string, notify: Notify): Promise<void> {
  if (pending.has(path)) return;
  pending.add(path);
  // Both the check and the URL are taken for the tenant that is selected NOW: a switch while
  // the check is out must not download that folder's name from another tenant's workspace.
  const tenant = getTenant();
  const url = downloadZipURL(path);
  try {
    const r = await fsZipCheck(path);
    if (getTenant() !== tenant) return;
    if (r.error) {
      notify(t("zip.failed", { msg: errDetail(r.error) }));
      return;
    }
    startDownload(url, r.name);
    notify(zipStartedText(r), { kind: "success" });
  } catch {
    // A dropped connection rejects instead of resolving {error}.
    notify(t("zip.failed", { msg: t("zip.unreachable") }));
  } finally {
    pending.delete(path);
  }
}
