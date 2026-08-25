// MemoryTab — エージェントメモリの版管理（docs/39 P2 / ADR 0022・採用既定値 #3）。
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
// （docs/39 ★2）。つまりこの画面のどの操作も後から取り消せる — 確認ダイアログの
// 文面もその前提で書いている。
import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr, raw } from "../../core/api/client.ts";
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

interface ProjectRef {
  slug: string;
  display: string;
}
interface MemoryRoot {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
  modified?: string;
  busy?: boolean;
  projects: ProjectRef[];
  /** エージェント側がメモリを書くことの ON/OFF（今は codex のみ・docs/39 P4）。 */
  toggleable?: boolean;
  enabled?: boolean;
}
/**
 * 宣言はされているが今この環境では有効でないルート（docs/39 P4）。codex の memories は
 * 上流の既定が OFF なので、黙って一覧から消すと「なぜ出てこないか」も「どう有効化するか」も
 * 伝わらない。toggleable なものはここから直接切り替える。
 */
interface InactiveRoot {
  kind: string;
  label: string;
  reason: string;
  toggleable?: boolean;
  enabled?: boolean;
}
interface RootsPayload {
  roots: MemoryRoot[];
  inactive?: InactiveRoot[];
  auto: boolean;
  autoLocked: boolean;
  lastSnapshot?: string;
}
interface Snapshot {
  rev: string;
  short: string;
  at: string;
  subject: string;
  trigger: string;
  kinds: string[];
  projects: ProjectRef[];
  files: number;
}
interface TreeKind {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
}
interface TreeProject extends ProjectRef {
  files: number;
  bytes: number;
}
/** 秘密情報らしき記述の検出結果。値そのものは Agent 側でマスク済み（hint のみ）。 */
interface SecretFinding {
  path: string;
  line: number;
  rule: string;
  hint: string;
  history?: boolean;
}
/** 取り込んだ系譜の概況（POST import の応答）。適用範囲はここから選ぶ。 */
interface ImportPreview {
  importId: string;
  format: string;
  head: string;
  headTs?: string;
  snapshots: number;
  kinds: TreeKind[];
  projects: TreeProject[];
  unavailable: string[];
  rejected: string[];
  secrets: SecretFinding[];
}

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

  // codex memories のフリート有効化（docs/39 P4・決着 #4）。設定の実体は codex 自身の
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

interface RestoreScopeState {
  rev: string;
  all: boolean;
  /** 個別選択（キーは "project:<slug>" / "kind:<kind>"）。 */
  picked: Record<string, boolean>;
  tree: { kinds: TreeKind[]; projects: TreeProject[] } | null;
}
interface RestoreBody {
  rev: string;
  scope: { all?: boolean; kinds?: string[]; projects?: string[] };
}

