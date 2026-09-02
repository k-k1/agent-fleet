// MemoryTab — エージェントメモリの版管理（docs/log/39 P2 / ADR 0022・採用既定値 #3）。
//
// エージェントが書き溜める永続メモリ（claude の auto-memory / codex の memories）は
// 「消えない置き場」にある一方で**履歴が無い**。このタブはその履歴・差分・巻き戻しの
// 操作面で、実体（git bare repo）は Agent 側にあり、ここは REST を叩くだけ。
//
// 画面は上から: ①対象ルートの概況＋自動取得トグル＋手動スナップショット
// ②スナップショット履歴（左）と選択行の差分（右） ③戻し操作（範囲を選んで確認ダイアログ）
// ④持ち出し / 取り込み（P3。bundle=全履歴 / tar.gz=最新のみ、取り込みは選択置き換え）。
// 差分の描画は SCM のコミットペインと同じ <Diff> を流用する（見え方を 2 つに増やさない）。
//
// 巻き戻しは履歴を書き換えず、適用前に pre-restore スナップショットを自動で積む
// （docs/log/39 ★2）。つまりこの画面のどの操作も後から取り消せる — 確認ダイアログの
// 文面もその前提で書いている。

import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Diff } from "../scm/GitDiff.tsx";
import { OnOff, Row } from "./controls.tsx";
import { useT, tMaybe } from "../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../lib/intl.ts";
import { humanSize } from "../../lib/filemeta.ts";
import type { RootsPayload, Snapshot } from "./memoryTypes.ts";
import type { RestoreBody } from "./memoryRestore.tsx";
import type { RestoreScopeState } from "./memoryRestore.tsx";
import { RestorePanel } from "./memoryRestore.tsx";
import { TransferSection } from "./memoryTransfer.tsx";

// 契機ラベル。Agent が返す trailer 値（auto/manual/pre-restore/restore/import）を
// そのままキーに使い、未知の値は生のまま出す（新しい契機を足しても画面は壊れない）。
const triggerLabel = (trigger: string): string =>
  tMaybe("mem.trigger_" + trigger.replace(/-/g, "_")) ?? trigger ?? "";

// 無効なルートの理由。契機ラベルと同じ流儀で、未知の理由は生のまま出す
// （Agent が新しい理由を返しても画面は壊れない）。
const reasonLabel = (reason: string): string => tMaybe("mem.reason_" + reason) ?? reason ?? "";

