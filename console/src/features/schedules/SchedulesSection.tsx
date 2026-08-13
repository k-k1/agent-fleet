// SchedulesSection (docs/38 P5) — the left-rail view of operator-authored scheduled
// executions. Membership-scoped and Control-Plane persisted (like the memo queue), so it
// shows in both running and stopped workspace states and refetches on mount / tenant
// switch + slow-polls while mounted. Read + manage only: toggle enabled, run-now, view run
// history, delete. Creating/editing a schedule stays in the operator conversation because
// it needs the NL→cron translation the operator LLM does.
//
// Row interaction (docs/38): clicking a row opens its run history inline; the ⋯ menu (also
// on right-click) holds the manage actions — pause/resume, run-now, delete. Each history
// row shows the outcome (成功/失敗/スキップ), whether it was a manual run-now or a scheduled
// fire, and opens the session that fire drove.
import { createPortal } from "react-dom";
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { agentOf } from "../../agents/registry.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openChat } from "../chat/open.ts";
import { chatList } from "../chat/api.ts";
import { useChatStore, ensureConvs } from "../chat/store.ts";
import { sessionFolder } from "../../lib/project.ts";
import { useSettings } from "../../lib/settings.ts";
import {
  useActiveWorkingSet,
  workingSetList,
  toggleWorkingSetMember,
  scheduleInSet,
} from "../../lib/workingSetsStore.ts";
import type { WorkingSet, ScheduleSetContext } from "../../lib/workingSetsStore.ts";
import {
  scheduleList,
  scheduleRuns,
  schedulePause,
  scheduleResume,
  scheduleRunNow,
  scheduleDelete,
} from "./api.ts";
import { ScheduleDetailModal } from "./ScheduleDetailModal.tsx";
import {
  type ScheduleDTO,
  type ScheduleRun,
  readScheduleList,
  scheduleTitle,
  specSummary,
  statusTone,
  statusIcon,
  runStatusLabelKey,
  isManualRun,
  sortSchedules,
} from "./read.ts";

const POLL_MS = 15000;

