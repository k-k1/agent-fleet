// Accepting a handoff (docs/log/77 / ADR 0057 decision 3) is an after-the-fact report that the
// session was launched.
//
// Never let the CP do the launching: that would have the CP drive someone else's Workspace, which
// is the very structure this feature exists to avoid. The recipient creates the session in their
// own Workspace with their own permissions, and only then does this send "accepted". So never call
// it from anywhere but the success path of the launch flow (StartHost).
//
// It is split out of the launch flow to avoid a circular import; sharing is the right home for it
// (an offer's lifetime is subordinate to the share ACL).
import { apiJSON } from "../../core/api/client.ts";
import { useHandoffStore } from "./handoffStore.ts";

export async function acceptHandoffOffer(offerId: string, sessionName: string): Promise<void> {
  await apiJSON(`api/session-handoff-offers/${encodeURIComponent(offerId)}/accept`, "POST", { sessionName }).catch(
    () => undefined, // best-effort: the launch already happened; a failed report must not break it
  );
  void useHandoffStore.getState().refresh();
}
