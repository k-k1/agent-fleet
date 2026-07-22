// Schedules Console endpoints (docs/38 P5). Member-scoped face of the CP scheduleAPI —
// read + manage only; create/edit stay on the operator MCP because a schedule is authored
// from natural language the operator translates to a cron spec (routes.go registerSchedule
// Routes). The tenant header rides every request via the global fetch wrapper (client.ts),
// so callers pass nothing extra.
import { api, apiJSON, raw } from "../../core/api/client.ts";
import type { ScheduleDTO, ScheduleRun } from "./read.ts";

const q = (id: string) => encodeURIComponent(id);

export function scheduleList(): Promise<ScheduleDTO[] | { error?: unknown }> {
  return api("api/schedules");
}

export function scheduleRuns(id: string): Promise<{ schedule_id: string; runs: ScheduleRun[] } | { error?: unknown }> {
  return api(`api/schedules/${q(id)}/runs`);
}

export function schedulePause(id: string): Promise<ScheduleDTO | { error?: unknown }> {
  return apiJSON(`api/schedules/${q(id)}/pause`, "POST");
}

export function scheduleResume(id: string): Promise<ScheduleDTO | { error?: unknown }> {
  return apiJSON(`api/schedules/${q(id)}/resume`, "POST");
}

export function scheduleRunNow(id: string): Promise<ScheduleDTO | { error?: unknown }> {
  return apiJSON(`api/schedules/${q(id)}/run-now`, "POST");
}

// DELETE returns 200 {deleted:true}; use raw so the caller can check r.ok (the idiom the
// other delete endpoints use — memoDelete/chatDelete).
export function scheduleDelete(id: string): Promise<Response> {
  return raw(`api/schedules/${q(id)}`, { method: "DELETE" });
}
