import { apiJSON, errText, pasteImage } from "../../core/api/client.ts";
import { AUTH_EXPIRED_CODE, toastSendFailure } from "../../lib/authExpiredToast.tsx";
import { t } from "../../lib/i18n/index.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import type { useToast } from "../../ui/ToastProvider.tsx";

export interface LaunchFile {
  file: File;
  path: string;
}

type Toast = ReturnType<typeof useToast>;

export async function uploadLaunchFiles(session: string, files: LaunchFile[], toast: Toast): Promise<LaunchFile[]> {
  const uploaded: LaunchFile[] = [];
  for (const entry of files) {
    if (!entry.path) {
      const { file } = entry;
      try {
        const up = await pasteImage(session, file);
        if (up.status < 300 && up.path) {
          uploaded.push({ file, path: up.path });
          continue;
        }
        toast(t("rp.image_upload_failed", { name: file.name, err: up.error ? errText(up.error) : "" }));
      } catch {
        toast(t("rp.image_upload_failed_network", { name: file.name }));
      }
    }
    uploaded.push(entry);
  }
  return uploaded;
}

export async function deliverLaunchPrompt(session: string, kind: string, prompt: string, files: LaunchFile[], toast: Toast) {
  const seed = buildImagePrompt(prompt, files.map((entry) => entry.path), kind);
  const del = await apiJSON(`api/sessions/${encodeURIComponent(session)}/input`, "POST", {
    prompt: seed,
    when_ready: true,
  }).catch(() => ({ error: { message: t("err.network") } }));
  if (del?.error) {
    if (del.error.code === AUTH_EXPIRED_CODE) toastSendFailure(toast, { code: del.error.code }, "", kind);
    else toast(t("rp.first_prompt_failed", { err: errText(del.error) }));
    return { ok: false, seed };
  }
  return { ok: true, seed };
}
