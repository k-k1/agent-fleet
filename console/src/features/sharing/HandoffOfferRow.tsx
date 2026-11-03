// HandoffOfferRow renders one received handoff (docs/log/77 / ADR 0057).
//
// It is shown by the handoff dialog (HandoffInboxModal), the banner in the shared view
// (SharedSessionView) and the top-bar inbox (InboxModal); it is a separate component because acceptance must not live in only
// one place — a notification leads to the shared view, which had no way to accept, leaving
// the rail heading icon as the only entry point.
//
// "One button" means nothing to think about BEFORE pressing, not launching the moment it is
// pressed (docs/log/77 §77.1). Pressing opens the pre-filled launch modal, where the
// recipient still chooses the prompt, the working copy and the agent: it runs in the
// recipient's own Workspace and is billed to them, so that confirmation cannot be skipped.
//
// The launch itself rides entirely on the existing path (useLaunchTarget → LaunchModal →
// useStartWork). The CP never operates someone else's Workspace (ADR 0057 decision 3);
// StartHost takes the offer on the launch press and accepts or gives it back around the
// launch (acceptHandoff.ts).
import { useMemo, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { senderLabel, useHandoffStore, type HandoffOffer } from "./handoffStore.ts";
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
  const askConfirm = useConfirm();
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
    // Carry which offer this is into the launch path. Nothing is taken here: StartHost takes
    // the offer only when the launch is pressed, so a cancelled dialog leaves it pending.
    useLaunchSeed.getState().set(offer.prompt || "", offer.title, "", "", offer.id);
    // The whole row, not a hand-picked subset: the dialog gates the worktree choice on
    // `vcs`/`unborn`, and a copy of the row that drops them offers git actions on an SVN
    // checkout (docs/log/41), which has no worktrees at all.
    useLaunchTarget.getState().open({ ...repo, branch: offer.branch || repo.branch });
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
  // "This session is not in my workspace — give it back" (#2058): only for a launch whose result
  // stays unknown. The confirmation names the risk because the CP cannot check the statement and
  // giving back cancels nothing: if the session exists, or the launch is still being processed
  // and starts later, the sender may start the same work and it runs twice. takeRev binds the
  // confirmation to the launch this row shows, so a stale row never gives back a later one.
  const giveBack = async () => {
    if (busy) return;
    const ok = await askConfirm({
      title: tr("handoff.give_back_confirm_title", { who: senderLabel(offer) }),
      body: tr("handoff.give_back_confirm_body", { who: senderLabel(offer) }),
      confirmLabel: tr("handoff.give_back_confirm_action"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    const d = await apiJSON(`api/session-handoff-offers/${encodeURIComponent(offer.id)}/give-back`, "POST", {
      confirmed: true,
      takeRev: offer.takeRev || "",
    }).catch(() => ({ error: { message: tr("err.network") } }));
    setBusy(false);
    const e = d?.error as { code?: string; message?: string; takeState?: string } | undefined;
    if (!e) toast(tr("handoff.give_back_done", { who: senderLabel(offer) }));
    else if (e.code === "handoff_give_back_stale") toast(tr("handoff.give_back_stale"));
    else if (e.code === "handoff_not_unknown") toast(tr(e.takeState ? "handoff.give_back_too_soon" : "handoff.give_back_not_held"));
    else toast(tr("handoff.give_back_failed", { err: errText({ code: e.code, message: e.message }) }));
    void useHandoffStore.getState().refresh();
  };

  const takeHint =
    offer.takeState === "unknown" ? (
      <p className="ui-field-hint handoff-blocked">{tr("handoff.unknown_hint")}</p>
    ) : offer.takeState ? (
      <p className="ui-field-hint">{tr("handoff.confirming_hint")}</p>
    ) : null;
  const giveBackButton = offer.takeState === "unknown" && (
    <Button variant="ghost" disabled={busy} onClick={() => void giveBack()}>
      {tr("handoff.give_back")}
    </Button>
  );

  // Listed only because the recipient's earlier launch still holds it after an unshare or an
  // archive: the CP sends no body or coordinates, and nothing may launch or decline from it.
  if (offer.recoveryOnly) {
    return (
      <li className="handoff-inbox-row">
        <header className="handoff-inbox-head">
          <strong>{offer.title}</strong>
          <span className="muted">{tr("handoff.from", { who: senderLabel(offer) })}</span>
        </header>
        <p className="ui-field-hint">{tr("handoff.recovery_hint")}</p>
        {takeHint}
        {giveBackButton && <div className="handoff-inbox-actions">{giveBackButton}</div>}
      </li>
    );
  }

  return (
    <li className="handoff-inbox-row">
      <header className="handoff-inbox-head">
        <strong>{offer.title}</strong>
        <span className="muted">{tr("handoff.from", { who: senderLabel(offer) })}</span>
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
      {takeHint}
      <div className="handoff-inbox-actions">
        {giveBackButton}
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
