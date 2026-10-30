import type { MsgKey } from "../../lib/i18n/index.ts";

// The Agent's closed set of boot-install failure reasons (workspace/agent/boot_install_status.go)
// mapped to their strings; anything unknown reads as "other". Shared by the member's own
// Toolchains tab and the admin member views, which must say the same thing about one failure.
export function bootReasonKey(reason?: string): MsgKey {
  switch (reason) {
    case "no_space":
      return "env.boot_reason_no_space";
    case "network":
      return "env.boot_reason_network";
    case "checksum":
      return "env.boot_reason_checksum";
    default:
      return "env.boot_reason_other";
  }
}
