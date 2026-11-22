// agentsApi — typed wrapper for the one write the Agents tab makes outside the shared
// ../api.ts readers: starting the on-demand CLI install. The path is a template over the two
// kinds that install on demand (kiro, muse); control-plane/console_routes_test.go reads it as text.
//
// api() can resolve with a JSON `null` body: the type keeps `| null` and the caller treats it as a
// failed start.
import { api } from "../../../core/api/client.ts";
import type { Rep } from "../workspace/workspaceApi.ts";
import type { CliInstallStatus } from "../api.ts";

export type CliInstallKind = "kiro" | "muse";

export const startCliInstall = (kind: CliInstallKind): Promise<Rep<CliInstallStatus> | null> =>
  api(`api/connections/${kind}/install`, { method: "POST" });
