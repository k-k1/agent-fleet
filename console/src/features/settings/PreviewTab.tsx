// PreviewTab — プレビュー用サブドメイン（docs/log/81）の Workspace 単位の設定。
//
// もとは「ツールチェーン」タブ（EnvTab）の一節だった。タイムゾーンや JDK の選択と、
// 「どのポートを外に出すか」「ログインなしで公開するか」は読む人も判断の重さも別物で、
// 公開設定が言語のバージョン選択の下にぶら下がっているのは見つけにくい。独立したタブに
// 分ける。
//
// ★ レールに出すのは、このデプロイでプレビュー用サブドメインが**発行される**ときだけ
//   （CP の AF_PREVIEW_DOMAIN が空なら previewDomain も空 = 何をしても URL は出ない）。
//   「押しても何も起きない設定」を置かないのが目的で、判定は usePreviewAvailable。
//
// 保存は CP 所有の ws-settings（PUT /api/env/ws-settings）なので、ワークスペースが停止中
// でも編集できる。発行済み URL の一覧だけは起動中にしか無い（停止すると捨てられる）。
//
// i18n キーは `env.preview_*` のまま（タブを分けただけで文言は同じもの。キー名は識別子で
// あって置き場所ではないので、移設のためだけに全部を書き換えない）。
import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, apiJSON } from "../../core/api/client.ts";
import { OnOff, Row } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";

// このデプロイにプレビュー用サブドメインが在るか。null = まだ分からない（確定するまで
// レールに出さない＝一瞬出てから消えるのを避ける）。previewDomain はデプロイ固定の値
// なので、一度確定したらページ内で使い回す（設定を開くたびに GET し直さない）。
let availCache: boolean | null = null;

export function usePreviewAvailable(): boolean | null {
  const [ok, setOk] = useState<boolean | null>(availCache);
  useEffect(() => {
    if (availCache !== null) return;
    let cancelled = false;
    void api("api/env/ws-settings").then(
      (res: any) => {
        const v = !!(res && !res.error && res.previewDomain);
        availCache = v;
        if (!cancelled) setOk(v);
      },
      // 取得できなかっただけ（CP に届かない等）は「無い」とは違うのでキャッシュしない。
      // ただし今回の描画では出さない——存在しない設定を見せるよりは伏せるほうがまし。
      () => {
        if (!cancelled) setOk(false);
      },
    );
    return () => {
      cancelled = true;
    };
  }, []);
  return ok;
}

export function PreviewTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  // { previewDomain, previewUrls, previewPorts, previewPublic, … } | null
  const [au, setAu] = useState<any>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void api("api/env/ws-settings").then(
      (res: any) => {
        if (cancelled) return;
        if (res && !res.error) setAu(res);
        else setErr(true);
      },
      () => !cancelled && setErr(true),
    );
    return () => {
      cancelled = true;
    };
  }, []);

  const save = async (patch: Record<string, unknown>) => {
    const res = await apiJSON("api/env/ws-settings", "PUT", patch);
    if (res && !res.error) setAu(res);
    else toast(tr("common.save_failed"));
  };

  // 再発行（docs/log/81 §4.1）: 配ってしまった URL をその場で捨てる。取り消せないので
  // 確認を挟み、何が起きるか（今開いているタブが 404 になる）を先に言う。
  const reissue = async () => {
    const ok = await askConfirm({
      title: tr("env.preview_reissue_confirm_title"),
      body: tr("env.preview_reissue_confirm_body"),
      confirmLabel: tr("env.preview_reissue_go"),
      danger: true,
    });
    if (!ok) return;
    const res = await apiJSON("api/env/ws-settings/preview/reissue", "POST", {});
    if (res && !res.error) {
      setAu(res);
      // ★ 成功しても停止中は画面が変わらない（発行済みの URL が無いので捨てる先が
      // 無い）。黙って終えると「押しても無反応」になるので、どちらだったかを必ず言う。
      toast(tr(res.previewReissued ? "env.preview_reissue_done" : "env.preview_reissue_nothing"));
    } else toast(tr("common.save_failed"));
  };

  return (
    <div className="display-settings">
      {!au ? (
        <p className="muted pad">{err ? tr("env.fetch_failed") : tr("common.loading")}</p>
      ) : !au.previewDomain ? (
        // レールからは隠れているので普通は来ない。前回開いたタブの記憶や古いリンクで
        // 直接来たときに、白紙ではなく「このデプロイには無い」と言う。
        <p className="muted pad">{tr("env.preview_unavailable")}</p>
      ) : (
        <PreviewSection au={au} save={save} reissue={reissue} />
      )}
    </div>
  );
}

