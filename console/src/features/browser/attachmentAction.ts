// One place that turns "the user followed a Chromium attachment link" into a
// pane (docs/53 §53.7). Two entry points share it: the action ROUTE (a real
// navigation to /open/browser-attachment/<id>, e.g. a new tab or a reload) and a
// CLICK on the Markdown link the agent posts into the mirror, which never
// navigates at all. Both must verify membership/expiry, ask before replacing a
// pane, and commit exactly once — so neither may re-implement the wiring.
import { errText } from "../../core/api/client.ts";
import { mobileMatches } from "../../lib/device.ts";
import { t } from "../../lib/i18n/index.ts";
import {
  canonicalWorkspaceURL,
  runBrowserAttachmentAction,
} from "../../layout/browserAttachmentAction.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { askConfirm } from "../../ui/confirmBridge.ts";
import { toast } from "../../ui/toast.ts";
import { getBrowserAttachment } from "./attachmentService.ts";

export interface OpenBrowserAttachmentOptions {
  /** True only when the current URL *is* the action route: on success it is
   * replaced with the canonical Workspace URL so a reload cannot re-run it. A
   * click in the mirror has no such URL to clean up. */
  fromActionURL?: boolean;
}

/** Returns false when the user cancelled or the attachment could not be opened
 * (the failure is already surfaced as a toast). */
export async function openBrowserAttachment(
  attachmentId: string,
  { fromActionURL = false }: OpenBrowserAttachmentOptions = {},
): Promise<boolean> {
  try {
    return await runBrowserAttachmentAction({
      attachmentId,
      mobile: mobileMatches(),
      getStatus: getBrowserAttachment,
      getLayout: () => useLayoutStore.getState().layout,
      commit: (layout) => useLayoutStore.getState().commitAction(layout),
      confirmReplace: () =>
        askConfirm({
          title: t("browser.attach.cap_title"),
          body: t("browser.attach.cap_body"),
          confirmLabel: t("browser.attach.replace_current"),
          danger: false,
        }),
      replaceURL: () => {
        if (fromActionURL) history.replaceState(history.state, "", canonicalWorkspaceURL());
      },
    });
  } catch (error) {
    const e = error as { code?: string; message?: string };
    const message = e.code === "browser_attachment_not_found"
      ? t("browser.attach.expired")
      : errText(e) || t("browser.attach.open_failed");
    toast(message, { kind: "error" });
    return false;
  }
}
