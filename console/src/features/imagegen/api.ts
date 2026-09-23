// features/imagegen/api — the seven routes of ADR 0081 (decisions 2, 3, 12), all of them
// through the control plane's agent proxy under `api/imagegen/*` (decision 1): the browser
// holds no engine credential, and the graph, the disk and the ledger are all in the
// Workspace Agent.
//
// The shapes live in `./wire.ts` and are re-exported here, so a caller that needs both a
// type and a fetch has one import. Pure modules must import from `wire.ts` directly — this
// file pulls in the shared client, which reads `localStorage` at module scope and is not
// loadable in the node test project.
import { api, apiJSON, raw } from "../../core/api/client.ts";
import type {
  EnqueueRequest,
  EnqueueResult,
  GroupOp,
  ImageProperties,
  ImagegenStatus,
  JobsResponse,
  QueueOp,
  DraftLogPage,
  HistoryPage,
  Knowledge,
  KnowledgeAdd,
  KnowledgeScope,
  PressMode,
  StudioCreate,
  StudioList,
  StudioPatch,
  StudioPatchResult,
  StudioPersona,
  StudioPressResult,
  StudioWire,
} from "./wire.ts";
import type { ApiError } from "../../core/api/client.ts";

export * from "./wire.ts";

export const imagegenStatus = (): Promise<ImagegenStatus> => api("api/imagegen/status");

export const imagegenJobs = (): Promise<JobsResponse> => api("api/imagegen/jobs");

export const imagegenEnqueue = (body: EnqueueRequest): Promise<EnqueueResult> =>
  apiJSON("api/imagegen/jobs", "POST", body);

/** Cancel ONE job. Queued: removed. Running: the provider's targeted interrupt. */
export const imagegenCancelJob = (id: string): Promise<Response> =>
  raw(`api/imagegen/jobs/${encodeURIComponent(id)}`, { method: "DELETE" });

export const imagegenGroupOp = (id: string, op: GroupOp): Promise<{ error?: ApiError }> =>
  apiJSON(`api/imagegen/groups/${encodeURIComponent(id)}`, "POST", { op });

export const imagegenQueueOp = (op: QueueOp): Promise<{ error?: ApiError }> =>
  apiJSON("api/imagegen/queue", "POST", { op });

/**
 * Read one picture's properties. On demand ONLY — when the lightbox opens or a card is asked.
 * Never for a folder on mount: 300 originals is the bandwidth ADR 0080 decision 4 refused,
 * and the thumbnail route is a re-encode that drops the PNG chunk anyway.
 */
export const imageProperties = (path: string): Promise<ImageProperties> =>
  api(`api/imagegen/props?path=${encodeURIComponent(path)}`);

// --- ADR 0100: the image studio -------------------------------------------------------------

const studioPath = (id: string, rest = "") => `api/imagegen/studios/${encodeURIComponent(id)}${rest}`;

export const listStudios = (): Promise<StudioList> => api("api/imagegen/studios");

export const createStudio = (body: StudioCreate): Promise<StudioWire> => apiJSON("api/imagegen/studios", "POST", body);

export const getStudio = (id: string): Promise<StudioWire> => api(studioPath(id));

/** Merge-patch the studio. `ifMatch` is the `updated_at` the pane last read: a write that lost
 *  a race is refused (412) rather than silently overwriting the agent's (or the member's) edit.
 *  The status comes back with the body because the caller answers 412, a 5xx and a refusal three
 *  different ways; 0 is a request that never got an answer. */
export const patchStudio = async (
  id: string,
  body: StudioPatch,
  ifMatch: string,
): Promise<StudioPatchResult & { status: number }> => {
  let r: Response;
  try {
    r = await raw(studioPath(id), {
      method: "PUT",
      headers: { "Content-Type": "application/json", "If-Match": ifMatch },
      body: JSON.stringify(body),
    });
  } catch {
    return { status: 0 } as StudioPatchResult & { status: number };
  }
  const parsed = (await r.json().catch(() => ({}))) as StudioPatchResult;
  return { ...parsed, status: r.status };
};

/** Deletes the draft and its versions; the pictures stay. */
export const deleteStudio = (id: string): Promise<Response> => raw(studioPath(id), { method: "DELETE" });

/** Attach a session, or detach with an empty one. */
export const bindStudio = (id: string, session: string): Promise<StudioWire> =>
  apiJSON(studioPath(id, "/bind"), "POST", { session });

export const pressStudio = (id: string, mode: PressMode): Promise<StudioPressResult> =>
  apiJSON(studioPath(id, "/press"), "POST", { mode });

export const rewindStudio = (id: string, to: number): Promise<StudioWire> =>
  apiJSON(studioPath(id, "/rewind"), "POST", { to });

export const studioDraftLog = (id: string, before?: number, limit?: number): Promise<DraftLogPage> => {
  const q = new URLSearchParams();
  if (before) q.set("before", String(before));
  if (limit) q.set("limit", String(limit));
  const qs = q.toString();
  return api(studioPath(id, "/draft-log") + (qs ? `?${qs}` : ""));
};

export const studioPersona = (id: string): Promise<StudioPersona> => api(studioPath(id, "/persona"));

export const imagegenHistory = (opts: { studio?: string; before?: string; limit?: number } = {}): Promise<HistoryPage> => {
  const q = new URLSearchParams();
  if (opts.studio) q.set("studio", opts.studio);
  if (opts.before) q.set("before", opts.before);
  if (opts.limit) q.set("limit", String(opts.limit));
  const qs = q.toString();
  return api("api/imagegen/history" + (qs ? `?${qs}` : ""));
};

export const imagegenKnowledge = (scope: KnowledgeScope, key: string): Promise<Knowledge> =>
  api(`api/imagegen/knowledge?scope=${encodeURIComponent(scope)}&key=${encodeURIComponent(key)}`);

export const addImagegenKnowledge = (body: KnowledgeAdd): Promise<{ error?: ApiError }> =>
  apiJSON("api/imagegen/knowledge", "POST", body);
