import { rel } from "../../core/api/client.ts";
import { requireRevision } from "./buffer.ts";

export interface EditableFile {
  path: string;
  content: string;
  size: number;
  binary: false;
  truncated: false;
  editable: true;
  editabilityReason: null;
  revision: string;
}

export interface FileApiError {
  code: string;
  message: string;
}

export type PutFileResult =
  | { ok: true; path: string; size: number; revision: string }
  | { ok: false; status: number; error: FileApiError };

// A stalled PUT/GET would otherwise hold the dirty guard's discard wait (and
// its modal) open forever. The abort maps to io_timeout: for PUT that is a
// lost response (SaveStateUnknown), never an ordinary failure, because the
// Agent may have committed the rename before the client gave up.
export const EDITOR_IO_TIMEOUT_MS = 15_000;

async function withTimeout<T>(run: (signal: AbortSignal) => Promise<T>): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), EDITOR_IO_TIMEOUT_MS);
  try {
    return await run(controller.signal);
  } catch (error) {
    throw controller.signal.aborted ? new Error("io_timeout") : error;
  } finally {
    clearTimeout(timer);
  }
}

function apiError(status: number, body: unknown): PutFileResult {
  const value = body as { error?: Partial<FileApiError> } | null;
  return {
    ok: false,
    status,
    error: {
      code: value?.error?.code || `http_${status}`,
      message: value?.error?.message || `HTTP ${status}`,
    },
  };
}

export function putFile(
  path: string,
  content: string,
  baseDiskRevision: string,
): Promise<PutFileResult> {
  return withTimeout(async (signal) => {
    const response = await fetch(rel("api/fs/file"), {
      method: "PUT",
      headers: { "Content-Type": "application/json; charset=utf-8" },
      body: JSON.stringify({ path, content, baseDiskRevision }),
      signal,
    });
    let body: unknown = null;
    try {
      body = await response.json();
    } catch (error) {
      // A body read cut off by the timeout may hide a committed 200; it must
      // surface as io_timeout (lost response), not as an ordinary failed save.
      if (signal.aborted) throw error;
      // The same holds for any unreadable 2xx body (truncated stream, invalid
      // JSON): the Agent may have committed the rename, so this is a lost
      // response, never a confirmed ordinary failure.
      if (response.ok) throw new Error("invalid save response");
      return apiError(response.status, null);
    }
    if (!response.ok) return apiError(response.status, body);
    const value = body as { path?: unknown; size?: unknown; revision?: unknown };
    if (typeof value.path !== "string" || typeof value.size !== "number") {
      throw new Error("invalid save response");
    }
    return {
      ok: true,
      path: value.path,
      size: value.size,
      revision: requireRevision(value.revision),
    };
  });
}

/** What one external-change probe observed (docs/log/44 §7). `unavailable` covers
 *  transport failures, timeouts, and 5xx — the probe stays silent on those and
 *  the next trigger retries. The other kinds are advisory observations. */
export type FileProbeResult =
  | { kind: "revision"; revision: string }
  | { kind: "uneditable"; reason: string }
  | { kind: "missing" }
  | { kind: "boundary" }
  | { kind: "unavailable" };

/** Metadata-only GET (`meta=1`) for the external-change probe. Never throws:
 *  a probe is advisory, so every failure folds into a FileProbeResult. */
