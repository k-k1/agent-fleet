import { sha256 } from "@noble/hashes/sha2.js";
import { bytesToHex } from "@noble/hashes/utils.js";

export const MAX_EDIT_BYTES = 2 * 1024 * 1024;
export const REVISION_RE = /^sha256:[0-9a-f]{64}$/;

export type BufferErrorCode =
  | "too_large"
  | "binary_not_supported"
  | "unsupported_newline"
  | "invalid_unicode";

export interface BufferValidationError {
  code: BufferErrorCode;
  message: string;
}

/** Validate the invariant shared by every editor transaction and PUT snapshot. */
export function validateEditorBuffer(content: string): BufferValidationError | null {
  for (let i = 0; i < content.length; i++) {
    const unit = content.charCodeAt(i);
    if (unit === 0) {
      return { code: "binary_not_supported", message: "NUL is not supported in editable files" };
    }
    if (unit === 13) {
      return { code: "unsupported_newline", message: "Only LF newlines are supported" };
    }
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const low = content.charCodeAt(i + 1);
      if (!(low >= 0xdc00 && low <= 0xdfff)) {
        return { code: "invalid_unicode", message: "Unpaired high surrogate is not supported" };
      }
      i++;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return { code: "invalid_unicode", message: "Unpaired low surrogate is not supported" };
    }
  }
  if (new TextEncoder().encode(content).byteLength > MAX_EDIT_BYTES) {
    return { code: "too_large", message: "Editable content exceeds 2 MiB" };
  }
  return null;
}

export function revisionOf(content: string): string {
  const error = validateEditorBuffer(content);
  if (error) throw new Error(error.code);
  return `sha256:${bytesToHex(sha256(new TextEncoder().encode(content)))}`;
}

export function requireRevision(value: unknown): string {
  if (typeof value !== "string" || !REVISION_RE.test(value)) {
    throw new Error("invalid revision");
  }
  return value;
}
