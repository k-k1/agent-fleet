// English カタログ / ドメイン: engines (ADR 0084's topbar indicator)
// キー接頭辞: engine
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { engines as jaEngines } from "../ja/engines.ts";

export const engines: Record<keyof typeof jaEngines, string> = {
  "engine.role_chat": "Chat",
  "engine.role_images": "Images",
  "engine.state_in_use": "In use",
  "engine.state_ready": "Ready",
  "engine.state_running": "Running",
  "engine.state_starting": "Starting",
  "engine.state_stopping": "Stopping",
  "engine.state_stopped": "Stopped",
  "engine.state_available": "Available",
  "engine.lifecycle_external": "External",
  "engine.lifecycle_external_hint": "Runs outside this deployment. It cannot be started or stopped from here.",
  "engine.lifecycle_remote": "Borrowed",
  "engine.lifecycle_remote_hint": "Borrowed from another fleet. It cannot be started or stopped from here.",
  "engine.stops_at": "Stops at {t}",
  "engine.stops_in": "in {d}",
  "engine.idle_policy": "Stops automatically after {d} with nobody using it",
  // typical_ms (the wake-time estimate) does not ride on the member row (decision 3) — no
  // minute count here rather than a fabricated one (decision 4).
  "engine.cold_hint": "The engine starts on the next request. That can take a few minutes.",
  "engine.warm_model": "Model: ",
  "engine.last_used": "Last used {d} ago",
  "engine.queue_shared": "{n} queued",
  "engine.dur_hm": "{h}h {m}m",
  "engine.dur_m": "{m}m",
  // The member's own lcpp connection (docs/log/107 follow-up; ADR 0084 decision 10/11's "one
  // pill per role" carried over). This connection bypasses the Control Plane entirely, so
  // reusing the CP engine table's vocabulary (state_running etc.) would read as "the
  // deployment's engine" — a separate set of words instead.
  "engine.state_member_reachable": "Connected",
  "engine.state_member_unreachable": "Not reachable",
  "engine.state_member_unknown": "Checking",
  "engine.member_conn_hint": "Your own connection. Not this deployment's engine.",
};
