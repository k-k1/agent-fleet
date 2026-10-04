// Re-asking the AWS / Google Cloud login profiles after a tenant switch. Both workspaces running
// means no running edge re-asks, and the chips stay hidden while profiles is null, so a first
// answer lost to the agent's 502 would leave them empty. Shaped for useRetryLoad: false = ask again.
import { useAwsLoginStore } from "../features/awslogin/store.ts";
import { useGcpLoginStore } from "../features/gcplogin/store.ts";
import { useWorkspaceStore } from "../core/store/workspace.ts";

/** true when both answered, or when the workspace is stopped (its running edge asks instead). */
export async function reloadCloudProfiles(): Promise<boolean> {
  const [aws, gcp] = await Promise.all([
    useAwsLoginStore.getState().refreshExpiry(),
    useGcpLoginStore.getState().refreshProfiles(),
  ]);
  if (aws !== null && gcp) return true;
  return useWorkspaceStore.getState().state === "stopped";
}