function PreviewSection({
  au,
  save,
  reissue,
}: {
  au: any;
  save: (patch: Record<string, unknown>) => void;
  reissue: () => void;
}) {
  const tr = useT();
  const [ports, setPorts] = useState((au.previewPorts || []).join(", "));
  // 発行済みの URL（停止中は空 = CP が発行されていないものを返さない）。ポート順に
  // 並べる —— オブジェクトのキー順に頼ると 8080 が 3000 より前に来ることがある。
  const issuedPorts = Object.keys(au.previewUrls || {}).sort((a, b) => Number(a) - Number(b));
  // 保存は入力欄を離れたときだけ。打鍵ごとに PUT すると、"3000, 80" のような
  // 打ちかけの状態がそのまま保存されて 80 番が許可される。
  const commitPorts = () => {
    const parsed = ports
      .split(/[\s,]+/)
      .map((v: string) => Number(v))
      .filter((n: number) => Number.isInteger(n) && n >= 1 && n <= 65535);
    save({ previewPorts: parsed });
  };
  return (
    <section className="ds-group">
      <h4 className="ds-title">{tr("env.preview_title")}</h4>
      {/* ★ いま何が割り当てられているかを、設定を触る前に見せる。ここが無いと
          「公開するポート」も「再発行」も**見えない何かに効く操作**になり、押しても
          変化が分からない（実際に「再発行が無反応」として報告された）。 */}
      <Row label={tr("env.preview_current_label")}>
        <span className="ds-sub pv-current">
          {issuedPorts.length > 0 ? (
            issuedPorts.map((p) => (
              <a key={p} className="pv-current-url" href={au.previewUrls[p]} target="_blank" rel="noreferrer noopener">
                {au.previewUrls[p].replace(/^https:\/\//, "")}
              </a>
            ))
          ) : (
            <span className="muted">{tr("env.preview_current_none")}</span>
          )}
        </span>
      </Row>
      <p className="muted ds-sub">
        {/* 停止中でもドメインだけは出す —— 「どのドメインに割り当てられているか」は
            URL が発行されていなくても分かってよい情報で、設定の前提そのものである。 */}
        {tr("env.preview_current_note", { domain: au.previewDomain || "" })}
      </p>
      <Row label={tr("env.preview_ports_label")}>
        <input
          className="ds-select"
          value={ports}
          onChange={(e) => setPorts(e.target.value)}
          onBlur={commitPorts}
          onKeyDown={(e) => e.key === "Enter" && commitPorts()}
          placeholder="3000, 8080"
          aria-label={tr("env.preview_ports_label")}
          spellCheck={false}
        />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_ports_note", { n: au.previewMaxPorts || 8 })}</p>
      <Row label={tr("env.preview_fixed_label")}>
        <OnOff value={!!au.previewFixedSlug} onChange={(on) => save({ previewFixedSlug: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_fixed_note")}</p>
      {/* 同じテナントへの共有（docs/log/81 §14）。公開モードの「手前」に置く —— 社内に
          見せたいだけの人が、そのために公開モードへ手を伸ばすのを止めるのが目的。 */}
      <Row label={tr("env.preview_share_label")}>
        <OnOff value={!!au.previewTenantShare} onChange={(on) => save({ previewTenantShare: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_share_note")}</p>
      <Row label={tr("env.preview_public_label")}>
        <OnOff value={!!au.previewPublic} onChange={(on) => save({ previewPublic: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_public_note")}</p>
      <Row label={tr("env.preview_cross_origin_label")}>
        <OnOff value={!!au.previewCrossOrigin} onChange={(on) => save({ previewCrossOrigin: on })} />
      </Row>
      <p className="muted ds-sub">{tr("env.preview_cross_origin_note")}</p>
      <Row label={tr("env.preview_reissue_label")}>
        <button className="ghost" onClick={reissue}>
          {tr("env.preview_reissue")}
        </button>
      </Row>
      <p className="muted ds-sub">{tr("env.preview_reissue_note")}</p>
    </section>
  );
}
