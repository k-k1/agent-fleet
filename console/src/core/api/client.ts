// core/api/client — the next console's API core.
//
// During the parallel-entry transition (docs/22) the implementation stays in
// src/api.ts, shared by both entries so there is exactly ONE fetch wrapper /
// tenant source of truth. This module re-exports the transport primitives; new
// code imports from here (or from core/api/endpoints/* once those land per
// feature in P1+) and NEVER from ../../api.ts directly. At swap time (P8) the
// implementation moves into this directory and src/api.ts is deleted.
//
// Domain wrappers (chat/assistant/memo/…) are intentionally NOT re-exported —
// they belong to feature endpoint modules, ported with their feature.
export {
  rel,
  getTenant,
  setTenant,
  api,
  apiJSON,
  raw,
  rawJSON,
  errText,
  previewURL,
  ocwebURL,
  downloadURL,
  uploadFiles,
  fsMkdir,
  fsNewFile,
  fsRename,
  fsDelete,
  wsURL,
} from "../../api.ts";
export type { ApiError, FsResult } from "../../api.ts";