export async function probeFileMeta(path: string): Promise<FileProbeResult> {
  try {
    return await withTimeout(async (signal) => {
      const response = await fetch(rel(`api/fs/file?path=${encodeURIComponent(path)}&meta=1`), {
        cache: "no-store",
        signal,
      });
      let body: unknown = null;
      try {
        body = await response.json();
      } catch {
        body = null;
      }
      if (!response.ok) {
        // Application-level answers are observations (§7.5): the file is gone,
        // or its path stopped resolving safely (symlink/denylist/bad path).
        // Gateway/server failures are probe failures and stay silent.
        if (response.status === 404) return { kind: "missing" };
        if (response.status === 400 || response.status === 403) return { kind: "boundary" };
        return { kind: "unavailable" };
      }
      const value = body as {
        editable?: unknown;
        editabilityReason?: unknown;
        revision?: unknown;
      } | null;
      if (value?.editable === true) {
        try {
          return { kind: "revision", revision: requireRevision(value.revision) };
        } catch {
          return { kind: "unavailable" };
        }
      }
      if (value?.editable === false) {
        return {
          kind: "uneditable",
          reason: typeof value.editabilityReason === "string" ? value.editabilityReason : "not_editable",
        };
      }
      return { kind: "unavailable" };
    });
  } catch {
    return { kind: "unavailable" };
  }
}

// --- AI 変更提案（docs/log/44 Phase 4） ---

// LLM 生成はファイル IO の 15 秒では足りない。Agent 側の editSuggestTimeout（90s）
// より広く取り、通常はサーバー側のタイムアウトが先に確定する。
export const SUGGEST_EDIT_TIMEOUT_MS = 100_000;

export interface SuggestEditRequest {
  path: string;
  instruction: string;
  before: string;
  selection: string;
  after: string;
}

export type SuggestEditResult =
  | { ok: true; summary: string; replacement: string }
  | { ok: false; code: string };

/** POST /api/fs/suggest-edit — 置換文の生成だけを頼む同期呼び出し。envelope
 *  （paneId/requestId/sourceRevision）はクライアントが控えて応答へ合成するため
 *  wire には載せない。例外を投げず、全失敗を code に畳む（提案は advisory）。 */
export async function suggestEdit(request: SuggestEditRequest): Promise<SuggestEditResult> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), SUGGEST_EDIT_TIMEOUT_MS);
  try {
    const response = await fetch(rel("api/fs/suggest-edit"), {
      method: "POST",
      headers: { "Content-Type": "application/json; charset=utf-8" },
      body: JSON.stringify(request),
      signal: controller.signal,
    });
    let body: unknown = null;
    try {
      body = await response.json();
    } catch {
      body = null;
    }
    if (!response.ok) {
      const error = (body as { error?: Partial<FileApiError> } | null)?.error;
      return { ok: false, code: error?.code || `http_${response.status}` };
    }
    const value = body as { summary?: unknown; replacement?: unknown } | null;
    if (typeof value?.summary !== "string" || typeof value?.replacement !== "string") {
      return { ok: false, code: "generation_failed" };
    }
    return { ok: true, summary: value.summary, replacement: value.replacement };
  } catch {
    return { ok: false, code: controller.signal.aborted ? "io_timeout" : "unavailable" };
  } finally {
    clearTimeout(timer);
  }
}

export function getEditableFile(path: string): Promise<EditableFile> {
  return withTimeout(async (signal) => {
    const response = await fetch(rel(`api/fs/file?path=${encodeURIComponent(path)}`), {
      cache: "no-store",
      signal,
    });
    let body: unknown = null;
    try {
      body = await response.json();
    } catch (error) {
      if (signal.aborted) throw error;
      throw new Error(`http_${response.status}`);
    }
    if (!response.ok) {
      const error = (body as { error?: Partial<FileApiError> } | null)?.error;
      throw new Error(error?.code || `http_${response.status}`);
    }
    const value = body as Partial<EditableFile> & { editable?: boolean; editabilityReason?: string | null };
    if (
      value.editable !== true ||
      value.binary !== false ||
      value.truncated !== false ||
      typeof value.path !== "string" ||
      typeof value.content !== "string" ||
      typeof value.size !== "number"
    ) {
      throw new Error(value.editabilityReason || "not_editable");
    }
    return {
      path: value.path,
      content: value.content,
      size: value.size,
      binary: false,
      truncated: false,
      editable: true,
      editabilityReason: null,
      revision: requireRevision(value.revision),
    };
  });
}