export function MemoryTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";

  const [data, setData] = useState<RootsPayload | null>(null);
  const [snaps, setSnaps] = useState<Snapshot[] | null>(null);
  const [err, setErr] = useState("");
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);

  // 選択中のスナップショット（履歴行）と、その差分。
  const [sel, setSel] = useState("");
  const [diff, setDiff] = useState<string | null>(null);
  // 日時指定ジャンプ（datetime-local の値）。
  const [at, setAt] = useState("");
  // 戻し操作のパネル。null = 閉じている。
  const [scope, setScope] = useState<RestoreScopeState | null>(null);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true; // 停止中は叩かない（起動後に deps で再実行）
      const [r, s] = await Promise.all([
        api("api/agents/memory/roots"),
        api("api/agents/memory/snapshots?limit=100"),
      ]);
      if (signal.aborted) return true;
      if (isTransientErr(r) || isTransientErr(s)) return false;
      if (r?.error) {
        setErr(errText(r.error));
        return true;
      }
      setErr("");
      setData(r);
      setSnaps(s?.error ? [] : (s?.snapshots ?? []));
      return true;
    },
    [running],
  );
  useRetryLoad(load, [running, reload]);

  // 履歴が入れ替わったら選択を追従させる（消えた rev を掴んだままにしない）。
  useEffect(() => {
    if (!snaps?.length) {
      setSel("");
      return;
    }
    setSel((cur) => (cur && snaps.some((s) => s.rev === cur) ? cur : snaps[0].rev));
  }, [snaps]);

  // 選択行の差分（省略時 = 「その時点が入れた変更」）。
  useEffect(() => {
    if (!sel) {
      setDiff(null);
      return;
    }
    let live = true;
    setDiff(null);
    api("api/agents/memory/diff?to=" + encodeURIComponent(sel))
      .then((d) => {
        if (!live) return;
        setDiff(d?.error ? "" : (d.diff ?? ""));
      })
      .catch(() => live && setDiff(""));
    return () => {
      live = false;
    };
  }, [sel]);

  const setAuto = async (on: boolean) => {
    const res = await apiJSON("api/agents/memory/settings", "PUT", { auto: on });
    if (res?.error) {
      toast(tr("common.save_failed"));
      return;
    }
    setData((d) => (d ? { ...d, auto: !!res.auto, autoLocked: !!res.autoLocked } : d));
  };

  // codex memories のフリート有効化（docs/log/39 P4・決着 #4）。設定の実体は codex 自身の
  // config.toml なので、書き込みは既存の /codex/settings を使う。有効化しても
  // ~/.codex/memories は次に codex が走るまで生えないため、直後は「有効だが未生成」で
  // 出る（roots を読み直して状態を Agent 側から取り直す）。
  const setCodexMemories = async (on: boolean) => {
    const res = await apiJSON("api/codex/settings", "PUT", { memories: on });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    toast(on ? tr("mem.codex_enabled") : tr("mem.codex_disabled_toast"));
    setReload((n) => n + 1);
  };

  const snapshotNow = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/agents/memory/snapshots", "POST", { trigger: "manual" });
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(res.committed ? tr("mem.snapshot_taken") : tr("mem.snapshot_unchanged"));
      setReload((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  // 日時指定: 「その時刻以前の直近スナップショット」へ選択を移す（巻き戻しと同じ意味論）。
  const jumpTo = async () => {
    if (!at) return;
    const iso = new Date(at).toISOString();
    const res = await api("api/agents/memory/snapshots?limit=1&before=" + encodeURIComponent(iso));
    const hit = res?.snapshots?.[0];
    if (!hit) {
      toast(tr("mem.jump_none"));
      return;
    }
    // 履歴は新しい順なので、先頭へ差し込まず at 降順を保ったまま挿入する
    // （limit 外の古いスナップショットが一覧の先頭に来て時系列が崩れないように）。
    if (!snaps?.some((s) => s.rev === hit.rev))
      setSnaps((cur) => [...(cur ?? []), hit].sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0)));
    setSel(hit.rev);
  };

  const runRestore = async (rev: string, body: RestoreBody, label: string) => {
    const anyBusy = (data?.roots ?? []).some((r) => r.busy);
    const ok = await askConfirm({
      title: tr("mem.restore_confirm_title"),
      body: (
        <>
          <p>{tr("mem.restore_confirm_body", { scope: label })}</p>
          <p className="muted">{tr("mem.restore_undo_hint")}</p>
          {anyBusy && <p className="mem-warn">{tr("mem.restore_busy_warn")}</p>}
        </>
      ),
      confirmLabel: tr("mem.restore_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // rev はクエリにも載せる — CP の監査台帳は URL からしか target を採らないため
      // （本文は読まない）。実処理に使われるのは本文側。
      const res = await apiJSON(
        "api/agents/memory/restore?rev=" + encodeURIComponent(rev),
        "POST",
        body,
      );
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(
        res.committed
          ? tr("mem.restored", {
              written: res.written?.length ?? 0,
              deleted: res.deleted?.length ?? 0,
            })
          : tr("mem.restore_nochange"),
        { kind: "success", persist: true },
      );
      setScope(null);
      setReload((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  if (!running) {
    return (
      <div className="mem-tab">
        <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
          </Button>
        </EmptyState>
      </div>
    );
  }

  const selected = snaps?.find((s) => s.rev === sel) ?? null;

  return (
    <div className="mem-tab">
      <p className="muted ds-note">{tr("mem.intro")}</p>
      {err && <p className="mem-warn">{err}</p>}

      {/* ① 対象と取得契機 */}
      <section className="mem-section">
        <div className="mem-head">
          <h3>{tr("mem.roots_title")}</h3>
          <span className="muted">
            {data?.lastSnapshot
              ? tr("mem.last_snapshot", { when: fmtDateTime(data.lastSnapshot, DATETIME_FULL) })
              : tr("mem.never")}
          </span>
          <button type="button" disabled={busy} onClick={snapshotNow}>
            {tr("mem.snapshot_now")}
          </button>
        </div>
        {!data ? (
          <p className="muted pad">{tr("common.loading")}</p>
        ) : data.roots.length === 0 && !data.inactive?.length ? (
          <p className="muted pad">{tr("mem.no_roots")}</p>
        ) : (
          <ul className="mem-roots">
            {data.roots.map((r) => (
              <li key={r.kind} className="mem-root">
                <span className={"mem-kind kind-" + r.kind}>{r.label}</span>
                <span className="muted">
                  {tr("mem.root_stats", { files: r.files, size: humanSize(r.bytes) })}
                </span>
                {r.busy && <span className="mem-badge busy">{tr("mem.busy_badge")}</span>}
                {r.toggleable && (
                  <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />
                )}
                {r.projects.length > 0 && (
                  <span className="mem-projects">
                    {r.projects.map((p) => (
                      <span key={p.slug} className="mem-chip" title={p.slug}>
                        {p.display}
                      </span>
                    ))}
                  </span>
                )}
              </li>
            ))}
            {(data.inactive ?? []).map((r) => (
              <li key={r.kind} className="mem-root mem-root-off">
                <span className={"mem-kind kind-" + r.kind}>{r.label}</span>
                <span className="muted">{reasonLabel(r.reason)}</span>
                {r.toggleable && <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />}
              </li>
            ))}
          </ul>
        )}
        {/* 有効化はトークンを継続的に消費する（バックグラウンドの抽出・統合）。
            トグルの隣で必ず伝える — 「ただのスイッチ」に見せない。 */}
        {data?.inactive?.some((r) => r.toggleable && !r.enabled) && (
          <p className="muted ds-hint">{tr("mem.codex_cost_hint")}</p>
        )}
        <Row label={tr("mem.auto_label")}>
          <OnOff value={!!data?.auto} onChange={(v) => void setAuto(v)} />
        </Row>
        <p className="muted ds-hint">
          {data?.autoLocked ? tr("mem.auto_locked") : tr("mem.auto_hint")}
        </p>
      </section>

      {/* ② 履歴と差分 */}
      <section className="mem-section">
        <div className="mem-head">
          <h3>{tr("mem.history_title")}</h3>
          <label className="mem-jump">
            <span className="muted">{tr("mem.jump_at")}</span>
            <input
              type="datetime-local"
              className="cinput"
              value={at}
              onChange={(e) => setAt(e.target.value)}
            />
            <button type="button" disabled={!at} onClick={() => void jumpTo()}>
              {tr("mem.jump_go")}
            </button>
          </label>
        </div>
        <div className="mem-body">
          <ul className="mem-list">
            {snaps === null ? (
              <li className="muted pad">{tr("common.loading")}</li>
            ) : snaps.length === 0 ? (
              <li className="muted pad">{tr("mem.history_empty")}</li>
            ) : (
              snaps.map((s) => (
                <li key={s.rev}>
                  <button
                    type="button"
                    className={"mem-snap" + (s.rev === sel ? " active" : "")}
                    aria-current={s.rev === sel ? "true" : undefined}
                    onClick={() => setSel(s.rev)}
                  >
                    <span className="mem-snap-when">{fmtDateTime(s.at, DATETIME_FULL)}</span>
                    <span className={"mem-badge trig-" + s.trigger}>{triggerLabel(s.trigger)}</span>
                    <span className="mem-snap-what muted">
                      {s.projects.length > 0
                        ? s.projects.map((p) => p.display).join(tr("common.list_sep"))
                        : s.kinds.join(tr("common.list_sep"))}
                      {" · "}
                      {tr("mem.n_files", { n: s.files })}
                    </span>
                  </button>
                </li>
              ))
            )}
          </ul>
          <div className="mem-diff">
            {selected && (
              <div className="mem-diff-head">
                <code title={selected.rev}>{selected.short}</code>
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => setScope({ rev: selected.rev, all: true, picked: {}, tree: null })}
                >
                  {tr("mem.restore")}
                </button>
              </div>
            )}
            {diff === null ? (
              <pre className="diff muted">{tr("common.loading")}</pre>
            ) : (
              <Diff text={diff} embedded wrap />
            )}
          </div>
        </div>
      </section>

      {/* ③ 戻し範囲の選択 */}
      {scope && (
        <RestorePanel
          state={scope}
          patch={(p) => setScope((cur) => (cur ? { ...cur, ...p } : cur))}
          onClose={() => setScope(null)}
          onSubmit={runRestore}
          busy={busy}
        />
      )}

      {/* ④ 持ち出し / 取り込み */}
      <TransferSection busy={busy} setBusy={setBusy} onApplied={() => setReload((n) => n + 1)} />
    </div>
  );
}
