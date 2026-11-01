// "Export as Markdown" for a session's conversation (#2183). The Agent renders the whole
// transcript, not the mirror's window, and masks secret-looking strings.
//
// fetch -> Blob rather than a plain link: a stopped workspace or an unsupported kind answers
// with a JSON error, which a link navigation would save as the "Markdown" file.
import { errDetail, raw } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { effectiveTimeZone } from "../../lib/intl.ts";

type Notify = (message: string, opts?: { kind?: "error" | "warn" | "info" | "success" }) => void;

export async function exportSessionMarkdown(name: string, tools: "none" | "names" | "full", notify: Notify): Promise<void> {
  let res: Response;
  try {
    // Always the resolved zone, chosen or the browser's. An Agent older than the time zone
    // setting falls back to its container's clock without ?tz=, and a setting just changed may
    // not have reached the Agent's ui-prefs yet; the Agent still reads the setting when it is absent.
    const tz = effectiveTimeZone();
    res = await raw(
      "api/sessions/" + encodeURIComponent(name) + "/export?tools=" + tools + "&tz=" + encodeURIComponent(tz),
    );
  } catch {
    notify(t("srow.export_failed"));
    return;
  }
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    // A non-JSON answer (an Agent older than this route says 404 in plain text) still names
    // its status, so "failed" is never the whole story.
    notify(t("srow.export_failed") + ": " + (body?.error ? errDetail(body.error) : "HTTP " + res.status));
    return;
  }
  // The body can still fail after a 200 (a connection dropped mid-transfer); that must reach
  // the member as a failure, not vanish as an unhandled rejection.
  let blob: Blob;
  try {
    blob = await res.blob();
  } catch {
    notify(t("srow.export_failed"));
    return;
  }
  const cd = res.headers.get("Content-Disposition") ?? "";
  // The Agent masks a token-shaped session name; never fall back to the raw name here.
  const file = /filename="?([^";]+)"?/.exec(cd)?.[1] ?? "session.md";
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = file;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
  notify(t("srow.export_done"), { kind: "success" });
}
