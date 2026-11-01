// SessionLinks — a session row's second line (#1062): the pull request of the branch the
// session works on, the work items it is linked to (#3134), and the ports its processes listen
// on. An item opens the work item panel, the same one a ticket link in the mirror opens. A port opens in the workspace's
// default target (subdomain, else browser pane, else lightweight preview — see features/preview).
//
// A line of its own rather than chips beside the title: the rail is narrow and the title is
// already the part that gets cut. A row with neither draws nothing and stays one line, so the
// height is only paid where there is something row-specific to say.
import { useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { PortOpen } from "../preview/PortOpen.tsx";
import { useWorkItemStore } from "../workitems/store.ts";
import { useWorkItemModal } from "../workitems/modal.ts";
import { resolveWorkItemRef } from "../workitems/refs.ts";
import {
  CHECK_ICON,
  LINK_ROLE_ICON,
  PR_ICON,
  knownRole,
  linkDone,
  linkPRState,
  prChecks,
  prStateKey,
  rowPorts,
  rowWorkLinks,
  safePRURL,
} from "./sessionLinks.ts";
import type { Session, SessionWorkLink } from "../../types/session.ts";

/** Ports drawn before the rest fold behind "+N": one line at the rail's narrowest. */
const PORTS_SHOWN = 3;
/** Linked items drawn before the rest fold behind "+N", for the same reason. */
const LINKS_SHOWN = 2;

/** Whether a row has a link line to draw. */
export function hasSessionLinks(s: Session, running: boolean): boolean {
  return (
    Boolean(s.pr && safePRURL(s.pr.url)) ||
    rowWorkLinks(s).length > 0 ||
    (running && Boolean(s.alive) && rowPorts(s.ports).length > 0)
  );
}

export function SessionLinks({ s, running }: { s: Session; running: boolean }) {
  const tr = useT();
  const [allPorts, setAllPorts] = useState(false);
  const [allLinks, setAllLinks] = useState(false);
  const links = rowWorkLinks(s);
  const pr = s.pr;
  const href = pr ? safePRURL(pr.url) : null;
  // A port is only worth offering while the Workspace runs: the pane cannot reach it otherwise.
  const ports = running && s.alive ? rowPorts(s.ports) : [];
  if (!(pr && href) && links.length === 0 && ports.length === 0) return null;

  return (
    <div className="sess-links" aria-label={tr("srow.links")}>
      {pr && href && (() => {
        const key = prStateKey(pr);
        const ci = prChecks(pr);
        return (
          <a
            className={"sess-pr pr-" + key}
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            title={tr("srow.pr_title", {
              n: pr.number,
              state: tr(`srow.pr_state.${key}`),
              checks: ci ? tr(`srow.pr_checks.${ci}`) : "",
            })}
          >
            <Icon name={PR_ICON[key]} />#{pr.number}
            {ci && <Icon name={CHECK_ICON[ci]} className={"sess-pr-ci ci-" + ci} />}
          </a>
        );
      })()}
      {links.length > 0 && (
        <span className="sess-items">
          {(allLinks ? links : links.slice(0, LINKS_SHOWN)).map((l) => (
            <WorkLinkChip key={`${l.provider}:${l.key}`} link={l} />
          ))}
          {!allLinks && links.length > LINKS_SHOWN && (
            <button
              type="button"
              className="sess-port sess-port-more"
              title={tr("srow.links_more", { n: links.length - LINKS_SHOWN })}
              onClick={() => setAllLinks(true)}
            >
              +{links.length - LINKS_SHOWN}
            </button>
          )}
        </span>
      )}
      {ports.length > 0 && (
        <span className="sess-ports">
          <Icon name="globe" />
          {(allPorts ? ports : ports.slice(0, PORTS_SHOWN)).map((port) => (
            <PortOpen key={port} port={port} running={running} variant="chip" />
          ))}
          {!allPorts && ports.length > PORTS_SHOWN && (
            <button
              type="button"
              className="sess-port sess-port-more"
              title={tr("srow.ports_more", { n: ports.length - PORTS_SHOWN })}
              onClick={() => setAllPorts(true)}
            >
              +{ports.length - PORTS_SHOWN}
            </button>
          )}
        </span>
      )}
    </div>
  );
}

/** One linked item. The row of the inbox cache is looked up at click time, as the mirror's ticket
 * links do: an item that is not cached opens as a reference stand-in. */
function WorkLinkChip({ link }: { link: SessionWorkLink }) {
  const tr = useT();
  const open = () => {
    const { item, reference } = resolveWorkItemRef(
      { provider: link.provider, key: link.key },
      useWorkItemStore.getState().payload?.items || [],
    );
    useWorkItemModal.getState().openDetail(item, { reference });
  };
  const role = knownRole(link.role);
  // A finished pull request draws its state, not the role it was worked in: the role is over.
  const prState = linkPRState(link);
  const done = linkDone(link);
  const roleText = role ? tr(`srow.link_role.${role}`) : "";
  return (
    <button
      type="button"
      className={"sess-item" + (role ? " role-" + role : "") + (done ? " done" : "")}
      title={tr("srow.link_title", {
        key: link.key,
        role: prState ? `${roleText} · ${tr(`srow.pr_state.${prState}`)}` : roleText,
        title: link.title ? ` — ${link.title}` : "",
      })}
      onClick={open}
    >
      <Icon name={done && prState ? PR_ICON[prState] : role ? LINK_ROLE_ICON[role] : "link"} />
      {link.key}
    </button>
  );
}
