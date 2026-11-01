import { useEffect } from "react";
import { subscribeUsage } from "./usageAll.ts";
import { recordUsageReading } from "./usageReadings.ts";
import { REMAINING_SOURCES, USAGE_SOURCES } from "./usageSources.ts";

// The usage poll's owner for the life of the WS bar. The chips subscribe too, but on a phone they
// mount only while the ⋯ popover is open, so a reader of usageReadings.ts (Home) would see
// nothing until the member happened to open it, and nothing new after closing it. The subscriptions
// share the tenant's one hub (usageAll.ts), so this adds no request of its own: chips mounting in
// the same commit still fold into a single /usage/all.
export function useUsageFeed(tenant: string | null): void {
  useEffect(() => {
    const offs = [...USAGE_SOURCES, ...REMAINING_SOURCES].map((s) =>
      subscribeUsage(tenant, s.kind, s.endpoint, (d) => recordUsageReading(tenant, s.kind, d)),
    );
    return () => offs.forEach((off) => off());
  }, [tenant]);
}
