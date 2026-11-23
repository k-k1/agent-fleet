// TerminalsSection — the Terminal view (ADR 0142 decision 10): every shell and SSM session, shells
// grouped by the place they were opened in (home, a repository, a space) and SSM logins by
// machine (account, region, instance id). The Sessions view lists none of them; a count chip on
// its repository and space rows brings the member here (useTerminalReveal).
//
// A group is never a fold: the view is a list of places, and a group that could hide its rows
// would hide the very shells the chip promised.
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useViewShown } from "../../lib/viewShown.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useReposStore } from "../repos/store.ts";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { terminalGroups, workingCopyLabel, worktreeTag } from "../../lib/project.ts";
import type { ShellGroup, SsmGroup } from "../../lib/project.ts";
import { useRailRoving } from "../project/useRailRoving.ts";
import { useUnreadSessions } from "../notifications/unread.ts";
import { spaceLabel, spacesOwner } from "../spaces/store.ts";
import { useSpaceNames } from "../spaces/useSpaceNames.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { SsmHost } from "../../types/session.ts";
import { NewShellModal } from "./NewShellModal.tsx";
import { machineLabel, ssmRowDetail } from "./ssmLabel.ts";
import type { SsmProfileRef } from "./ssmLabel.ts";
import { useTerminalView } from "./useTerminalView.ts";
import { useTerminalReveal } from "./store.ts";

const NO_HOSTS: SsmHost[] = [];
const NO_PROFILES: SsmProfileRef[] = [];