// RestorePanel — 戻す範囲を選ぶ。選択肢は**その時点のツリー**から作る（今の live では
// なく）。誤って消したメモリを戻す、が本命のユースケースなので、現在存在しない
// プロジェクトも選べなければ機能として成立しない（docs/39 ④）。
function RestorePanel({
  state,
  patch,
  onClose,
  onSubmit,
  busy,
}: {
  state: RestoreScopeState;
  /** 部分更新。非同期の tree 取得が、その間に変わった選択を巻き戻さないようにするため
      「スナップショットを差し替える」ではなく「差分を当てる」形にしてある。 */
  patch: (p: Partial<RestoreScopeState>) => void;
  onClose: () => void;
  onSubmit: (rev: string, body: RestoreBody, label: string) => Promise<void>;
  busy: boolean;
}) {
  const tr = useT();
  const { rev, all, picked, tree } = state;

  useEffect(() => {
    let live = true;
    api("api/agents/memory/tree?rev=" + encodeURIComponent(rev))
      .then((d) => {
        if (!live || d?.error) return;
        patch({ tree: { kinds: d.kinds ?? [], projects: d.projects ?? [] } });
      })
      .catch(() => {});
    return () => {
      live = false;
    };
    // rev が変わったときだけ取り直す（選択の変更で再取得しない）。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rev]);

  const toggle = (key: string) => patch({ picked: { ...picked, [key]: !picked[key] } });
  // プロジェクト粒度を持たないルート（codex）は「まるごと」しか選べない。
  const wholeKinds = (tree?.kinds ?? []).filter((k) => !k.scopes);
  const projects = tree?.projects ?? [];
  const pickedProjects = projects.filter((p) => picked["project:" + p.slug]).map((p) => p.slug);
  const pickedKinds = wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => k.kind);
  const canSubmit = all || pickedProjects.length > 0 || pickedKinds.length > 0;

  const submit = () => {
    const body: RestoreBody = all
      ? { rev, scope: { all: true } }
      : { rev, scope: { projects: pickedProjects, kinds: pickedKinds } };
    const label = all
      ? tr("mem.scope_all")
      : [...projects.filter((p) => picked["project:" + p.slug]).map((p) => p.display), ...pickedKinds].join(
          tr("common.list_sep"),
        );
    void onSubmit(rev, body, label);
  };

  return (
    <section className="mem-section mem-restore">
      <div className="mem-head">
        <h3>{tr("mem.restore_title")}</h3>
        <code>{rev.slice(0, 8)}</code>
      </div>
      <div className="mem-scope">
        <label>
          <input type="radio" checked={all} onChange={() => patch({ all: true })} />
          {tr("mem.scope_all")}
        </label>
        <label>
          <input type="radio" checked={!all} onChange={() => patch({ all: false })} />
          {tr("mem.scope_pick")}
        </label>
      </div>
      {!all &&
        (tree === null ? (
          <p className="muted pad">{tr("common.loading")}</p>
        ) : projects.length === 0 && wholeKinds.length === 0 ? (
          <p className="muted pad">{tr("mem.tree_empty")}</p>
        ) : (
          <ul className="mem-picks">
            {projects.map((p) => (
              <li key={p.slug}>
                <label title={p.slug}>
                  <input
                    type="checkbox"
                    checked={!!picked["project:" + p.slug]}
                    onChange={() => toggle("project:" + p.slug)}
                  />
                  {p.display}
                  <span className="muted">
                    {" "}
                    {tr("mem.root_stats", { files: p.files, size: humanSize(p.bytes) })}
                  </span>
                </label>
              </li>
            ))}
            {wholeKinds.map((k) => (
              <li key={k.kind}>
                <label>
                  <input
                    type="checkbox"
                    checked={!!picked["kind:" + k.kind]}
                    onChange={() => toggle("kind:" + k.kind)}
                  />
                  {tr("mem.scope_whole_root", { label: k.label })}
                  <span className="muted">
                    {" "}
                    {tr("mem.root_stats", { files: k.files, size: humanSize(k.bytes) })}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        ))}
      <div className="flow">
        <button type="button" disabled={busy || !canSubmit} onClick={submit}>
          {tr("mem.restore_do")}
        </button>
        <button type="button" className="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </button>
      </div>
    </section>
  );
}

// TransferSection — 環境間の持ち出し / 取り込み（docs/39 ⑤ P3）。
//
// 持ち出しは既定が **bundle（全履歴）**。受け側で `git bundle verify` が通り、履歴ごと
// 移せる。tar.gz は「最新だけ軽く持ち出す」用の併設。
//
// 書き出しは Agent 側の secret スキャン（★4）を必ず通る。検出時は 409 が返るので、
// **何が引っかかったかを見せてから** ack 付きで叩き直す。ここで確認を省いて自動 ack
// すると、防御が実質無効になる（値そのものは Agent がマスクしており表示もしない）。
//
// 取り込みは受領（refs/imports へ独立系譜として保持）と適用（選択置き換え）を分ける。
// 受領だけでは live に触れないので、中身を見てから範囲を決められる。
function TransferSection({
  busy,
  setBusy,
  onApplied,
}: {
  busy: boolean;
  setBusy: (v: boolean) => void;
  onApplied: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [format, setFormat] = useState<"bundle" | "tar">("bundle");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const fileRef = useRef<HTMLInputElement>(null);

  // 保存は fetch → Blob → 一時 URL。素のリンク遷移にしないのは、409（secret 検出）を
  // JSON として受け取って確認ダイアログに回す必要があるため。
  const saveBlob = async (res: Response) => {
    const blob = await res.blob();
    const cd = res.headers.get("Content-Disposition") ?? "";
    const name = /filename="?([^";]+)"?/.exec(cd)?.[1] ?? "af-memory." + (format === "tar" ? "tar.gz" : "bundle");
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
    toast(tr("mem.export_done"), { kind: "success" });
  };

  const doExport = async (ack: boolean) => {
    setBusy(true);
    try {
      const res = await raw(
        "api/agents/memory/export?format=" + format + (ack ? "&ack=1" : ""),
      );
      if (res.status === 409) {
        const body = await res.json().catch(() => null);
        const secrets: SecretFinding[] = body?.secrets ?? [];
        const ok = await askConfirm({
          title: tr("mem.export_secret_title"),
          body: (
            <>
              <p>{tr("mem.export_secret_body", { n: secrets.length })}</p>
              <ul className="mem-secrets">
                {secrets.slice(0, 20).map((s, i) => (
                  <li key={i}>
                    <code>{s.rule}</code> {s.path}:{s.line} <span className="muted">{s.hint}</span>
                    {s.history && <span className="muted"> {tr("mem.export_secret_history")}</span>}
                  </li>
                ))}
              </ul>
              <p className="muted">{tr("mem.export_secret_hint")}</p>
            </>
          ),
          confirmLabel: tr("mem.export_anyway"),
          danger: true,
        });
        if (ok) await doExport(true);
        return;
      }
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        toast(body?.error ? errDetail(body.error) : tr("mem.export_failed"));
        return;
      }
      await saveBlob(res);
    } finally {
      setBusy(false);
    }
  };

  const readFile = async (file: File) => {
    setBusy(true);
    setPreview(null);
    try {
      const fd = new FormData();
      fd.append("file", file, file.name);
      const res = await raw("api/agents/memory/import", { method: "POST", body: fd });
      const body = await res.json().catch(() => null);
      if (!res.ok) {
        toast(body?.error ? errDetail(body.error) : tr("mem.import_failed"));
        return;
      }
      setPicked({});
      setPreview(body as ImportPreview);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = ""; // 同じファイルを選び直せるように
    }
  };

  // 適用できるのは「この環境に受け皿があるルート」だけ（codex memories 未有効の環境等）。
  const available = (preview?.kinds ?? []).filter((k) => !(preview?.unavailable ?? []).includes(k.kind));
  const wholeKinds = available.filter((k) => !k.scopes);
  const projects = preview?.projects ?? [];
  const pickedProjects = projects.filter((p) => picked["project:" + p.slug]).map((p) => p.slug);
  const pickedKinds = wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => k.kind);
  const canApply = !!preview && (pickedProjects.length > 0 || pickedKinds.length > 0);

  const applyImport = async () => {
    if (!preview) return;
    const label = [
      ...projects.filter((p) => picked["project:" + p.slug]).map((p) => p.display),
      ...wholeKinds.filter((k) => picked["kind:" + k.kind]).map((k) => tr("mem.scope_whole_root", { label: k.label })),
    ].join(tr("common.list_sep"));
    const ok = await askConfirm({
      title: tr("mem.import_confirm_title"),
      body: (
        <>
          <p>{tr("mem.import_confirm_body", { scope: label })}</p>
          <p className="muted">{tr("mem.restore_undo_hint")}</p>
        </>
      ),
      confirmLabel: tr("mem.import_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // importId はクエリにも載せる（CP の監査台帳は URL からしか target を採らない）。
      const res = await apiJSON(
        "api/agents/memory/import/apply?importId=" + encodeURIComponent(preview.importId),
        "POST",
        { importId: preview.importId, scope: { projects: pickedProjects, kinds: pickedKinds } },
      );
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(
        res.committed
          ? tr("mem.import_done", {
              written: res.written?.length ?? 0,
              deleted: res.deleted?.length ?? 0,
            })
          : tr("mem.import_nochange"),
        { kind: "success", persist: true },
      );
      setPreview(null);
      onApplied();
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="mem-section">
      <div className="mem-head">
        <h3>{tr("mem.transfer_title")}</h3>
      </div>

      {/* 持ち出し */}
      <div className="mem-transfer">
        <div className="mem-scope">
          <label>
            <input
              type="radio"
              checked={format === "bundle"}
              onChange={() => setFormat("bundle")}
            />
            {tr("mem.export_format_bundle")}
          </label>
          <label>
            <input type="radio" checked={format === "tar"} onChange={() => setFormat("tar")} />
            {tr("mem.export_format_tar")}
          </label>
          <button type="button" disabled={busy} onClick={() => void doExport(false)}>
            {tr("mem.export_do")}
          </button>
        </div>
        <p className="muted ds-hint">{tr("mem.export_note")}</p>
      </div>

      {/* 取り込み */}
      <div className="mem-transfer">
        <div className="mem-scope">
          <input
            ref={fileRef}
            type="file"
            className="cinput"
            accept=".bundle,.gz,.tgz,application/gzip,application/octet-stream"
            disabled={busy}
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) void readFile(f);
            }}
          />
        </div>
        <p className="muted ds-hint">{tr("mem.import_hint")}</p>
      </div>

      {preview && (
        <div className="mem-import">
          <p className="muted">
            {tr("mem.import_summary", {
              format: preview.format,
              snapshots: preview.snapshots,
              when: preview.headTs ? fmtDateTime(preview.headTs, DATETIME_FULL) : "-",
            })}
          </p>
          {preview.secrets.length > 0 && (
            <p className="mem-warn">{tr("mem.import_secrets", { n: preview.secrets.length })}</p>
          )}
          {preview.unavailable.length > 0 && (
            <p className="mem-warn">
              {tr("mem.import_unavailable", { kinds: preview.unavailable.join(tr("common.list_sep")) })}
            </p>
          )}
          {preview.rejected.length > 0 && (
            <p className="muted ds-hint" title={preview.rejected.slice(0, 50).join("\n")}>
              {tr("mem.import_rejected", { n: preview.rejected.length })}
            </p>
          )}
          {projects.length === 0 && wholeKinds.length === 0 ? (
            <p className="muted pad">{tr("mem.import_none")}</p>
          ) : (
            <ul className="mem-picks">
              {projects.map((p) => (
                <li key={p.slug}>
                  <label title={p.slug}>
                    <input
                      type="checkbox"
                      checked={!!picked["project:" + p.slug]}
                      onChange={() =>
                        setPicked((cur) => ({ ...cur, ["project:" + p.slug]: !cur["project:" + p.slug] }))
                      }
                    />
                    {p.display}
                    <span className="muted">
                      {" "}
                      {tr("mem.root_stats", { files: p.files, size: humanSize(p.bytes) })}
                    </span>
                  </label>
                </li>
              ))}
              {wholeKinds.map((k) => (
                <li key={k.kind}>
                  <label>
                    <input
                      type="checkbox"
                      checked={!!picked["kind:" + k.kind]}
                      onChange={() =>
                        setPicked((cur) => ({ ...cur, ["kind:" + k.kind]: !cur["kind:" + k.kind] }))
                      }
                    />
                    {tr("mem.scope_whole_root", { label: k.label })}
                    <span className="muted">
                      {" "}
                      {tr("mem.root_stats", { files: k.files, size: humanSize(k.bytes) })}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          <div className="flow">
            <button type="button" disabled={busy || !canApply} onClick={() => void applyImport()}>
              {tr("mem.import_do")}
            </button>
            <button type="button" className="ghost" onClick={() => setPreview(null)}>
              {tr("common.cancel")}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
