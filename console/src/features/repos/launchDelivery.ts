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

export async function uploadLaunchFiles(session: string, files: LaunchFile[], toast: Toast, current: () => boolean, signal?: AbortSignal): Promise<LaunchFile[]> {
  const uploaded: LaunchFile[] = [];
  for (const entry of files) {
    if (!entry.path && current() && !signal?.aborted) {
      const { file } = entry;
      try {
        const up = await (signal ? pasteImage(session, file, signal) : pasteImage(session, file));
        if (up.status < 300 && up.path) {
          uploaded.push({ file, path: up.path });
          continue;
        }
        if (!signal?.aborted) toast(t("rp.image_upload_failed", { name: file.name, err: up.error ? errText(up.error) : "" }));
      } catch {
        if (!signal?.aborted) toast(t("rp.image_upload_failed_network", { name: file.name }));
      }
    }
    uploaded.push(entry);
  }
  return uploaded;
}

export async function deliverLaunchPrompt(session: string, kind: string, prompt: string, files: LaunchFile[], toast: Toast, signal?: AbortSignal) {
  const seed = buildImagePrompt(prompt, files.map((entry) => entry.path), kind);
  const path = `api/sessions/${encodeURIComponent(session)}/input`;
  const body = { prompt: seed, when_ready: true };
  let del;
  try {
    del = await (signal ? apiJSON(path, "POST", body, undefined, { signal }) : apiJSON(path, "POST", body));
  } catch {
    if (signal) return { ok: false, unknown: true, seed };
    del = { error: { message: t("err.network") } };
  }
  if (signal) {
    // A proxy can fail after the Agent accepted the input. Only a confirmed
    // acknowledgement or an HTTP refusal settles the attempt.
    const code = del?.error?.code;
    const status = del?.error?.status ?? (typeof code === "string" && /^http_4\d\d$/.test(code) ? Number(code.slice(5)) : 0);
    const refused = status >= 400 && status < 500 && status !== 408;
    const accepted = !del?.error && (del?.queued === session || del?.sent === session);
    if (!refused && !accepted) return { ok: false, unknown: true, seed };
  }
  if (del?.error) {
    if (del.error.code === AUTH_EXPIRED_CODE) toastSendFailure(toast, { code: del.error.code }, "", kind);
    else toast(t("rp.first_prompt_failed", { err: errText(del.error) }));
    return { ok: false, seed };
  }
  return { ok: true, seed };
}
