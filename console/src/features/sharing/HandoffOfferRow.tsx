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
import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore, useSpaceHandoff } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { identityScope, useTenantStore } from "../../core/store/tenant.ts";
import { boundedRequest, type BoundedRequest } from "./boundedRequest.ts";
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
  // False once this row is gone: an answer that arrives after the inbox was closed must not
  // start a launch nobody is looking at.
  const alive = useRef(true);
  // The space probe in flight: bounded, so an Agent that took the connection and never answers
  // frees the row (retry and decline work again), and cancelled with the row.
  const probe = useRef<BoundedRequest | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      probe.current?.cancel();
    };
  }, []);

  // The same probe the Start hub makes before it offers a space (StartModal): an older Agent
  // ignores the space field and would start the session in home, which is not what this offer is.
  // Only a real list of spaces proves support: an error, a dropped connection or a page that is
  // no list leaves it unknown, and "unknown" starts nothing (a retry may well succeed).
  const spacesSupport = async (): Promise<"yes" | "no" | "unknown"> => {
    const req = boundedRequest();
    probe.current = req;
    const d = await api("api/spaces", { signal: req.signal }).catch(() => null);
    req.settle();
    if (probe.current === req) probe.current = null;
    if (d && !d.error && Array.isArray(d.spaces)) return "yes";
    const e = d?.error;
    return e && (e.status === 404 || e.status === 405 || e.code === "http_404" || e.code === "http_405") ? "no" : "unknown";
  };
  const acceptInSpace = async () => {
    if (busy) return;
    setBusy(true);
    // What the launch state looked like when the button was pressed. The probe takes a moment, and
    // in it the member may close the inbox, open another offer from the same list or open the
    // Start hub: an answer that finds any of that changed writes nothing, so it can never
    // overwrite the seed of the launch that came after it.
    const scopeNow = () => identityScope(useTenantStore.getState());
    const before = { scope: scopeNow(), tick: useSessionsStore.getState().startTick, target: useLaunchTarget.getState().target, offerId: useSpaceHandoff.getState().offerId, seedOfferId: useLaunchSeed.getState().handoffOfferId };
    const support = await spacesSupport();
    if (alive.current) setBusy(false);
    if (
      !alive.current ||
      scopeNow() !== before.scope ||
      useSessionsStore.getState().startTick !== before.tick ||
      useLaunchTarget.getState().target !== before.target ||
      useSpaceHandoff.getState().offerId !== before.offerId ||
      useLaunchSeed.getState().handoffOfferId !== before.seedOfferId
    ) {
      return;
    }
    if (support !== "yes") {
      toast(tr(support === "no" ? "handoff.space_unsupported" : "handoff.space_check_failed"));
      return;
    }
    // A space offer has no repository to open it in: the launch is a dialog that starts in a
    // new space of the recipient's own, and StartHost takes the offer on its launch press.
    useSpaceHandoff.getState().open(offer.id, offer.prompt || "", offer.title);
    onDone();
  };
  const accept = () => {
    const repo = repos.find((r) => r.name === repoName);
    if (!repo?.path) {
      toast(tr("handoff.accept_no_repo"));
      return;
    }
    // Choosing a working copy supersedes a space handoff dialog that is still up.
    useSpaceHandoff.getState().clear();
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
      {offer.sourceSpace ? (
        <p className="ui-field-hint handoff-blocked">{tr("handoff.space_offer_hint", { who: senderLabel(offer) })}</p>
      ) : (
        <p className="ui-field-hint">
          {tr("handoff.coordinates", {
            branch: offer.branch || "-",
            sha: (offer.headSha || "").slice(0, 8),
            remote: offer.repoRemote || "-",
          })}
        </p>
      )}
      {/* The prompt is shown unfolded: the one-button flow assumes the recipient read it
          before pressing. */}
      <pre className="mirror-handoff-prompt">{offer.prompt}</pre>
      {!offer.sourceSpace && (
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
      )}
      {/* With no working copy at all the accept button cannot fire. Without a reason that
          only reads as a dead button, so the next step (clone it yourself) is spelled
          out. */}
      {!offer.sourceSpace && repos.length === 0 && <p className="ui-field-hint handoff-blocked">{tr("handoff.accept_no_repo_hint")}</p>}
      <p className="ui-field-hint">{tr("handoff.accept_cost_hint")}</p>
      {takeHint}
      <div className="handoff-inbox-actions">
        {giveBackButton}
        <Button variant="ghost" disabled={busy} onClick={() => void decline()}>
          {tr("handoff.decline")}
        </Button>
        <Button variant="primary" disabled={busy || (!offer.sourceSpace && !repoName)} onClick={offer.sourceSpace ? () => void acceptInSpace() : accept}>
          <Icon name="run" /> {tr("handoff.accept")}
        </Button>
      </div>
    </li>
  );
}