export const TerminalsSection = memo(function TerminalsSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const spaces = useSpaceNames(sessions);
  const ctx = useRepoRailContext();
  const actions = useSessionActions();
  const rail = useRailRoving();
  const openStartAt = useSessionsStore((s) => s.openStartAt);
  const [newShell, setNewShell] = useState(false);

  // What the view lists is what the chips count: the working set narrows both the same way.
  const { visible } = useTerminalView();
  const groups = useMemo(() => terminalGroups(visible, repos), [visible, repos]);
  const all = useMemo(() => [...groups.shells.flatMap((g) => g.sessions), ...groups.ssm.flatMap((g) => g.sessions)], [groups]);
  const stopped = all.filter((s) => !s.alive && !s.locked);

  // The registered hosts and their profiles name the machines. Read again whenever the view comes
  // on screen (a host renamed in Settings since, or a read that failed), when the set of machines
  // changes, and for another workspace; an answer is kept with the workspace it came from, so a
  // late one from the previous tenant never names this one's machines.
  const machines = groups.ssm.map((g) => g.key).join("\n");
  const shown = useViewShown();
  const owner = spacesOwner();
  const [names, setNames] = useState<{ owner: string; hosts: SsmHost[]; profiles: SsmProfileRef[] }>({ owner: "", hosts: [], profiles: [] });
  useEffect(() => {
    if (!machines || !shown) return;
    let alive = true;
    Promise.all([api("api/ssm/hosts"), api("api/ssm/profiles")])
      .then(([h, p]) => {
        if (!alive) return;
        setNames({ owner, hosts: Array.isArray(h) ? h : [], profiles: Array.isArray(p) ? p : [] });
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [machines, shown, owner]);
  const hosts = names.owner === owner ? names.hosts : NO_HOSTS;
  const profiles = names.owner === owner ? names.profiles : NO_PROFILES;

  // Reveal request from a count chip: scroll to the group and say which one it is. Read on mount
  // too, because the view mounts on the very request that shows it.
  const rootRef = useRef<HTMLDivElement>(null);
  const revealN = useTerminalReveal((s) => s.n);
  useEffect(() => {
    const key = useTerminalReveal.getState().key;
    if (!key) return;
    let flash: ReturnType<typeof setTimeout> | undefined;
    const raf = requestAnimationFrame(() => {
      const el = Array.from(rootRef.current?.querySelectorAll<HTMLElement>("[data-term-group]") ?? []).find((e) => e.dataset.termGroup === key);
      if (!el) return;
      el.scrollIntoView({ block: "nearest" });
      el.focus();
      // One request, one reveal: a view that mounts again later (a restart) must not jump back.
      useTerminalReveal.setState({ key: null });
      for (const f of rootRef.current?.querySelectorAll(".term-flash") ?? []) f.classList.remove("term-flash");
      void el.offsetWidth; // restart the animation for a repeat reveal of the same group
      el.classList.add("term-flash");
      // Gone after the animation, so a reduced-motion rule that leaves a background does not keep it.
      flash = setTimeout(() => el.classList.remove("term-flash"), 1400);
    });
    return () => {
      cancelAnimationFrame(raf);
      clearTimeout(flash);
    };
  }, [revealN]);

  const idBase = useRef(Math.random().toString(36).slice(2, 8)).current;
  const unreadSessions = useUnreadSessions();
  const unread = all.some((s) => unreadSessions.has(s.name));

  const shellLabel = (g: ShellGroup): { label: string; sub: string; icon: string } => {
    if (g.place.kind === "home") return { label: tr("terms.home"), sub: "", icon: "home" };
    if (g.place.kind === "space") return { label: spaceLabel(g.place.folder, spaces), sub: tr("pj.space_folder_hint", { folder: g.place.folder }), icon: "folder" };
    const folder = g.place.folder;
    const r = repos.find((x) => x.name === folder);
    return { label: workingCopyLabel(folder, r).project, sub: r?.worktree ? worktreeTag(folder, r) : "", icon: "repo" };
  };
  const ssmLabel = (g: SsmGroup): { label: string; sub: string } => {
    if (!g.target) return { label: tr("terms.ssm_unknown"), sub: "" };
    const label = machineLabel(g.target, g.region, g.accountId, hosts, profiles);
    // The instance id only when the alias took the name's place; account and region tell
    // two machines with one id apart (a copy in another account).
    return { label, sub: [label !== g.target ? g.target : "", g.accountId, g.region].filter(Boolean).join(" · ") };
  };

  const head = (key: string, icon: string, label: string, sub: string, rowsId: string, title?: string) => (
    <div className="proj-node-head space-group-head">
      <div
        className="space-group-row term-group-row"
        role="treeitem"
        aria-expanded={true}
        aria-owns={rowsId}
        tabIndex={-1}
        data-rail-row=""
        data-term-group={key}
        title={title ?? (sub ? `${label} · ${sub}` : label)}
      >
        <Icon name={icon} />
        <span className="space-group-name">{label}</span>
        {sub && <span className="term-group-sub">{sub}</span>}
      </div>
    </div>
  );
  const rowsOf = (list: typeof all, id: string, label: string, detailOf?: (s: (typeof all)[number]) => string) => (
    <ul id={id} className="sess-list proj-sub-list" role="group" aria-label={label}>
      {list.map((s) => (
        <SessionRow
          key={s.name}
          s={s}
          selected={ctx.activeSession === s.name}
          opens={ctx.sPanes?.get(s.name) || []}
          multi={ctx.multiPane}
          running={ctx.running}
          actions={actions}
          detail={detailOf?.(s)}
        />
      ))}
    </ul>
  );

  return (
    <Section
      id="terminals"
      title={tr("terms.title")}
      icon="terminal"
      count={all.length}
      unread={unread}
      actions={
        <>
          <IconButton icon="add" label={tr("terms.new_shell")} disabled={!ctx.running} onClick={() => setNewShell(true)} />
          <IconButton icon="vm" label={tr("terms.new_ssm")} disabled={!ctx.running} onClick={() => openStartAt("ssm")} />
          <IconButton icon="archive" label={tr("terms.tidy")} disabled={stopped.length === 0} onClick={() => void actions.archiveStopped(stopped)} />
        </>
      }
    >
      <div ref={rootRef}>
        {all.length === 0 ? (
          <div className="section-empty">{tr("terms.empty")}</div>
        ) : (
          <ul className="sess-list space-list" ref={rail.ref} role="tree" aria-label={tr("terms.title")} onKeyDown={rail.onKeyDown}>
            {groups.shells.length > 0 && (
              <li role="presentation" className="term-sub-head">
                {tr("terms.shells")}
              </li>
            )}
            {groups.shells.map((g) => {
              const { label, sub, icon } = shellLabel(g);
              const id = `${idBase}-${g.key}`;
              return (
                <li key={g.key} className="proj-node space-group">
                  {head(g.key, icon, label, sub, id)}
                  {rowsOf(g.sessions, id, label)}
                </li>
              );
            })}
            {groups.ssm.length > 0 && (
              <li role="presentation" className="term-sub-head">
                {tr("terms.ssm")}
              </li>
            )}
            {groups.ssm.map((g) => {
              const { label, sub } = ssmLabel(g);
              const id = `${idBase}-${g.key}`;
              return (
                <li key={g.key} className="proj-node space-group">
                  {head(g.key, "vm", label, sub, id)}
                  {rowsOf(g.sessions, id, label, ssmRowDetail)}
                </li>
              );
            })}
          </ul>
        )}
      </div>
      {newShell && <NewShellModal onClose={() => setNewShell(false)} />}
    </Section>
  );
});
