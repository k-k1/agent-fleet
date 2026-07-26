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
