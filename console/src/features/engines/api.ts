// Engine indicator — REST fallback (ADR 0084 decision 1). The tenant header rides every
// request via the global fetch wrapper (client.ts).
import { api } from "../../core/api/client.ts";

/** Fallback for the `engines` push stream: an old CP that 404s `/api/events`, or a stream
 *  that has not connected yet. Not polled on a tight interval — see store.ts. */
export function engineStatus(): Promise<unknown> {
  return api("api/engines/status");
}
