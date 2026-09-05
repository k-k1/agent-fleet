// Retry policy that keeps a single api/connections failure from being taken as final
// (pure logic — the actual fetch and the WS-state check are injected by useRepoRail).
//
// Right after a WS starts the Agent is not listening yet and this call fails with 502.
// Settling on null there pins launchKinds empty (it does not recover until connTick
// fires), so the whole launch flow degrades to "no usable agents". The symptom — the
// modal opens but lists no agent and the launch button stays disabled — hides its cause
// and is easily misdiagnosed. So keep re-fetching at growing intervals until it answers.
import type { ConnectionsStatus } from "../../types/session.ts";

/** Retry intervals (ms). ~22s in total plus the time each attempt takes. A boot too slow
 *  for that (native rootfs boot-install runs for minutes) is picked up when the WS moves
 *  to running and useRepoRail re-fetches under a new key, so there is no need to keep
 *  trying here indefinitely. */
export const CONNS_RETRY_MS = [1500, 3000, 6000, 12000];

const wait = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export interface ConnsRetryDeps {
  /** One fetch. A failure (network / non-2xx / error body) must be returned as null. */
  once: () => Promise<ConnectionsStatus | null>;
  sleep?: (ms: number) => Promise<void>;
  delays?: number[];
  /** Stop retrying as soon as this returns true (the WS stopped / this series went stale). */
  abort: () => boolean;
}

/** Re-fetch at the delays' intervals until it succeeds. Schedule exhausted -> null (the
 *  caller then shows "none"). */
export async function fetchConnsWithRetry({
  once,
  sleep = wait,
  delays = CONNS_RETRY_MS,
  abort,
}: ConnsRetryDeps): Promise<ConnectionsStatus | null> {
  for (let attempt = 0; ; attempt++) {
    const d = await once();
    if (d || attempt >= delays.length || abort()) return d;
    await sleep(delays[attempt]);
    if (abort()) return null;
  }
}
