// HandoffOfferRow renders one received handoff (docs/log/77 / ADR 0057).
//
// It is shown both by the inbox (HandoffInboxModal) and by the banner in the shared view
// (SharedSessionView); it is a separate component because acceptance must not live in only
// one place — a notification leads to the shared view, which had no way to accept, leaving
// the rail heading icon as the only entry point.
//
// "One button" means nothing to think about BEFORE pressing, not launching the moment it is
// pressed (docs/log/77 §77.1). Pressing opens the pre-filled launch modal, where the
// recipient still chooses the prompt, the working copy and the agent: it runs in the
// recipient's own Workspace and is billed to them, so that confirmation cannot be skipped.
//
// The launch itself rides entirely on the existing path (useLaunchTarget → LaunchModal →
// useStartWork). The CP never operates someone else's Workspace (ADR 0057 decision 3), so
// acceptance is reported after the fact by StartHost, once the launch succeeded.
import { useMemo, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { useHandoffStore, type HandoffOffer } from "./handoffStore.ts";
import "./sharing.css";

/** Guesses a working-copy name from a remote URL (`…/k-k1/agent-fleet.git` →
 *  `agent-fleet`). The recipient's working-copy list carries only the remote HOST, so the
 *  URLs cannot be matched against each other; this guess only fills the default, and the
 *  recipient decides in the select. */
export function repoNameFromRemote(remote?: string): string {
  if (!remote) return "";
  const last = remote.replace(/\/+$/, "").split("/").filter(Boolean).at(-1) || "";
  return last.replace(/\.git$/i, "");
}

export function HandoffOfferRow({ offer, onDone }: { offer: HandoffOffer; onDone: () => void }) {
  const tr = useT();
  const toast = useToast();
  const repos = useReposStore((s) => s.repos);
  const guess = useMemo(() => repoNameFromRemote(offer.repoRemote), [offer.repoRemote]);
  const [repoName, setRepoName] = useState(() => (repos.some((r) => r.name === guess) ? guess : repos[0]?.name || ""));
  const [busy, setBusy] = useState(false);

  const accept = () => {
    const repo = repos.find((r) => r.name === repoName);
    if (!repo?.path) {
      toast(tr("handoff.accept_no_repo"));
      return;
    }
    // Carry which offer this is into the launch path. StartHost sends the accept once the
    // launch succeeds: a cancelled launch must not mark the offer accepted.
    useLaunchSeed.getState().set(offer.prompt || "", offer.title, "", "", offer.id);
    useLaunchTarget.getState().open({ name: repo.name, path: repo.path, branch: offer.branch || repo.branch, worktree: repo.worktree });
    onDone();
  };
  const decline = async () => {
    if (busy) return;
    setBusy(true);
    const d = await apiJSON(`api/session-handoff-offers/${encodeURIComponent(offer.id)}/decline`, "POST", {});
    setBusy(false);
    if (d?.error) {
      toast(errText(d.error));
    }
    void useHandoffStore.getState().refresh();
  };

  return (
    <li className="handoff-inbox-row">
      <header className="handoff-inbox-head">
        <strong>{offer.title}</strong>
        <span className="muted">{tr("handoff.from", { who: offer.ownerUserKey || "" })}</span>
      </header>
      <p className="ui-field-hint">
        {tr("handoff.coordinates", {
          branch: offer.branch || "-",
          sha: (offer.headSha || "").slice(0, 8),
          remote: offer.repoRemote || "-",
        })}
      </p>
      {/* The prompt is shown unfolded: the one-button flow assumes the recipient read it
          before pressing. */}
      <pre className="mirror-handoff-prompt">{offer.prompt}</pre>
      <label className="ui-field">
        <span className="ui-field-label">{tr("handoff.accept_repo")}</span>
        <select value={repoName} onChange={(e) => setRepoName(e.target.value)}>
          {repos.map((r) => (
            <option key={r.name} value={r.name}>
              {r.name}
            </option>
          ))}
        </select>
      </label>
      {/* With no working copy at all the accept button cannot fire. Without a reason that
          only reads as a dead button, so the next step (clone it yourself) is spelled
          out. */}
      {repos.length === 0 && <p className="ui-field-hint handoff-blocked">{tr("handoff.accept_no_repo_hint")}</p>}
      <p className="ui-field-hint">{tr("handoff.accept_cost_hint")}</p>
      <div className="handoff-inbox-actions">
        <Button variant="ghost" disabled={busy} onClick={() => void decline()}>
          {tr("handoff.decline")}
        </Button>
        <Button variant="primary" disabled={busy || !repoName} onClick={accept}>
          <Icon name="run" /> {tr("handoff.accept")}
        </Button>
      </div>
    </li>
  );
}
