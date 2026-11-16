import { useMemo } from "react";
import { ASSISTANT_AGENT_KINDS } from "../../../lib/settings.ts";
import { chatOffered } from "../../../agents/launchAccess.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useMemberCatalog } from "../credentials/memberCatalog.ts";

/**
 * useOfferedChatKinds is the set of assistant backends the backend order and the model rows may
 * show: the kinds whose chat backend the credential policy allows and the member has not switched
 * off (ADR 0138 D10a), from the live catalogue. null while there is no fresh answer (the workspace
 * stopped, the answer out or failed): nothing is listed then, rather than every backend.
 */
export function useOfferedChatKinds(): Set<string> | null {
  const running = useWorkspaceStore((s) => s.state) === "running";
  const { kinds } = useMemberCatalog(running);
  return useMemo(() => (kinds ? new Set(ASSISTANT_AGENT_KINDS.filter((k) => chatOffered(k, kinds))) : null), [kinds]);
}
