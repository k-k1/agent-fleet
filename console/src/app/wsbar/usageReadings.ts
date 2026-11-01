import { create } from "zustand";
import type { UsageReading } from "./usageCommon.tsx";

// The latest reading of every usage chip, kept where Home can read it too.
//
// The chips own the only poll of the usage-limit endpoints (usageAll.ts: one shared
// GET /usage/all); every reading they receive is also written here. A second reader must read
// THIS store and never subscribe to the poll itself: subscribing starts a read, and
// api/claude/usage is a live upstream call.
//
// Same keep-the-last-good rule as the chip: a failed read replaces nothing once a reading
// exists, and is adopted only when there is none (its `authed` flag still means something).

export interface StoredReading {
  reading: UsageReading;
  /** Wall clock (ms) this Console received it; with reading.ageSec it dates the observation. */
  at: number;
}

interface UsageReadingsStore {
  /** Tenant the readings belong to; a different tenant's are dropped on its first record. */
  tenant: string;
  byKind: Record<string, StoredReading>;
}

export const useUsageReadings = create<UsageReadingsStore>(() => ({ tenant: "", byKind: {} }));

export function recordUsageReading(tenant: string | null, kind: string, reading: UsageReading, at = Date.now()): void {
  const key = tenant ?? "";
  useUsageReadings.setState((s) => {
    const cur = s.tenant === key ? s.byKind : NONE;
    if (cur[kind] && !reading.ok) return s.tenant === key ? s : { tenant: key, byKind: cur };
    return { tenant: key, byKind: { ...cur, [kind]: { reading, at } } };
  });
}

// One shared empty object: a zustand selector must return a stable reference, or the reader
// re-renders forever.
const NONE: Record<string, StoredReading> = {};

/** What the chips have read for this tenant so far; empty when none has been read yet. */
export function readingsFor(s: UsageReadingsStore, tenant: string | null): Record<string, StoredReading> {
  return s.tenant === (tenant ?? "") ? s.byKind : NONE;
}
