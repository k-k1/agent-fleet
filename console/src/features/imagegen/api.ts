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
