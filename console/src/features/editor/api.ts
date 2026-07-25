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

export async function putFile(
  path: string,
  content: string,
  baseDiskRevision: string,
): Promise<PutFileResult> {
  const response = await fetch(rel("api/fs/file"), {
    method: "PUT",
    headers: { "Content-Type": "application/json; charset=utf-8" },
    body: JSON.stringify({ path, content, baseDiskRevision }),
  });
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
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
}

export async function getEditableFile(path: string): Promise<EditableFile> {
  const response = await fetch(rel(`api/fs/file?path=${encodeURIComponent(path)}`), {
    cache: "no-store",
  });
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
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
}