// Compact local timestamp for last_run / fired_at (RFC3339 UTC → the viewer's locale).
// next_run uses the server-rendered next_run_local (already in the schedule's own tz).
function shortLocal(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

// Open the session a fire drove. new-mode sessions take the schedule's agent kind, so a
// chat-capable kind opens the mirror and a terminal kind opens the terminal pane.
// An assistant fire (docs/38 session_mode=assistant) records the CONVERSATION slug
// ("a"+6 chars) instead of a session name — resolve it via the chat list and open the
// assistant chat pane.
function openRunSession(agentKind: string | undefined, name: string): void {
  if (/^a[a-z2-7]{6}$/.test(name)) {
    void chatList()
      .then((r) => {
        const conv = (r.conversations || []).find((c) => c.slug === name);
        if (conv) openChat(conv.id);
      })
      .catch(() => {});
    return;
  }
  (agentOf(agentKind || "claude").caps.chat ? openSessionChat : openSessionTerminal)(name);
}

interface ScheduleRowProps {
  s: ScheduleDTO;
  rowBusy: boolean;
  runsOpen: boolean;
  runs: ScheduleRun[] | undefined;
  /** 作業グループ (docs/52): the defined sets + the derivation context, for the
   * ⋯ menu's membership toggles. */
  wsets: WorkingSet[];
  wctx: ScheduleSetContext;
  onToggleRuns: (s: ScheduleDTO) => void;
  onDetail: (s: ScheduleDTO) => void;
  onPause: (s: ScheduleDTO) => void;
  onRunNow: (s: ScheduleDTO) => void;
  onDelete: (s: ScheduleDTO) => void;
}

function ScheduleRow({ s, rowBusy, runsOpen, runs, wsets, wctx, onToggleRuns, onDetail, onPause, onRunNow, onDelete }: ScheduleRowProps) {
  const tr = useT();
  const paused = !s.enabled;
  const tone = statusTone(s.last_status);

  // ⋯ menu (also opened by right-click on the row), positioned like SessionRow's menu.
  const menuWrapRef = useRef<HTMLDivElement>(null);
  const menuElRef = useRef<HTMLDivElement>(null);
  const menuBtnRef = useRef<HTMLButtonElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  useDismiss([menuWrapRef, menuElRef], menuOpen, () => setMenuOpen(false));
  useMenuRoving(menuElRef, menuOpen);
  useLayoutEffect(() => {
    const el = menuElRef.current;
    const anchor = menuBtnRef.current;
    if (!menuOpen || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 2, menuBtnRef.current?.closest<HTMLElement>(".app-rail"));
  });

  const runAction = (fn: (s: ScheduleDTO) => void) => {
    setMenuOpen(false);
    fn(s);
  };

  return (
    <div className={"sched-row" + (paused ? " paused" : "")}>
      {/* Clicking the row body toggles the run history; the ⋯ menu holds the actions. */}
      <div
        className="sched-main"
        role="button"
        tabIndex={0}
        aria-expanded={runsOpen}
        onClick={() => onToggleRuns(s)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onToggleRuns(s);
          }
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          setMenuOpen(true);
        }}
      >
        <span className={"sched-dot tone-" + tone} title={s.last_status || tr("sched.never_run")}>
          <Icon name={statusIcon(s.last_status)} />
        </span>
        <div className="sched-info">
          <div className="sched-name" title={scheduleTitle(s)}>
            {scheduleTitle(s)}
            {paused && <span className="sched-badge">{tr("sched.paused_tag")}</span>}
          </div>
          <div className="sched-sub">
            <code className="sched-spec">{specSummary(s)}</code>
            {s.agent_kind && <span className="sched-kind">{s.agent_kind}</span>}
          </div>
          <div className="sched-times">
            {!paused && s.next_run_local && (
              <span className="sched-next" title={tr("sched.next_run")}>
                <Icon name="arrow-small-right" /> {s.next_run_local}
              </span>
            )}
            {s.last_run && (
              <span className="sched-last" title={tr("sched.last_run")}>
                <Icon name="history" /> {shortLocal(s.last_run)}
              </span>
            )}
          </div>
        </div>
        {/* Chevron hints the row expands; the ⋯ menu carries the manage actions. */}
        <Icon name={runsOpen ? "chevron-down" : "chevron-right"} className="sched-caret" />
        <div className="sched-menu-wrap" ref={menuWrapRef} onClick={(e) => e.stopPropagation()}>
          <button
            type="button"
            className="sched-menu-btn"
            title={tr("sched.menu")}
            aria-label={tr("sched.menu")}
            ref={menuBtnRef}
            onClick={() => setMenuOpen((v) => !v)}
          >
            <Icon name="ellipsis" />
          </button>
          {menuOpen &&
            createPortal(
              <div className="ui-menu sched-menu" ref={menuElRef} onMouseDown={(e) => e.stopPropagation()}>
                <button type="button" className="ui-menu-item" onClick={() => runAction(onDetail)}>
                  <Icon name="edit" /> {tr("sched.detail")}
                </button>
                <button type="button" className="ui-menu-item" disabled={rowBusy} onClick={() => runAction(onPause)}>
                  <Icon name={paused ? "debug-start" : "debug-pause"} /> {paused ? tr("sched.resume") : tr("sched.pause")}
                </button>
                <button
                  type="button"
                  className="ui-menu-item"
                  disabled={rowBusy || paused}
                  onClick={() => runAction(onRunNow)}
                >
                  <Icon name="play" /> {tr("sched.run_now")}
                </button>
                {/* 作業グループ (docs/52): direct-assignment toggles. A membership
                    DERIVED from the schedule's repo / owner conversation / reuse
                    target shows checked-but-disabled — it moves with that entity,
                    not with this toggle. */}
                {wsets.length > 0 && (
                  <>
                    <div className="ui-menu-sep" role="separator" />
                    <div className="ui-menu-caption">{tr("wset.menu_caption")}</div>
                    {wsets.map((w) => {
                      const direct = w.schedules.includes(s.id);
                      const derived = !direct && scheduleInSet(w, s, wctx);
                      return (
                        <button
                          key={w.id}
                          type="button"
                          className="ui-menu-item"
                          disabled={derived}
                          title={derived ? tr("wset.derived_hint") : undefined}
                          onClick={() => {
                            setMenuOpen(false);
                            toggleWorkingSetMember(w.id, "schedules", s.id);
                          }}
                        >
                          <Icon name="check" className={direct || derived ? "wset-check" : "wset-check off"} /> {w.name}
                        </button>
                      );
                    })}
                    <div className="ui-menu-sep" role="separator" />
                  </>
                )}
                <button type="button" className="ui-menu-item danger" disabled={rowBusy} onClick={() => runAction(onDelete)}>
                  <Icon name="trash" /> {tr("common.delete")}
                </button>
              </div>,
              document.body,
            )}
        </div>
      </div>

      {runsOpen && (
        <div className="sched-runs">
          {runs === undefined ? (
            <div className="sched-runs-empty">
              <Icon name="loading" spin /> {tr("sched.loading")}
            </div>
          ) : runs.length === 0 ? (
            <div className="sched-runs-empty">{tr("sched.no_runs")}</div>
          ) : (
            runs.map((run, i) => {
              const manual = isManualRun(run.trigger);
              const openable = !!run.session;
              return (
                <div
                  key={i}
                  className={"sched-run" + (openable ? " openable" : "")}
                  role={openable ? "button" : undefined}
                  tabIndex={openable ? 0 : undefined}
                  title={openable ? tr("sched.open_session", { name: run.session as string }) : run.detail || run.status}
                  onClick={openable ? () => openRunSession(s.agent_kind, run.session as string) : undefined}
                  onKeyDown={
                    openable
                      ? (e) => {
                          if (e.key === "Enter" || e.key === " ") {
                            e.preventDefault();
                            openRunSession(s.agent_kind, run.session as string);
                          }
                        }
                      : undefined
                  }
                >
                  <span className={"sched-dot tone-" + statusTone(run.status)}>
                    <Icon name={statusIcon(run.status)} />
                  </span>
                  <span className="sched-run-time">{shortLocal(run.fired_at)}</span>
                  <span className={"sched-run-label tone-" + statusTone(run.status)} title={run.detail || run.status}>
                    {tr(runStatusLabelKey(run.status))}
                  </span>
                  <span className={"sched-run-trigger" + (manual ? " manual" : "")}>
                    {manual ? tr("sched.trigger_manual") : tr("sched.trigger_scheduled")}
                  </span>
                  {openable && <Icon name="link-external" className="sched-run-open" />}
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}

export const SchedulesSection = memo(function SchedulesSection() {
  const tenant = useTenantStore((s) => s.tenant);
  const toast = useToast();
  const askConfirm = useConfirm();
  const tr = useT();

  const [items, setItems] = useState<ScheduleDTO[]>([]);
  const [loaded, setLoaded] = useState(false);
  // 取得失敗の理由（"" = 成功）。空一覧と失敗を同じ見た目にしないための state。
  const [loadErr, setLoadErr] = useState("");
  // 「再試行」で load を走らせ直すための世代カウンタ（effect の dep）。
  const [reloadTick, setReloadTick] = useState(0);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [openRuns, setOpenRuns] = useState<string | null>(null);
  const [runs, setRuns] = useState<Record<string, ScheduleRun[]>>({});
  const [detail, setDetail] = useState<ScheduleDTO | null>(null);
  const serRef = useRef("");
  // 作業グループ (docs/52): CP 永続のスケジュールは所属を導出する — repo /
  // owner_conv(作成元会話) / reuse_target(会話 slug・セッション名)。導出できない
  // ものは行メニューの直接割当（set.schedules）で。
  const wset = useActiveWorkingSet();
  const wsets = workingSetList(useSettings());
  const sessions = useSessionsStore((s) => s.sessions);
  const convs = useChatStore((s) => s.convs);
  // slug→id 解決（assistant 発火の reuse_target）に会話一覧が要る。AssistantSection が
  // 未マウントでも一度だけ読み込む。グループ未定義なら不要。
  useEffect(() => {
    if (wsets.length) void ensureConvs();
  }, [wsets.length]);
  const wctx = useMemo<ScheduleSetContext>(
    () => ({
      convIdBySlug: (slug) => (convs ?? []).find((c) => c.slug === slug)?.id,
      folderOfSession: (name) => {
        const x = sessions.find((ss) => ss.name === name);
        return x ? sessionFolder(x) : undefined;
      },
    }),
    [convs, sessions],
  );

  // Refetch on mount / tenant switch / retry, and poll while mounted (CP is pull-only —
  // no push). A failed fetch keeps the rows already on screen and surfaces the reason: it
  // must never read as "there are no schedules" (readScheduleList).
  useEffect(() => {
    let alive = true;
    const load = () =>
      scheduleList()
        .then((res) => {
          if (!alive) return;
          const { items: arr, error } = readScheduleList(res);
          if (!arr) {
            setLoadErr(errText(error) || t("sched.load_failed"));
            return;
          }
          setLoadErr("");
          const ser = JSON.stringify(arr);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setItems(arr);
          }
          setLoaded(true);
        })
        .catch(() => {
          // Thrown = the network dropped (api() resolves CP errors instead).
          if (alive) setLoadErr(t("sched.load_failed"));
        });
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [tenant, reloadTick]);

  const sorted = useMemo(() => sortSchedules(items), [items]);
  const scoped = useMemo(() => (wset ? sorted.filter((s) => scheduleInSet(wset, s, wctx)) : sorted), [sorted, wset, wctx]);
  const activeCount = useMemo(() => scoped.filter((s) => s.enabled).length, [scoped]);

  const setRowBusy = (id: string, v: boolean) => setBusy((b) => ({ ...b, [id]: v }));

  // Apply a returned DTO to local state so the row updates without waiting for the poll.
  const applyDTO = (dto: ScheduleDTO | { error?: unknown }) => {
    if (dto && typeof dto === "object" && "id" in dto) {
      const s = dto as ScheduleDTO;
      setItems((cur) => cur.map((x) => (x.id === s.id ? s : x)));
      serRef.current = ""; // force the next poll to re-apply (order may have changed)
      return true;
    }
    return false;
  };

  const doToggle = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    setRowBusy(s.id, true);
    try {
      const res = s.enabled ? await schedulePause(s.id) : await scheduleResume(s.id);
      if (!applyDTO(res)) {
        toast(t("sched.action_failed"), { kind: "warn" });
        return;
      }
      toast(s.enabled ? t("sched.paused") : t("sched.resumed"), { kind: "success" });
    } catch {
      toast(t("sched.action_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const doRunNow = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    setRowBusy(s.id, true);
    try {
      const res = await scheduleRunNow(s.id);
      if (!applyDTO(res)) {
        toast(t("sched.action_failed"), { kind: "warn" });
        return;
      }
      const warning = (res as ScheduleDTO).warning;
      if (warning) toast(warning, { kind: "warn", duration: 8000 });
      else toast(t("sched.run_now_queued"), { kind: "success" });
    } catch {
      toast(t("sched.action_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const doDelete = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    // 他の破壊操作と同じ ConfirmDialog（ブロッキングな native confirm() は使わない）。
    const ok = await askConfirm({
      title: t("sched.delete_title"),
      body: t("sched.delete_confirm", { name: scheduleTitle(s) }),
      confirmLabel: t("common.delete"),
      danger: true,
    });
    if (!ok) return;
    setRowBusy(s.id, true);
    try {
      const r = await scheduleDelete(s.id);
      if (!r.ok) {
        toast(t("common.delete_failed"), { kind: "warn" });
        return;
      }
      setItems((cur) => cur.filter((x) => x.id !== s.id));
      if (openRuns === s.id) setOpenRuns(null);
      toast(t("sched.deleted"), { kind: "success" });
    } catch {
      toast(t("common.delete_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const toggleRuns = async (s: ScheduleDTO) => {
    if (openRuns === s.id) {
      setOpenRuns(null);
      return;
    }
    setOpenRuns(s.id);
    try {
      const res = await scheduleRuns(s.id);
      const list = res && typeof res === "object" && "runs" in res ? (res as { runs: ScheduleRun[] }).runs : [];
      setRuns((r) => ({ ...r, [s.id]: Array.isArray(list) ? list : [] }));
    } catch {
      setRuns((r) => ({ ...r, [s.id]: [] }));
    }
  };

  return (
    <Section id="schedules" title={tr("sched.title")} icon="watch" count={activeCount}>
      {/* 取得失敗は「空」と別物として出す（直前まで表示していた行は残す）。 */}
      {loadErr && (
        <div className="sched-load-err" role="status" title={loadErr}>
          <Icon name="warning" />
          <span className="sched-load-err-msg">{tr("sched.load_failed")}</span>
          <button type="button" className="sched-retry" onClick={() => setReloadTick((n) => n + 1)}>
            {tr("sched.retry")}
          </button>
        </div>
      )}
      {!loadErr && loaded && items.length === 0 ? (
        <div className="pane-empty">{tr("sched.empty")}</div>
      ) : !loadErr && loaded && scoped.length === 0 ? (
        // グループで絞った結果の空（スケジュール自体はある）— 真の空とは区別する。
        <div className="pane-empty">{tr("wset.no_schedules")}</div>
      ) : (
        <div className="sched-list">
          {scoped.map((s) => (
            <ScheduleRow
              key={s.id}
              s={s}
              rowBusy={!!busy[s.id]}
              runsOpen={openRuns === s.id}
              runs={openRuns === s.id ? runs[s.id] : undefined}
              wsets={wsets}
              wctx={wctx}
              onToggleRuns={(x) => void toggleRuns(x)}
              onDetail={(x) => setDetail(x)}
              onPause={(x) => void doToggle(x)}
              onRunNow={(x) => void doRunNow(x)}
              onDelete={(x) => void doDelete(x)}
            />
          ))}
        </div>
      )}
      {detail && (
        <ScheduleDetailModal
          s={detail}
          onClose={() => setDetail(null)}
          onSaved={(dto) => {
            applyDTO(dto);
            setDetail(null);
          }}
        />
      )}
    </Section>
  );
});
