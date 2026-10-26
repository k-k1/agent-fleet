import type { t } from "../../../lib/i18n/index.ts";

// endpointLabel names the MCP endpoint a token or connection is bound to (#2764). A row without
// one comes from a Control Plane that serves /mcp alone.
export function endpointLabel(tr: typeof t, endpoint?: string): string {
  return endpoint === "memory" ? tr("tokens.endpoint_short_memory") : tr("tokens.endpoint_short_mcp");
}
